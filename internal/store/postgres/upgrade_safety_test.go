package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
	"testing"
	"time"
)

func TestLegacyDatabaseUpgradePreservesDataAndFencesPreUpgradeQueue(t *testing.T) {
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
	schema := fmt.Sprintf("save_upgrade_%x", time.Now().UnixNano())
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
	// Build the actual pre-change schema, rather than downgrading an already
	// migrated table and calling that an upgrade test.
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() > "0007_integrity.sql" {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
		version := entry.Name()[:len(entry.Name())-4]
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO accounts(x_uid,user_id,session_token,session_base,session_generation) VALUES('upgrade-account',42,'legacy-device',1000,9); INSERT INTO gems(user_id,free,paid) VALUES(42,123,456)`); err != nil {
		t.Fatal(err)
	}
	original := []Row{{"_masterStoryId": json.Number("9990000000000001"), "_isClear": true, "_futureProgress": 99}}
	raw, _ := json.Marshal(original)
	oldHash, _ := deltacomm.HashTable(original)
	if _, err := pool.Exec(ctx, `INSERT INTO user_state(user_id,table_name,rows,hash) VALUES(42,'user_story',$1,$2)`, raw, oldHash); err != nil {
		t.Fatal(err)
	}
	upgraded := &Store{pool: pool, seed: map[string][]Row{"user_story": {{"_masterStoryId": 1, "_isClear": false}}}}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.GetOrCreate(ctx, "upgrade-account"); err != nil {
		t.Fatal(err)
	}
	rows, err := upgraded.GetTables(ctx, 42, []string{"user_story"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(rows["user_story"])
	if string(got) != string(raw) {
		t.Fatalf("upgrade/new seed rewrote save: %s != %s", got, raw)
	}
	balance, err := upgraded.Get(ctx, 42)
	if err != nil || balance.Free != 123 || balance.Paid != 456 {
		t.Fatal(balance, err)
	}
	if _, err := upgraded.CheckSession(ctx, 42, "legacy-device", 999999); !errors.Is(err, store.ErrSession) {
		t.Fatal("pre-upgrade queue still admitted", err)
	}
	hashes, err := upgraded.AllHashes(ctx, 42)
	if err != nil || hashes["user_story"] == oldHash {
		t.Fatal("upgrade did not change legacy token", hashes, err)
	}
	var archived []byte
	if err := pool.QueryRow(ctx, `SELECT old_data FROM player_data_history WHERE user_id=42 AND operation='UPGRADE'`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	saved, _ := unmarshalRows(archived)
	savedRaw, _ := json.Marshal(saved)
	if string(savedRaw) != string(raw) {
		t.Fatal("pre-upgrade copy not recoverable")
	}
	if _, err := upgraded.ApplyDeltas(ctx, 42, storyDelta(false), map[string]string{"user_story": oldHash}); !errors.Is(err, store.ErrConcurrency) {
		t.Fatal("pre-upgrade baseline accepted", err)
	}
	if _, err := upgraded.ApplyClientDeltas(ctx, 42, storyDelta(false), hashes, 100); !errors.Is(err, store.ErrConcurrency) {
		t.Fatal("pre-upgrade packet with refreshed baseline accepted", err)
	}
	// Reapplying migrations/restarting is harmless and does not rotate tokens.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	after, err := upgraded.AllHashes(ctx, 42)
	if err != nil || after["user_story"] != hashes["user_story"] {
		t.Fatal("repeat startup changed saved version", after, err)
	}
}

func TestSaveTruncateIsRejectedWithoutLosingData(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE user_state`); err == nil {
		t.Fatal("unrecoverable truncate allowed")
	}
	rows, err := s.GetTables(ctx, uid, []string{"user_story"})
	if err != nil || len(rows["user_story"]) != 1 {
		t.Fatal(rows, err)
	}
}
