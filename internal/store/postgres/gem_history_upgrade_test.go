package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lilypad/internal/gem"
	"lilypad/internal/store"
)

func TestQuartzJournalUpgradePreservesExistingWalletAndRecovery(t *testing.T) {
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsnOrSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := fmt.Sprintf("quartz_upgrade_%x", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() > "0009_registration_time.sql" {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, strings.TrimSuffix(entry.Name(), ".sql")); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{pool: pool}
	acc, err := s.GetOrCreate(ctx, "synthetic-quartz-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	uid := acc.UserID
	if err := s.ImportSnapshot(ctx, uid, map[string][]Row{"user_story": {{"_masterStoryId": 1, "_isClear": true}}}, &gem.Balance{Free: 500, Paid: 20}); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSession(ctx, uid, "synthetic-legacy-session", 1); err != nil {
		t.Fatal(err)
	}
	before, err := s.ExportSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	var recoveryBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1`, uid).Scan(&recoveryBefore); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	after, err := s.ExportSnapshot(ctx, uid)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("journal upgrade changed existing data", after, err)
	}
	var recoveryAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1`, uid).Scan(&recoveryAfter); err != nil || recoveryAfter != recoveryBefore {
		t.Fatal("journal upgrade changed recovery history", recoveryBefore, recoveryAfter, err)
	}
	journal, more, err := s.History(ctx, uid, 0, 20)
	if err != nil || more || journal == nil || len(journal) != 0 {
		t.Fatal("upgrade backfilled fictional transactions", journal, more, err)
	}
	if _, err := s.CheckSession(ctx, uid, "synthetic-legacy-session", 2); !errors.Is(err, store.ErrSession) {
		t.Fatal("upgrade did not fence old session", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, uid, 20); err != nil {
		t.Fatal(err)
	}
	journal, more, err = s.History(ctx, uid, 0, 20)
	if err != nil || more || len(journal) != 1 || journal[0].Free != 20 {
		t.Fatal("upgraded journal not usable", journal, more, err)
	}
}
