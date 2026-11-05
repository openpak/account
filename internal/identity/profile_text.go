package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // the zone check must not depend on the image carrying zoneinfo
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidProfile = errors.New("identity: invalid profile")

// PublicProfilePatch changes only supplied fields. A supplied empty bio or
// motto clears it; an omitted field preserves the stored value.
type PublicProfilePatch struct {
	DisplayName *string
	Country     *string
	Bio         *string
	Motto       *string
	// Language: empty clears. Timezone: empty clears, otherwise an IANA name.
	Language *string
	Timezone *string
}

// EventProfileUpdated is the durable invalidation event announced after any change to the
// public profile (display name, country, bio, motto, language, timezone). It carries only the
// account id: adapters re-read the account (GetAccount) to refresh their caches.
const EventProfileUpdated = "profile_updated"

// NormalizeLanguage validates a BCP 47 style short tag ("en", "pt-BR", "zh-Hant-TW") and returns
// the canonical casing. Empty is valid (unset).
func NormalizeLanguage(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > 20 {
		return "", errors.New("identity: language is too long")
	}
	parts := strings.Split(v, "-")
	for i, part := range parts {
		if part == "" || len(part) > 8 {
			return "", errors.New("identity: language must be a short tag like en or pt-BR")
		}
		for _, r := range part {
			alpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !alpha && !(i > 0 && r >= '0' && r <= '9') {
				return "", errors.New("identity: language must be a short tag like en or pt-BR")
			}
		}
		switch {
		case i == 0:
			if len(part) < 2 || len(part) > 3 {
				return "", errors.New("identity: language must start with a 2 or 3 letter code")
			}
			parts[i] = strings.ToLower(part)
		case len(part) == 2:
			parts[i] = strings.ToUpper(part)
		case len(part) == 4:
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		default:
			parts[i] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "-"), nil
}

// ValidateTimezone accepts an IANA zone name ("Europe/Dublin", "UTC"). Empty is valid (unset).
func ValidateTimezone(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > 64 || v == "Local" {
		return "", errors.New("identity: timezone must be an IANA name")
	}
	for _, r := range v {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/_+-", r)
		if !ok {
			return "", errors.New("identity: timezone must be an IANA name")
		}
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", errors.New("identity: unknown timezone")
	}
	return v, nil
}

func validateProfileText(bio, motto *string) error {
	for _, field := range []struct {
		name  string
		value *string
		limit int
	}{{"bio", bio, 499}, {"motto", motto, 21}} {
		if field.value == nil {
			continue
		}
		value := *field.value
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("identity: %s must contain valid text", field.name)
		}
		if len(utf16.Encode([]rune(value))) > field.limit {
			return fmt.Errorf("identity: %s is too long", field.name)
		}
	}
	if motto != nil && strings.ContainsAny(*motto, "\r\n\t") {
		return errors.New("identity: motto must be a single line")
	}
	return nil
}

func (s *Service) UpdatePublicProfile(ctx context.Context, accountID string, patch PublicProfilePatch) (*Account, error) {
	p := Profile{Bio: patch.Bio, Motto: patch.Motto}
	if patch.DisplayName != nil {
		p.DisplayName = *patch.DisplayName
		if strings.TrimSpace(p.DisplayName) == "" {
			return nil, fmt.Errorf("%w: display name required", ErrInvalidProfile)
		}
	}
	if patch.Country != nil {
		p.Country = *patch.Country
	}
	if patch.Language != nil {
		p.Language = *patch.Language
	}
	if patch.Timezone != nil {
		p.Timezone = *patch.Timezone
	}
	if err := validateProfile(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if patch.Language != nil {
		patch.Language = &p.Language
	}
	if patch.Timezone != nil {
		patch.Timezone = &p.Timezone
	}
	if patch.DisplayName != nil {
		patch.DisplayName = &p.DisplayName
	}
	if patch.Country != nil {
		patch.Country = &p.Country
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE accounts SET
			display_name = COALESCE($2, display_name), country = COALESCE($3, country),
			bio = COALESCE($4, bio), motto = COALESCE($5, motto),
			language = COALESCE($6, language), timezone = COALESCE($7, timezone),
			profile_version = profile_version + 1, updated_at = now()
			WHERE id = $1 AND status = 'active'`, accountID, patch.DisplayName, patch.Country, patch.Bio, patch.Motto, patch.Language, patch.Timezone)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, EventProfileUpdated, accountID, "", "")
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: update public profile: %w", err)
	}
	return s.GetByID(ctx, accountID)
}
