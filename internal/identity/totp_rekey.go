package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TOTPRekeyReport counts what RekeyTOTP found. It never carries a secret or a key.
type TOTPRekeyReport struct {
	Total      int
	AlreadyNew int
	FromOld    []int // FromOld[i]: rows that opened under the i-th old key and were (or would be) moved
}

// Rekeyed is the number of rows moved off an old key.
func (r TOTPRekeyReport) Rekeyed() int {
	n := 0
	for _, c := range r.FromOld {
		n += c
	}
	return n
}

// resealTOTP moves one sealed secret to news. It returns the new bytes and which key the row
// was under: -1 for news itself (nothing to change), i for olds[i]. The result is opened
// under news before it is returned.
func resealTOTP(news *sealer, olds []*sealer, accountID string, sealed []byte) ([]byte, int, error) {
	if _, err := news.open(sealed, accountID); err == nil {
		return sealed, -1, nil
	}
	for i, o := range olds {
		plain, err := o.open(sealed, accountID)
		if err != nil {
			continue
		}
		out, err := news.seal(plain, accountID)
		if err != nil {
			return nil, 0, err
		}
		if got, err := news.open(out, accountID); err != nil || !bytes.Equal(got, plain) {
			return nil, 0, errors.New("identity: the re-sealed secret does not open under the new key")
		}
		return out, i, nil
	}
	return nil, 0, errors.New("identity: the secret opens under neither the new nor any old key")
}

// RekeyTOTP re-seals every stored TOTP secret under newSecret (ACCOUNT_TOTP_KEY). oldSecrets
// are the keys the rows may be under now: the previous ACCOUNT_TOTP_KEY, and the session
// pepper for rows sealed before ACCOUNT_TOTP_KEY existed. All rows are handled in one
// transaction, row-locked, so one that opens under none of the keys leaves everything as it
// was. Without apply it only reports (and rolls back). Run it with the service stopped:
// the running process holds the old key.
func RekeyTOTP(ctx context.Context, pool *pgxpool.Pool, newSecret string, oldSecrets []string, apply bool) (TOTPRekeyReport, error) {
	rep := TOTPRekeyReport{FromOld: make([]int, len(oldSecrets))}
	if len(newSecret) < 32 {
		return rep, errors.New("identity: the new key must be at least 32 bytes")
	}
	news := newSealer(newSecret)
	var olds []*sealer
	for _, o := range oldSecrets {
		if o == newSecret {
			return rep, errors.New("identity: an old key equals the new key")
		}
		olds = append(olds, newSealer(o))
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT account_id::text, secret_enc FROM account_two_factor ORDER BY account_id FOR UPDATE`)
		if err != nil {
			return err
		}
		type row struct {
			id     string
			sealed []byte
		}
		all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var v row
			return v, r.Scan(&v.id, &v.sealed)
		})
		if err != nil {
			return err
		}
		rep.Total = len(all)
		for _, v := range all {
			out, from, err := resealTOTP(news, olds, v.id, v.sealed)
			if err != nil {
				return fmt.Errorf("account %s: %w", v.id, err)
			}
			if from < 0 {
				rep.AlreadyNew++
				continue
			}
			rep.FromOld[from]++
			if apply {
				if _, err := tx.Exec(ctx, `UPDATE account_two_factor SET secret_enc=$2 WHERE account_id=$1`, v.id, out); err != nil {
					return err
				}
			}
		}
		if !apply {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return rep, err
}

var errDryRun = errors.New("dry run")
