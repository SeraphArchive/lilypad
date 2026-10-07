package replay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"lilypad/internal/config"
	"lilypad/internal/fixtures"
	"lilypad/internal/httpapi"
	"lilypad/internal/master"
	"lilypad/internal/model"
	"lilypad/internal/replay"
	"lilypad/internal/sign"
	"lilypad/internal/store/postgres"
)

// TestReplayCaptureStructuralFidelity replays the real capture through the live
// handler stack and asserts structural fidelity per route. Gated on all three
// external inputs; skips (green) when any is absent.
func TestReplayCaptureStructuralFidelity(t *testing.T) {
	fixPath := os.Getenv("LILYPAD_FIXTURES")
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if fixPath == "" || dsn == "" || dir == "" {
		t.Skip("set LILYPAD_FIXTURES, LILYPAD_TEST_DSN, LILYPAD_DATA_DIR to run capture replay")
	}
	recs, err := fixtures.Load(fixPath)
	if err != nil {
		t.Fatal(err)
	}

	// Self-configure systemLock/lotteryShop constants from the capture's own
	// app/start response so the envelope matches.
	cfg := &config.Config{}
	for _, p := range fixtures.Pairs(recs) {
		if p.Route == "/api/app/start" {
			var body map[string]any
			if err := json.Unmarshal(p.Resp.Body, &body); err == nil {
				if sl, ok := body["systemLock"].([]any); ok {
					cfg.Constants.SystemLock = sl
				}
				if ls, ok := body["lotteryShop"].([]any); ok {
					cfg.Constants.LotteryShop = ls
				}
			}
			break
		}
	}

	pg, err := postgres.New(context.Background(), dsn, model.NewPlayerSeed())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	md, err := master.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httpapi.New(cfg, sign.NoopSigner{}, pg, pg, md, nil)

	// Replay against a freshly-seeded account (unique per run) so confirm/pull
	// reflect the new-player seed rather than a pre-existing account.
	player := fmt.Sprintf("replay-%d", time.Now().UnixNano())
	results := replay.ReplayPairs(srv, recs, replay.Options{OverridePlayerID: player})
	t.Log("\n" + replay.Summary(results))

	// Aggregate match counts per route.
	type mc struct{ ok, total int }
	agg := map[string]*mc{}
	for _, r := range results {
		a := agg[r.Route]
		if a == nil {
			a = &mc{}
			agg[r.Route] = a
		}
		a.total++
		if r.OK() {
			a.ok++
		}
	}

	// Routes that must match exactly (server-authoritative, state-independent).
	// A route absent from this capture is skipped, not an error.
	for _, route := range []string{"/api/app/start", "/api/random/setup", "/api/user/migration/prepare"} {
		a := agg[route]
		if a == nil {
			continue
		}
		if a.ok != a.total {
			t.Errorf("%s: expected full structural match, got %v", route, a)
		}
	}

	// confirm matches fully (the hash map shape is state-independent). pull only
	// matches fully against a first-session capture: a mature account's pulls
	// return populated tables the fresh seed doesn't have, so there the
	// per-route aggregates above are the review tool.
	if a := agg["/api/user/confirm"]; a == nil || a.ok != a.total {
		t.Errorf("/api/user/confirm: expected full structural match, got %v", a)
	}
	if a := agg["/api/user/pull"]; a != nil && captureIsFreshAccount(recs) && a.ok != a.total {
		t.Errorf("/api/user/pull: expected full structural match with seed, got %v", a)
	}

	// The DeltaComm push loop should match for the vast majority of messages.
	if a := agg["/api/user/push"]; a == nil || a.ok < a.total*9/10 {
		t.Errorf("/api/user/push: expected >=90%% structural match, got %v", a)
	}
}

// captureIsFreshAccount reports whether the capture's first pull returns only
// empty tables (a brand-new account). Mature-account captures return populated
// rows, which the new-player seed cannot reproduce structurally.
func captureIsFreshAccount(recs []fixtures.Record) bool {
	for _, p := range fixtures.Pairs(recs) {
		if p.Route != "/api/user/pull" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(p.Resp.Body, &body); err != nil {
			continue
		}
		tables, _ := body["tables"].(map[string]any)
		for _, rows := range tables {
			if arr, ok := rows.([]any); ok && len(arr) > 0 {
				return false
			}
		}
		return true
	}
	return true
}
