package mii

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("mii: no Mii")
	uuidRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Stored is one Mii in one format.
type Stored struct {
	Data      []byte // nil when the caller's copy is current
	Version   string
	Name      string
	UpdatedAt time.Time
}

func seedOf(accountID string) ([16]byte, error) {
	var s [16]byte
	h, err := hex.DecodeString(strings.ReplaceAll(accountID, "-", ""))
	if err != nil || len(h) != 16 {
		return s, ErrNotFound
	}
	copy(s[:], h)
	return s, nil
}

// Version is the content hash of the canonical Ver3 data, without its CRC bytes
// changing the answer for equal Miis.
func Version(ver3 []byte) string {
	h := sha256.Sum256(ver3)
	return hex.EncodeToString(h[:])[:16]
}

func nameOf(m *Mii) string {
	n := 0
	for n < len(m.Name) && m.Name[n] != 0 {
		n++
	}
	return string(utf16.Decode(m.Name[:n]))
}

// Put stores data (in format f) as the account's primary Mii. changed is false
// when the account already holds exactly this Mii. Only active and banned
// accounts take a Mii.
func (s *Service) Put(ctx context.Context, accountID string, f Format, data []byte) (version string, changed bool, err error) {
	seed, err := seedOf(accountID)
	if err != nil || !uuidRe.MatchString(accountID) {
		return "", false, ErrNotFound
	}
	m, err := Parse(f, data)
	if err != nil {
		return "", false, err
	}
	// A console writing back the very bytes it was given in its own format has
	// changed nothing. RFL and CharInfo hold less than Ver3, so storing them
	// would quietly degrade the canonical Mii.
	if f != FormatVer3 {
		col := map[Format]string{FormatRFL: "rfl", FormatCharInfo: "char_info"}[f]
		var cur []byte
		var have string
		err := s.pool.QueryRow(ctx, `SELECT m.`+col+`, m.version FROM account_miis m JOIN accounts a ON a.id = m.account_id
			WHERE m.account_id = $1 AND a.status IN ('active','banned')`, accountID).Scan(&cur, &have)
		if err == nil && bytes.Equal(cur, data) {
			return have, false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}
	ver3 := m.EncodeVer3()
	version = Version(ver3)
	tag, err := s.pool.Exec(ctx, `INSERT INTO account_miis (account_id, ver3, rfl, char_info, name, version, source)
		SELECT id, $2, $3, $4, $5, $6, $7 FROM accounts WHERE id = $1 AND status IN ('active','banned')
		ON CONFLICT (account_id) DO UPDATE SET ver3 = EXCLUDED.ver3, rfl = EXCLUDED.rfl, char_info = EXCLUDED.char_info,
			name = EXCLUDED.name, version = EXCLUDED.version, source = EXCLUDED.source, updated_at = now()
		WHERE account_miis.version <> EXCLUDED.version`,
		accountID, ver3, m.EncodeRFL(seed), m.EncodeCharInfo(seed), nameOf(m), version, f.String())
	if err != nil {
		return "", false, fmt.Errorf("mii: put: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return version, true, nil
	}
	// Nothing written: either the same Mii is already stored, or no such account.
	var have string
	err = s.pool.QueryRow(ctx, `SELECT version FROM account_miis WHERE account_id = $1`, accountID).Scan(&have)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return version, false, err
}

// Get returns the Mii in format f. With ifNoneMatch equal to the current
// version Data is nil. Closed accounts have none.
func (s *Service) Get(ctx context.Context, accountID string, f Format, ifNoneMatch string) (*Stored, error) {
	if !uuidRe.MatchString(accountID) {
		return nil, ErrNotFound
	}
	col := map[Format]string{FormatVer3: "ver3", FormatRFL: "rfl", FormatCharInfo: "char_info"}[f]
	if col == "" {
		return nil, fmt.Errorf("%w: unknown format %d", ErrInvalid, int(f))
	}
	var st Stored
	err := s.pool.QueryRow(ctx, `SELECT CASE WHEN m.version = $2 THEN NULL ELSE m.`+col+` END, m.version, m.name, m.updated_at
		FROM account_miis m JOIN accounts a ON a.id = m.account_id
		WHERE m.account_id = $1 AND a.status IN ('active','banned')`, accountID, ifNoneMatch).
		Scan(&st.Data, &st.Version, &st.Name, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// Delete removes an account's Mii. Idempotent.
func (s *Service) Delete(ctx context.Context, accountID string) error {
	if !uuidRe.MatchString(accountID) {
		return ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM account_miis WHERE account_id = $1`, accountID)
	return err
}
