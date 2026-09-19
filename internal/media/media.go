// Package media stores account avatars (website contract request CR-2): the
// website uploads the processed PNGs, adapters fetch them by account id. The
// core validates what it is given but never resizes or re-encodes it.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Size limits per image: the raw RGBA size of each picture (256*256*4 and
// 512*512*4 bytes). A compressed PNG from the website's pipeline is far
// below that; anything bigger is not an avatar.
const (
	MaxSmall = 256 << 10 // 256 KiB for the 256x256 PNG
	MaxLarge = 1 << 20   // 1 MiB for the 512x512 PNG
)

var (
	ErrInvalid  = errors.New("media: avatar must be a 256x256 PNG (<=256 KiB) and a 512x512 PNG (<=1 MiB)")
	ErrNotFound = errors.New("media: no avatar")
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type Avatar struct {
	PNG       []byte
	Version   string
	UpdatedAt time.Time
}

func checkPNG(b []byte, side, max int) error {
	if len(b) == 0 || len(b) > max {
		return ErrInvalid
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != side || cfg.Height != side {
		return ErrInvalid
	}
	return nil
}

// Version is the content hash both sizes share: it changes whenever either does.
func Version(small, large []byte) string {
	h := sha256.New()
	h.Write(small)
	h.Write(large)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Put replaces an account's avatar with both sizes at once.
func (s *Service) Put(ctx context.Context, accountID string, small, large []byte) (string, error) {
	if !uuidRe.MatchString(accountID) {
		return "", ErrNotFound
	}
	if err := checkPNG(small, 256, MaxSmall); err != nil {
		return "", err
	}
	if err := checkPNG(large, 512, MaxLarge); err != nil {
		return "", err
	}
	v := Version(small, large)
	tag, err := s.pool.Exec(ctx, `INSERT INTO account_avatars (account_id, png_256, png_512, version)
		SELECT id, $2, $3, $4 FROM accounts WHERE id = $1 AND status IN ('active','banned')
		ON CONFLICT (account_id) DO UPDATE SET png_256 = EXCLUDED.png_256, png_512 = EXCLUDED.png_512,
			version = EXCLUDED.version, updated_at = now()`, accountID, small, large, v)
	if err != nil {
		return "", fmt.Errorf("media: put: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return v, nil
}

// Get returns one size (256 or 512) of an account's avatar. With ifNoneMatch
// equal to the current version, PNG is nil: the caller's copy is current.
// Accounts that are being deleted or are deleted have none.
func (s *Service) Get(ctx context.Context, accountID string, size int, ifNoneMatch string) (*Avatar, error) {
	if !uuidRe.MatchString(accountID) {
		return nil, ErrNotFound
	}
	col := "png_512"
	switch size {
	case 0, 512:
	case 256:
		col = "png_256"
	default:
		return nil, ErrInvalid
	}
	var a Avatar
	err := s.pool.QueryRow(ctx, `SELECT CASE WHEN m.version = $2 THEN NULL ELSE m.`+col+` END, m.version, m.updated_at
		FROM account_avatars m JOIN accounts a ON a.id = m.account_id
		WHERE m.account_id = $1 AND a.status IN ('active','banned')`, accountID, ifNoneMatch).
		Scan(&a.PNG, &a.Version, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Delete removes an account's avatar. Idempotent.
func (s *Service) Delete(ctx context.Context, accountID string) error {
	if !uuidRe.MatchString(accountID) {
		return ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM account_avatars WHERE account_id = $1`, accountID)
	return err
}
