package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrDeletionNotPending = errors.New("deletion must be requested first")
	// ErrGraceOver is a restore asked for after the erasure date: the
	// finaliser may already be erasing the account elsewhere.
	ErrGraceOver = errors.New("identity: the grace period is over; erasure has started")
)

// DefaultDeletionGrace is how long a closed account stays restorable.
const DefaultDeletionGrace = 30 * 24 * time.Hour

// PendingDeletionError answers a correct password for a closed account that
// can still be restored. It is ErrAccountDeleted to every caller that only
// asks whether the account may sign in.
type PendingDeletionError struct {
	AccountID  string
	EraseAfter time.Time
}

func (e *PendingDeletionError) Error() string {
	return fmt.Sprintf("identity: account closed, erased after %s unless restored", e.EraseAfter.UTC().Format(time.RFC3339))
}

func (e *PendingDeletionError) Is(target error) bool { return target == ErrAccountDeleted }

// SetDeletionGrace sets the grace period for deletions requested from now on.
func (s *Service) SetDeletionGrace(d time.Duration) {
	if d > 0 {
		s.deletionGrace = d
	}
}

// DueDeletions lists closed accounts whose grace period is over, oldest first.
func (s *Service) DueDeletions(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM accounts
		WHERE status='deletion_pending' AND erase_after <= now() ORDER BY erase_after LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// EraseAfter reports when a closed account will be erased (zero for any
// account that is not closed).
func (s *Service) EraseAfter(ctx context.Context, accountID string) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT erase_after FROM accounts WHERE id=$1 AND status='deletion_pending'`, accountID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) || t == nil {
		return time.Time{}, nil
	}
	return *t, err
}

// RestoreDeletion reopens a closed account within its grace period, with the
// status it had before (a banned account stays banned). Sessions revoked at
// close stay revoked: the owner signs in again. ErrDeletionNotPending for an
// account that is not closed, ErrGraceOver once its erasure date has passed.
func (s *Service) RestoreDeletion(ctx context.Context, accountID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var state Status
		var eraseAfter *time.Time
		err := tx.QueryRow(ctx, `SELECT status, erase_after FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&state, &eraseAfter)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state != StatusDeletionPending {
			return ErrDeletionNotPending
		}
		if eraseAfter != nil && !eraseAfter.After(time.Now()) {
			return ErrGraceOver
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status=COALESCE(status_before_deletion,'active'),
			status_before_deletion=NULL, closed_at=NULL, erase_after=NULL, updated_at=now() WHERE id=$1`, accountID); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "account_restored", accountID, "", "")
		}
		return nil
	})
}

// RestoreByPassword is the owner's restore at sign-in: the password must be
// right, exactly as for a sign-in. An account that is not closed is left as
// it is (nil), so a repeated click just signs in.
func (s *Service) RestoreByPassword(ctx context.Context, email, password string) error {
	_, err := s.VerifyCredentials(ctx, email, password)
	var pe *PendingDeletionError
	switch {
	case errors.As(err, &pe):
		return s.RestoreDeletion(ctx, pe.AccountID)
	case err != nil:
		return err
	}
	return nil
}

// FinalizeDeletion is the irreversible second stage, run by the erasure
// finaliser once the grace period is over (or by the home operator). It
// destroys core credentials, erases everything the core holds about the
// account, and starts the database-enforced 90-day handle hold. Swept: sessions
// and credentials, two-factor material, avatar, console links and adapter
// routing, presence and play history, friendships both ways, blocks both ways,
// game invitations both ways. Moderation actions and reports stay: they are
// the audit trail, and after this they name an anonymized ID only (display
// name, email and profile are wiped with the accounts row). Adapters receive
// account_deleted and retire their own records.
func (s *Service) FinalizeDeletion(ctx context.Context, accountID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var state Status
		err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == StatusDeleted {
			return nil
		}
		if state != StatusDeletionPending {
			return ErrDeletionNotPending
		}
		// adapter_sessions before play_sessions: deleting a live session writes
		// its play-history row (migration 0019), which must go too.
		for _, q := range []string{
			`DELETE FROM sessions WHERE account_id=$1`,
			`DELETE FROM lifecycle_tokens WHERE account_id=$1`,
			`DELETE FROM adapter_credentials WHERE account_id=$1`,
			`DELETE FROM adapter_sessions WHERE account_id=$1`,
			`DELETE FROM play_sessions WHERE account_id=$1`,
			`DELETE FROM adapter_links WHERE account_id=$1`,
			`DELETE FROM account_two_factor WHERE account_id=$1`,
			`DELETE FROM two_factor_recovery_codes WHERE account_id=$1`,
			`DELETE FROM login_challenges WHERE account_id=$1`,
			`DELETE FROM account_avatars WHERE account_id=$1`,
			`DELETE FROM friendships WHERE requester_id=$1 OR addressee_id=$1`,
			`DELETE FROM account_blocks WHERE blocker_id=$1 OR blocked_id=$1`,
			`DELETE FROM invitations WHERE from_id=$1 OR to_id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, accountID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status='deleted', email=id::text||'@deleted.invalid',
   email_normalized=id::text||'@deleted.invalid', password_hash='', display_name='', country='',birthday=NULL, bio='',motto='',
   email_verified_at=NULL,status_before_deletion=NULL,updated_at=now() WHERE id=$1`, accountID); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "account_deleted", accountID, "", "")
		}
		return nil
	})
}
