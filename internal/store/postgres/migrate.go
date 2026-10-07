package postgres

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies any embedded migrations not yet recorded, in filename order,
// inside one transaction protected by a transaction-scoped lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// The lock and every migration share one transaction/connection. A session
	// lock on pool.Exec can leak when another connection runs the unlock.
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return migrateTx(ctx, tx) })
}

func migrateTx(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lilypad.internal_write','on',true)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(76078734603102001)`); err != nil {
		return fmt.Errorf("postgres: migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("postgres: ensure schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	applied := false
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("postgres: check migration %s: %w", version, err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("postgres: read %s: %w", name, err)
		}
		if err := func() error {
			if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
				return fmt.Errorf("exec %s: %w", name, err)
			}
			if version == "0007_integrity" {
				if err := repairHashes(ctx, tx); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version)
			return err
		}(); err != nil {
			return fmt.Errorf("postgres: apply %s: %w", name, err)
		}
		applied = true
	}
	if applied {
		// Every future schema update fences queued pre-update writes as well.
		_, err := tx.Exec(ctx, `UPDATE accounts SET session_revoked=TRUE,session_generation=session_generation+1,retired_sessions=retired_sessions || session_history || CASE WHEN session_token<>'' THEN jsonb_build_object(session_token,0) ELSE '{}'::jsonb END,write_barrier=GREATEST(write_barrier,floor(extract(epoch FROM clock_timestamp()))::bigint)`)
		return err
	}
	return nil
}
