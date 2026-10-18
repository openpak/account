package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrDeletionNotPending = errors.New("deletion must be requested first")

// FinalizeDeletion is the home operator's irreversible second stage. Requesting
// deletion disables access; this stage destroys core credentials and starts the
// database-enforced 90-day handle hold. Adapters receive account_deleted and
// retire their own records. No background timer guesses when to finalize.
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
		for _, table := range []string{"sessions", "lifecycle_tokens", "adapter_credentials", "account_two_factor", "two_factor_recovery_codes", "login_challenges", "account_avatars"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE account_id=$1`, accountID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status='deleted', email=id::text||'@deleted.invalid',
   email_normalized=id::text||'@deleted.invalid', password_hash='', display_name='', country='',birthday=NULL,
   email_verified_at=NULL,updated_at=now() WHERE id=$1`, accountID); err != nil {
			return err
		}
		if s.emitter != nil {
			return s.emitter.EmitTx(ctx, tx, "account_deleted", accountID, "", "")
		}
		return nil
	})
}
