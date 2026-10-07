package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"

	"lilypad/internal/store"
)

var _ store.SessionStore = (*Store)(nil)

// SessionBase returns the account's current session base (0 = none set).
func (s *Store) SessionBase(ctx context.Context, userID int64) (int64, error) {
	var base int64
	if err := s.database(ctx).QueryRow(ctx,
		`SELECT session_base FROM accounts WHERE user_id=$1`, userID).Scan(&base); err != nil {
		return 0, fmt.Errorf("postgres: session base: %w", err)
	}
	return base, nil
}

// SetSessionBase records a session's base message id, but never lowers one
// already stored. A stale device replaying client/id (or migration/id) with its
// own, smaller x-msgid must not drag the base back down: that would re-admit
// the session a newer login had just invalidated, and both devices would write
// the same account. GREATEST makes the base monotonic, matching
// InvalidateSessions.
func (s *Store) SetSessionBase(ctx context.Context, userID int64, base int64) error {
	// Administrative compatibility only: a counter cannot authorize a client.
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE accounts SET session_base=GREATEST(session_base,$2) WHERE user_id=$1`, userID, base)
		return err
	})
}

// InvalidateSessions revokes the login independently of its request counter.
func (s *Store) InvalidateSessions(ctx context.Context, userID int64) error {
	base := time.Now().Unix()
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE accounts SET session_base=GREATEST(session_base,$2),session_generation=session_generation+1,session_revoked=TRUE WHERE user_id=$1`, userID, base)
		return err
	})
}
