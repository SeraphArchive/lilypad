package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/store"
	"math"
	"testing"
	"time"
)

func TestMigrationLockReleasedAndLegacyHashesRepaired(t *testing.T) {
	s := freshStore(t, nil)
	ctx := context.Background()
	acc, err := s.GetOrCreate(ctx, "hash-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTables(ctx, acc.UserID, map[string][]store.Row{"user_life": {{"_num": json.Number("1e3")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE user_state SET hash='old'; ALTER TABLE gems DROP CONSTRAINT gems_valid_balance; DELETE FROM schema_migrations WHERE version='0007_integrity'`); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsnOrSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["application_name"] = "migration-integrity-test"
	cfg.AfterRelease = func(c *pgx.Conn) bool { time.Sleep(time.Millisecond); return true }
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.application_name='migration-integrity-test' AND locktype='advisory'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d migration locks leaked", n)
	}
	snapshot, err := s.ExportSnapshot(ctx, acc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := s.AllHashes(ctx, acc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Hashes["user_life"] != hashes["user_life"] || len(hashes["user_life"]) != 32 || hashes["user_life"] == "old" {
		t.Fatal("legacy hash not repaired")
	}
}

func TestImportSnapshotRollsBackEveryComponent(t *testing.T) {
	s := freshStore(t, nil)
	ctx := context.Background()
	acc, err := s.GetOrCreate(ctx, "atomic-import")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTables(ctx, acc.UserID, map[string][]store.Row{"user_item": {{"_id": 1, "_num": 5}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, acc.UserID, gem.Balance{Free: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSession(ctx, acc.UserID, "device", 0); err != nil {
		t.Fatal(err)
	}
	generation, err := s.CheckSession(ctx, acc.UserID, "device", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportSnapshot(ctx, acc.UserID, map[string][]store.Row{"user_item": {{"_id": 1, "_num": math.NaN()}}}, &gem.Balance{Free: 200}); err == nil {
		t.Fatal("invalid replacement committed")
	}
	snapshot, err := s.ExportSnapshot(ctx, acc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Balance.Free != 100 || snapshot.Tables["user_item"][0]["_num"].(json.Number) != "5" {
		t.Fatal(snapshot)
	}
	after, err := s.CheckSession(ctx, acc.UserID, "device", 0)
	if err != nil || after != generation {
		t.Fatal("failed import changed session")
	}
	if err := s.ImportSnapshot(ctx, acc.UserID, map[string][]store.Row{"user_item": {{"_id": 1, "_num": 9}}}, &gem.Balance{Free: 200}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.ExportSnapshot(ctx, acc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Balance.Free != 200 || snapshot.Tables["user_item"][0]["_num"].(json.Number) != "9" {
		t.Fatal(snapshot)
	}
	if _, err := s.CheckSession(ctx, acc.UserID, "device", 9999999999); err != store.ErrSession {
		t.Fatal("import did not revoke session")
	}
}

func TestRequestCanRunOnSingleConnectionPool(t *testing.T) {
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsnOrSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := &Store{pool: pool}
	acc, err := s.GetOrCreate(ctx, "single-connection")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.BeginSession(ctx, acc.UserID, "device", 0); err != nil {
		t.Fatal(err)
	}
	generation, err := s.CheckSession(ctx, acc.UserID, "device", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RunRequest(ctx, acc.UserID, generation, "test", "digest", func(ctx context.Context) (store.Response, error) {
		if _, err := s.GetOrCreate(ctx, acc.XUID); err != nil {
			return store.Response{}, err
		}
		if _, err := s.CheckSession(ctx, acc.UserID, "device", 0); err != nil {
			return store.Response{}, err
		}
		if _, err := s.Get(ctx, acc.UserID); err != nil {
			return store.Response{}, err
		}
		_, err := s.MutateWithGems(ctx, acc.UserID, nil, func(map[string][]Row) (deltacomm.Deltas, store.GemChange, error) {
			return deltacomm.Deltas{}, store.GemChange{Add: 1}, nil
		})
		return store.Response{Status: 200}, err
	})
	if err != nil {
		t.Fatal(err)
	}
}
