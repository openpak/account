package identity

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrHandleInvalid   = errors.New("handle_invalid")
	ErrHandleReserved  = errors.New("handle_reserved")
	ErrHandleProfane   = errors.New("handle_profane")
	ErrHandleTaken     = errors.New("handle_taken")
	ErrHandleImmutable = errors.New("handle_immutable")
)

var handlePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{5,14}$`)

// Reserved names protect staff roles and platform identities. This is the
// authoritative list; adapters must use the core's chosen handle verbatim.
var reservedHandles = strings.Fields(`admin administrator moderator support system openpak staff guest player null undefined root xbox xboxlive xboxlivesupport microsoft nintendo sony playstation psn wiiu switch`)

// ValidateHandle validates a public identifier, never a display name. Do not
// trim or rewrite it: the value accepted is the value every network receives.
func ValidateHandle(handle string) error {
	if !handlePattern.MatchString(handle) {
		return ErrHandleInvalid
	}
	lower := strings.ToLower(handle)
	for _, word := range reservedHandles {
		if lower == word {
			return ErrHandleReserved
		}
	}
	if strings.HasPrefix(lower, "openpak") || strings.HasPrefix(lower, "xboxlive") {
		return ErrHandleReserved
	}
	if profaneHandle(handle) {
		return ErrHandleProfane
	}
	return nil
}

type HandleCheck struct {
	Valid     bool   `json:"valid"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// CheckHandle is advisory. The database claim performed at registration is
// atomic and authoritative, including when two callers race for the same name.
func (s *Service) CheckHandle(ctx context.Context, handle string) (HandleCheck, error) {
	if err := ValidateHandle(handle); err != nil {
		return HandleCheck{Reason: err.Error()}, nil
	}
	var taken bool
	err := s.pool.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM account_handle_claims WHERE handle_lower=lower($1) AND (release_at IS NULL OR release_at > now()))
 OR EXISTS(SELECT 1 FROM adapter_links l JOIN accounts a ON a.id=l.account_id
 WHERE a.status <> 'deleted' AND l.state IN ('active','pending','revoking')
 AND l.namespace IN ('wiiu','xbox','x360','np') AND l.public_code_key=lower($1))`, handle).Scan(&taken)
	if err != nil {
		return HandleCheck{}, err
	}
	result := HandleCheck{Valid: true, Available: !taken}
	if taken {
		result.Reason = ErrHandleTaken.Error()
	}
	return result, nil
}

// ClaimHandle sets an existing pre-handle account's handle once. Identical retries
// succeed; even capitalization cannot change after the first claim.
func (s *Service) ClaimHandle(ctx context.Context, accountID, handle string) error {
	if err := ValidateHandle(handle); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current string
		err := tx.QueryRow(ctx, `SELECT COALESCE(handle,'') FROM accounts WHERE id=$1 AND status='active' FOR UPDATE`, accountID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current == handle {
			return nil
		}
		if current != "" {
			return ErrHandleImmutable
		}
		if _, err = tx.Exec(ctx, `UPDATE accounts SET handle=$2, updated_at=now() WHERE id=$1`, accountID, handle); err != nil {
			return mapHandleError(err)
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "handle_assigned", accountID, "", "")
		}
		return nil
	})
}

// ResolveHandle resolves active accounts only. A retired or held handle never
// resolves to its previous owner, and availability never exposes that owner.
func (s *Service) ResolveHandle(ctx context.Context, handle string) (string, error) {
	if !handlePattern.MatchString(handle) {
		return "", ErrHandleInvalid
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE lower(handle)=lower($1) AND status='active'`, handle).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func mapHandleError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.ConstraintName {
		case "account_handle_taken", "accounts_handle_unique":
			return ErrHandleTaken
		case "account_handle_immutable":
			return ErrHandleImmutable
		case "accounts_handle_format":
			return ErrHandleInvalid
		}
	}
	return err
}

// RegisterWithHandle is the public registration path. Older registration
// helpers remain for migration fixtures; no transport may create a new account
// without a handle.
func (s *Service) RegisterWithHandle(ctx context.Context, email, password, displayName, handle string, p Profile) (*Account, string, error) {
	if err := ValidateHandle(handle); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = handle
	}
	p.Handle = handle
	return s.RegisterWithProfile(ctx, email, password, displayName, p)
}
