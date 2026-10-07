package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lilypad/internal/gem"
	"lilypad/internal/store"
)

// *Store implements gem.Store alongside store.Store: gem balances live in the
// same database, keyed by the game user_id.
var _ gem.Store = (*Store)(nil)

// Get returns the player's gem balance; an absent row is 0/0.
func (s *Store) Get(ctx context.Context, userID int64) (gem.Balance, error) {
	var b gem.Balance
	err := s.database(ctx).QueryRow(ctx,
		`SELECT free, paid FROM gems WHERE user_id=$1`, userID).Scan(&b.Free, &b.Paid)
	if errors.Is(err, pgx.ErrNoRows) {
		return gem.Balance{}, nil
	}
	if err != nil {
		return gem.Balance{}, fmt.Errorf("postgres: gem get: %w", err)
	}
	return b, nil
}

// Add credits free gems (upsert). Adding 0 is a no-op (returns current balance).
func (s *Store) Add(ctx context.Context, userID int64, free int64) (gem.Balance, error) {
	if free < 0 {
		return gem.Balance{}, fmt.Errorf("negative gem credit")
	}
	if free == 0 {
		return s.Get(ctx, userID)
	}
	var b gem.Balance
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
		INSERT INTO gems (user_id, free, paid) VALUES ($1, $2, 0)
		ON CONFLICT (user_id) DO UPDATE SET free = gems.free + EXCLUDED.free
		RETURNING free, paid`, userID, free).Scan(&b.Free, &b.Paid)
	})
	if err != nil {
		return gem.Balance{}, fmt.Errorf("postgres: gem add: %w", err)
	}
	return b, nil
}

// Set overwrites the player's balance outright (save import) and returns it.
func (s *Store) Set(ctx context.Context, userID int64, b gem.Balance) (gem.Balance, error) {
	if err := b.Validate(); err != nil {
		return gem.Balance{}, err
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
		INSERT INTO gems (user_id, free, paid) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET free=EXCLUDED.free, paid=EXCLUDED.paid`,
			userID, b.Free, b.Paid)
		if err != nil {
			return err
		}
		// Set is an explicit wallet replacement, unlike an in-game add/debit.
		// Standalone tools/importers must also displace a live client's old view.
		if _, err := tx.Exec(ctx, `UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint) WHERE user_id=$1`, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM request_results WHERE user_id=$1`, userID)
		return err
	})
	if err != nil {
		return gem.Balance{}, fmt.Errorf("postgres: gem set: %w", err)
	}
	return b, nil
}

// Consume spends n gems free-first under a row lock; ErrInsufficient (balance
// unchanged) when free+paid < n. n <= 0 is a no-op.
func (s *Store) Consume(ctx context.Context, userID int64, n int64) (gem.Balance, error) {
	if n <= 0 {
		return s.Get(ctx, userID)
	}
	var out gem.Balance
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		nb, err := consumeGemsTx(ctx, tx, userID, n)
		if err != nil {
			return err
		}
		out = nb
		return nil
	})
	if err != nil {
		if errors.Is(err, gem.ErrInsufficient) {
			return gem.Balance{}, gem.ErrInsufficient
		}
		return gem.Balance{}, fmt.Errorf("postgres: gem consume: %w", err)
	}
	return out, nil
}

// applyGemChangeTx applies a gem add/consume on tx. Consume uses a row lock
// and returns gem.ErrInsufficient without writing when the balance is too low.
func applyGemChangeTx(ctx context.Context, tx pgx.Tx, userID int64, ch store.GemChange) error {
	if ch.Add < 0 || ch.Consume < 0 {
		return fmt.Errorf("negative gem credit or debit")
	}
	if ch.Consume > 0 {
		if _, err := consumeGemsTx(ctx, tx, userID, ch.Consume); err != nil {
			return err
		}
	}
	if ch.Add == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gems (user_id, free, paid) VALUES ($1, $2, 0)
		ON CONFLICT (user_id) DO UPDATE SET free = gems.free + EXCLUDED.free`,
		userID, ch.Add); err != nil {
		return fmt.Errorf("postgres: gem add: %w", err)
	}
	return nil
}

func consumeGemsTx(ctx context.Context, tx pgx.Tx, userID, n int64) (gem.Balance, error) {
	var b gem.Balance
	err := tx.QueryRow(ctx,
		`SELECT free, paid FROM gems WHERE user_id=$1 FOR UPDATE`, userID).Scan(&b.Free, &b.Paid)
	if errors.Is(err, pgx.ErrNoRows) {
		return gem.Balance{}, gem.ErrInsufficient
	}
	if err != nil {
		return gem.Balance{}, err
	}
	nb, err := gem.Apply(b, n)
	if err != nil {
		return gem.Balance{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE gems SET free=$2, paid=$3 WHERE user_id=$1`, userID, nb.Free, nb.Paid); err != nil {
		return gem.Balance{}, err
	}
	return nb, nil
}
