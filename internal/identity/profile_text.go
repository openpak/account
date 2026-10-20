package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var ErrInvalidProfile = errors.New("identity: invalid profile")

// PublicProfilePatch changes only supplied fields. A supplied empty bio or
// motto clears it; an omitted field preserves the stored value.
type PublicProfilePatch struct {
	DisplayName *string
	Country     *string
	Bio         *string
	Motto       *string
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
	if err := validateProfile(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if patch.DisplayName != nil {
		patch.DisplayName = &p.DisplayName
	}
	if patch.Country != nil {
		patch.Country = &p.Country
	}
	tag, err := s.pool.Exec(ctx, `UPDATE accounts SET
		display_name = COALESCE($2, display_name), country = COALESCE($3, country),
		bio = COALESCE($4, bio), motto = COALESCE($5, motto), profile_version = profile_version + 1, updated_at = now()
		WHERE id = $1 AND status = 'active'`, accountID, patch.DisplayName, patch.Country, patch.Bio, patch.Motto)
	if err != nil {
		return nil, fmt.Errorf("identity: update public profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetByID(ctx, accountID)
}
