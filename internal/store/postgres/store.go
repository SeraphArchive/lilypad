// Package postgres implements store.Store on PostgreSQL + JSONB. Mutating
// operations serialize per player via a transaction-scoped advisory lock.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lilypad/internal/account"
	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
)

// Row is a user-table row.
type Row = deltacomm.Row

var _ store.Store = (*Store)(nil)

// Store is the Postgres-backed persistence layer.
type Store struct {
	pool *pgxpool.Pool
	seed map[string][]Row // optional new-player initial state
}

// New connects, runs migrations, and returns a Store. seed (may be nil) is the
// initial table state installed for brand-new accounts.
func New(ctx context.Context, dsn string, seed map[string][]Row) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, seed: seed}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// UserExists reports whether an account with the given game userID exists.
func (s *Store) UserExists(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	if err := s.database(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE user_id=$1)`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("postgres: user exists: %w", err)
	}
	return exists, nil
}

// GetOrCreate resolves an XUID to an account, creating + seeding on first contact.
func (s *Store) GetOrCreate(ctx context.Context, xuid string) (account.Account, error) {
	var acc account.Account
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var userID int64
		var createdAt time.Time
		err := tx.QueryRow(ctx, `
			INSERT INTO accounts (x_uid, user_id)
			VALUES ($1, nextval('account_user_id_seq'))
			ON CONFLICT (x_uid) DO NOTHING
			RETURNING user_id, created_at`, xuid).Scan(&userID, &createdAt)
		switch {
		case err == nil:
			// freshly created -> seed
			if len(s.seed) > 0 {
				if err := seedTx(ctx, tx, userID, accountSeed(s.seed, createdAt.Unix())); err != nil {
					return err
				}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// already existed
			if err := tx.QueryRow(ctx,
				`UPDATE accounts SET last_seen_at=now() WHERE x_uid=$1 RETURNING user_id`,
				xuid).Scan(&userID); err != nil {
				return err
			}
		default:
			return err
		}
		acc = account.Account{XUID: xuid, UserID: userID}
		return nil
	})
	if err != nil {
		return account.Account{}, fmt.Errorf("postgres: GetOrCreate: %w", err)
	}
	return acc, nil
}

// Fill the neutral template's registration timestamp only when creating an
// account. Copy the changed row so concurrent creations cannot share its time.
func accountSeed(seed map[string][]Row, registeredAt int64) map[string][]Row {
	profiles := seed["user_profile"]
	if len(profiles) != 1 || fmt.Sprint(profiles[0]["_registeredAt"]) != "0" {
		return seed
	}
	out := maps.Clone(seed)
	profile := maps.Clone(profiles[0])
	profile["_registeredAt"] = registeredAt
	out["user_profile"] = []Row{profile}
	return out
}

func (s *Store) AllHashes(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := s.database(ctx).Query(ctx, `SELECT table_name, hash FROM user_state_tokens WHERE user_id=$1`, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: AllHashes: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, hash string
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, err
		}
		out[name] = hash
	}
	return out, rows.Err()
}

func (s *Store) GetTables(ctx context.Context, userID int64, names []string) (map[string][]Row, error) {
	out := make(map[string][]Row, len(names))
	for _, n := range names {
		out[n] = []Row{} // default empty for tables with no state
	}
	rows, err := s.database(ctx).Query(ctx,
		`SELECT table_name, rows FROM user_state WHERE user_id=$1 AND table_name = ANY($2)`,
		userID, names)
	if err != nil {
		return nil, fmt.Errorf("postgres: GetTables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		parsed, err := unmarshalRows(raw)
		if err != nil {
			return nil, fmt.Errorf("postgres: parse %s: %w", name, err)
		}
		out[name] = parsed
	}
	return out, rows.Err()
}

func (s *Store) HasState(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := s.database(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_state WHERE user_id=$1)`, userID).Scan(&exists)
	return exists, err
}

func (s *Store) SeedNewPlayer(ctx context.Context, userID int64, seed map[string][]Row) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		return seedTx(ctx, tx, userID, seed)
	})
}

// ImportTables replaces the player's save with a full-state snapshot under
// the per-player advisory lock. The snapshot is the whole save, so tables
// absent from it are deleted rather than kept: merging would splice a
// partial archive (an exporter that dropped a table, or a hand-trimmed
// file) into the live save and leave a state nobody played. Each payload
// table is then upserted with a freshly computed hash.
func (s *Store) ImportTables(ctx context.Context, userID int64, tables map[string][]Row) error {
	for name, rows := range tables {
		if err := deltacomm.ValidateArchiveRows(name, rows); err != nil {
			return err
		}
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_state WHERE user_id=$1`, userID); err != nil {
			return err
		}
		for table, rows := range tables {
			hash := ""
			if !deltacomm.HashExcluded(table) {
				var err error
				hash, err = deltacomm.HashTable(rows)
				if err != nil {
					return err
				}
			}
			raw, err := json.Marshal(rows)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_state (user_id, table_name, rows, hash, updated_at)
				VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (user_id, table_name)
				DO UPDATE SET rows=EXCLUDED.rows, hash=EXCLUDED.hash, updated_at=now()`,
				userID, table, raw, hash); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint) WHERE user_id=$1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM request_results WHERE user_id=$1`, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres: ImportTables: %w", err)
	}
	return nil
}

func (s *Store) ApplyClientDeltas(ctx context.Context, userID int64, d deltacomm.Deltas, baselines map[string]string, savedTime int64) (map[string]string, error) {
	if len(d.ReplaceItems) != 0 {
		return nil, fmt.Errorf("client whole-table replacement is unsupported")
	}
	var result map[string]string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		var barrier int64
		if err := tx.QueryRow(ctx, `SELECT write_barrier FROM accounts WHERE user_id=$1`, userID).Scan(&barrier); err != nil {
			return err
		}
		if savedTime <= 0 || savedTime <= barrier || savedTime > time.Now().Unix() {
			return fmt.Errorf("%w: packet savedTime is outside the current save epoch", store.ErrConcurrency)
		}
		if baselines == nil {
			baselines = map[string]string{}
		}
		var err error
		result, err = s.ApplyDeltas(context.WithValue(ctx, transactionKey{}, tx), userID, d, baselines)
		return err
	})
	return result, err
}

// ApplyDeltas applies a mutation set atomically under the per-player advisory
// lock and returns the new hash of every changed table.
func (s *Store) ApplyDeltas(ctx context.Context, userID int64, d deltacomm.Deltas, baselines map[string]string) (map[string]string, error) {
	result := map[string]string{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		current, stored, err := loadTables(ctx, tx, userID, d.TouchedTables())
		if err != nil {
			return err
		}
		if baselines != nil {
			for _, t := range d.TouchedTables() {
				if deltacomm.HashExcluded(t) {
					continue
				}
				base := baselines[t]
				if base != stored[t] {
					return fmt.Errorf("%w: table %s baseline does not match saved version", store.ErrConcurrency, t)
				}
			}
		}
		result, err = applyTouched(ctx, tx, userID, current, stored, d)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: ApplyDeltas: %w", err)
	}
	return result, nil
}

// MutateUnderLock locks the player, loads readTables, lets fn compute a delta set
// from the current rows, applies it atomically, and returns the changed hashes.
func (s *Store) MutateUnderLock(ctx context.Context, userID int64, readTables []string, fn func(current map[string][]Row) (deltacomm.Deltas, error)) (map[string]string, error) {
	return s.MutateWithGems(ctx, userID, readTables, func(cur map[string][]Row) (deltacomm.Deltas, store.GemChange, error) {
		d, err := fn(cur)
		return d, store.GemChange{}, err
	})
}

// MutateWithGems is MutateUnderLock plus a gem add/consume in the same transaction.
func (s *Store) MutateWithGems(ctx context.Context, userID int64, readTables []string, fn func(current map[string][]Row) (deltacomm.Deltas, store.GemChange, error)) (map[string]string, error) {
	result := map[string]string{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, userID); err != nil {
			return err
		}
		current, stored, err := loadTables(ctx, tx, userID, readTables)
		if err != nil {
			return err
		}
		d, gems, err := fn(cloneTableMap(current))
		if err != nil {
			return err
		}
		if err := applyGemChangeTx(ctx, tx, userID, gems); err != nil {
			return err
		}
		// Load any table the delta touches that wasn't preloaded.
		for _, t := range d.TouchedTables() {
			if _, ok := current[t]; !ok {
				extraCur, extraStored, err := loadTables(ctx, tx, userID, []string{t})
				if err != nil {
					return err
				}
				current[t] = extraCur[t]
				stored[t] = extraStored[t]
			}
		}
		result, err = applyTouched(ctx, tx, userID, current, stored, d)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: MutateWithGems: %w", err)
	}
	return result, nil
}

func lockPlayer(ctx context.Context, tx pgx.Tx, userID int64) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userID); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	return nil
}

// loadTables reads rows + stored hashes for the named tables (empty/"" if absent).
func loadTables(ctx context.Context, tx pgx.Tx, userID int64, names []string) (map[string][]Row, map[string]string, error) {
	current := map[string][]Row{}
	stored := map[string]string{}
	for _, t := range names {
		var raw []byte
		var hash string
		err := tx.QueryRow(ctx,
			`SELECT COALESCE(s.rows,'[]'::jsonb),t.hash FROM user_state_tokens t LEFT JOIN user_state s USING(user_id,table_name) WHERE t.user_id=$1 AND t.table_name=$2`,
			userID, t).Scan(&raw, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			current[t] = []Row{}
			stored[t] = ""
			continue
		} else if err != nil {
			return nil, nil, err
		}
		parsed, err := unmarshalRows(raw)
		if err != nil {
			return nil, nil, err
		}
		current[t] = parsed
		stored[t] = hash
	}
	return current, stored, nil
}

// applyTouched merges each touched table, persists changed ones, and returns
// their new hashes. Hash-excluded (write-only) tables are persisted with an
// empty hash and omitted from the result (matching official confirm/push).
// Unchanged tables are omitted from the result.
func applyTouched(ctx context.Context, tx pgx.Tx, userID int64, current map[string][]Row, stored map[string]string, d deltacomm.Deltas) (map[string]string, error) {
	result := map[string]string{}
	for _, t := range d.TouchedTables() {
		merged, err := deltacomm.MergeTable(
			t, current[t], d.PutItems[t], d.DeleteItems[t], d.ReplaceItems[t],
			hasKey(d.ReplaceItems, t))
		if err != nil {
			return nil, err
		}
		excluded := deltacomm.HashExcluded(t)
		hash := ""
		if !excluded {
			hash, err = deltacomm.HashTable(merged)
			if err != nil {
				return nil, err
			}
			previous, err := deltacomm.HashTable(current[t])
			if err != nil {
				return nil, err
			}
			if hash == previous {
				continue
			}
		}
		raw, err := json.Marshal(merged)
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO user_state (user_id, table_name, rows, hash, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (user_id, table_name)
			DO UPDATE SET rows=EXCLUDED.rows, hash=EXCLUDED.hash, updated_at=now() RETURNING hash`,
			userID, t, raw, hash).Scan(&hash); err != nil {
			return nil, err
		}
		if !excluded {
			result[t] = hash
		}
	}
	return result, nil
}

// cloneTableMap deep-copies the table map (and each row) so a MutateUnderLock
// callback can freely mutate rows without aliasing the merge base in applyTouched.
func cloneTableMap(in map[string][]Row) map[string][]Row {
	out := make(map[string][]Row, len(in))
	for k, rows := range in {
		out[k] = deltacomm.CloneRows(rows)
	}
	return out
}

// ErrConcurrency is re-exported for callers that import only this package.
var ErrConcurrency = store.ErrConcurrency

func seedTx(ctx context.Context, tx pgx.Tx, userID int64, seed map[string][]Row) error {
	for table, rows := range seed {
		if deltacomm.HashExcluded(table) {
			continue // write-only table: not part of the save-state
		}
		hash, err := deltacomm.HashTable(rows)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_state (user_id, table_name, rows, hash, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (user_id, table_name) DO NOTHING`,
			userID, table, raw, hash); err != nil {
			return err
		}
	}
	return nil
}

func hasKey(m map[string][]Row, k string) bool {
	_, ok := m[k]
	return ok
}

func unmarshalRows(raw []byte) ([]Row, error) {
	if len(raw) == 0 {
		return []Row{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out []Row
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Row{}
	}
	return out, nil
}
