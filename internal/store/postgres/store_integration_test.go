package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
)

// dsnOrSkip returns the test DSN or skips the test when none is configured, so
// the suite stays green without a database.
func dsnOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	if dsn == "" {
		t.Skip("LILYPAD_TEST_DSN not set; skipping Postgres integration test")
	}
	return dsn
}

func freshStore(t *testing.T, seed map[string][]Row) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := New(ctx, dsnOrSkip(t), seed)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Isolate: clear state tables (dedicated test database).
	if err := s.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM user_state; DELETE FROM gems; DELETE FROM user_state_tokens; DELETE FROM player_data_history; DELETE FROM accounts; DELETE FROM platform_requestor_bindings; DELETE FROM request_results`)
		return err
	}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPGGems(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-gem")

	// Absent row reads as zero.
	b, err := s.Get(ctx, acc.UserID)
	if err != nil || b.Free != 0 || b.Paid != 0 {
		t.Fatalf("empty balance: %+v err=%v", b, err)
	}
	// Add free.
	if b, err = s.Add(ctx, acc.UserID, 500); err != nil || b.Free != 500 || b.Paid != 0 {
		t.Fatalf("add: %+v err=%v", b, err)
	}
	// Consume within free.
	if b, err = s.Consume(ctx, acc.UserID, 300); err != nil || b.Free != 200 {
		t.Fatalf("consume: %+v err=%v", b, err)
	}
	// Insufficient -> ErrInsufficient, balance unchanged.
	if _, err = s.Consume(ctx, acc.UserID, 1000); !errors.Is(err, gem.ErrInsufficient) {
		t.Fatalf("want ErrInsufficient, got %v", err)
	}
	if b, _ = s.Get(ctx, acc.UserID); b.Free != 200 {
		t.Fatalf("balance changed after insufficient: %+v", b)
	}
}

func TestPGGemsConcurrentConsume(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-gem-cc")
	if _, err := s.Add(ctx, acc.UserID, 10); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var okCount int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(ctx, acc.UserID, 1); err == nil {
				atomic.AddInt64(&okCount, 1)
			}
		}()
	}
	wg.Wait()
	if okCount != 10 {
		t.Fatalf("want exactly 10 successful consumes, got %d", okCount)
	}
	if b, _ := s.Get(ctx, acc.UserID); b.Free != 0 || b.Paid != 0 {
		t.Fatalf("balance not drained to zero: %+v", b)
	}
}

func TestPGUserExists(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-ue")
	if ok, err := s.UserExists(ctx, acc.UserID); err != nil || !ok {
		t.Fatalf("existing user: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UserExists(ctx, 424242); err != nil || ok {
		t.Fatalf("absent user should be false: ok=%v err=%v", ok, err)
	}
}

func TestPGGetOrCreateStable(t *testing.T) {
	s := freshStore(t, nil)
	ctx := context.Background()
	a1, err := s.GetOrCreate(ctx, "xuid-1")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.GetOrCreate(ctx, "xuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if a1.UserID != a2.UserID {
		t.Fatalf("userId not stable: %d vs %d", a1.UserID, a2.UserID)
	}
	b, _ := s.GetOrCreate(ctx, "xuid-2")
	if b.UserID == a1.UserID {
		t.Fatal("distinct xuids must get distinct userIds")
	}
}

func TestPGRequestorBindingPersistsAcrossStore(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	if err := s.BindRequestor(ctx, "requestor-1", "xuid-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.XUIDForRequestor(ctx, "requestor-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xuid-1" {
		t.Fatalf("binding xuid = %q, want xuid-1", got)
	}

	dsn := os.Getenv("LILYPAD_TEST_DSN")
	s2, err := New(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)
	got, err = s2.XUIDForRequestor(ctx, "requestor-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xuid-1" {
		t.Fatalf("binding after reopen = %q, want xuid-1", got)
	}
}

func TestPGPushPullRoundTripAndPersist(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-rt")

	d := deltacomm.Deltas{
		PutItems: map[string][]Row{
			"user_card":    {{"_masterCardId": 1001101, "lvl": 1}, {"_masterCardId": 1001201, "lvl": 1}},
			"user_version": {{"_bundleVersion": "1.1.0"}},
		},
	}
	hashes, err := s.ApplyDeltas(ctx, acc.UserID, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 2 || len(hashes["user_card"]) != 32 {
		t.Fatalf("push hashes wrong: %v", hashes)
	}

	got, err := s.GetTables(ctx, acc.UserID, []string{"user_card", "user_version", "user_item"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["user_card"]) != 2 {
		t.Fatalf("user_card rows: %v", got["user_card"])
	}
	if len(got["user_item"]) != 0 {
		t.Fatalf("absent table must be empty: %v", got["user_item"])
	}

	// Upsert one card; row count stays 2.
	_, err = s.ApplyDeltas(ctx, acc.UserID, deltacomm.Deltas{
		PutItems: map[string][]Row{"user_card": {{"_masterCardId": 1001101, "lvl": 9}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetTables(ctx, acc.UserID, []string{"user_card"})
	if len(got2["user_card"]) != 2 {
		t.Fatalf("upsert changed row count: %v", got2["user_card"])
	}

	// Reopen the store: state must survive (persistence).
	s.Close()
	s2, err := New(ctx, os.Getenv("LILYPAD_TEST_DSN"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)
	persisted, _ := s2.GetTables(ctx, acc.UserID, []string{"user_card"})
	if len(persisted["user_card"]) != 2 {
		t.Fatalf("state did not persist across restart: %v", persisted)
	}
	h, _ := s2.AllHashes(ctx, acc.UserID)
	if h["user_version"] == "" {
		t.Fatalf("hashes missing after restart: %v", h)
	}
}

func TestPGConcurrencyBaselineConflict(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-cc")
	h, err := s.ApplyDeltas(ctx, acc.UserID,
		deltacomm.Deltas{PutItems: map[string][]Row{"user_version": {{"_bundleVersion": "1"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Correct baseline succeeds.
	if _, err := s.ApplyDeltas(ctx, acc.UserID,
		deltacomm.Deltas{PutItems: map[string][]Row{"user_version": {{"_bundleVersion": "2"}}}},
		map[string]string{"user_version": h["user_version"]}); err != nil {
		t.Fatalf("matching baseline should succeed: %v", err)
	}
	// Stale baseline is rejected.
	if _, err := s.ApplyDeltas(ctx, acc.UserID,
		deltacomm.Deltas{PutItems: map[string][]Row{"user_version": {{"_bundleVersion": "3"}}}},
		map[string]string{"user_version": "stalehashvalue"}); err == nil {
		t.Fatal("stale baseline must be rejected")
	}
}

func TestPGSeedNewPlayer(t *testing.T) {
	ctx := context.Background()
	seed := map[string][]Row{"user_card": {{"_masterCardId": 1001101}}}
	s := freshStore(t, seed)
	acc, _ := s.GetOrCreate(ctx, "xuid-seed")
	got, _ := s.GetTables(ctx, acc.UserID, []string{"user_card"})
	if len(got["user_card"]) != 1 {
		t.Fatalf("new player not seeded: %v", got)
	}
}

func TestPGInvalidateSessions(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, _ := s.GetOrCreate(ctx, "xuid-kick")

	if err := s.SetSessionBase(ctx, acc.UserID, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateSessions(ctx, acc.UserID); err != nil {
		t.Fatal(err)
	}
	base, err := s.SessionBase(ctx, acc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	// Import is a login-at-now: base is wall-clock unix time (same order as
	// a fresh client's x-msgid), not a magic future stamp.
	if base < 1_700_000_000 {
		t.Fatalf("session base %d is not a unix-now stamp", base)
	}

	// A still-higher base (concurrent newer login) must not be lowered.
	high := base + 10_000_000
	if err := s.SetSessionBase(ctx, acc.UserID, high); err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateSessions(ctx, acc.UserID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.SessionBase(ctx, acc.UserID)
	if got != high {
		t.Fatalf("invalidate lowered a higher base: got %d want %d", got, high)
	}
}
