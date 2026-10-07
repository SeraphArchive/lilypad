package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"lilypad/internal/codec"
	"lilypad/internal/config"
	"lilypad/internal/sign"
	"lilypad/internal/store/postgres"
)

var testMessageID atomic.Int64

func testSessionToken(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}

func newDBServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	if dsn == "" {
		t.Skip("LILYPAD_TEST_DSN not set; skipping DeltaComm HTTP integration test")
	}
	pg, err := postgres.New(context.Background(), dsn, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(pg.Close)
	return New(&config.Config{}, sign.NoopSigner{}, pg, pg, nil, nil)
}

// post sends an encoded body to route as player xuid and returns the decoded response.
func post(t *testing.T, s *Server, route, xuid string, body any) map[string]any {
	t.Helper()
	// These integration fixtures represent one real, authenticated client.
	// Explicit session/replay tests use their own headers and bypass this helper.
	if rs, ok := s.store.(interface {
		BeginSession(context.Context, int64, string, int64) error
	}); ok && xuid != "" {
		uid, ok := s.resolvePlayer(context.Background(), xuid)
		if !ok {
			t.Fatal("test client identity could not be resolved")
		}
		if err := rs.BeginSession(context.Background(), uid, testSessionToken(xuid), 0); err != nil {
			t.Fatal(err)
		}
	}
	if route == "/api/user/push" {
		if m, ok := body.(map[string]any); ok {
			if _, present := m["savedTime"]; !present {
				m["savedTime"] = time.Now().Unix()
			}
		}
	}
	wire, err := codec.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(wire))
	if xuid != "" {
		req.Header.Set("x-player-id", xuid)
		req.Header.Set("x-lilypad-session", testSessionToken(xuid))
	}
	req.Header.Set("x-msgid", fmt.Sprint(testMessageID.Add(1)))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d", route, rec.Code)
	}
	out, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode %s: %v", route, err)
	}
	return out
}

// A client that modifies seeded state first reads its current versions. Keep
// this separate from post: missing-baseline regression tests must remain able
// to send an invalid request without the fixture silently repairing it.
func pushFromCurrentState(t *testing.T, s *Server, xuid string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var req pushRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"tables": req.Deltas.TouchedTables()})
	assertRPCSuccess(t, pull)
	copy := make(map[string]any, len(body)+1)
	for k, v := range body {
		copy[k] = v
	}
	copy["hashes"] = pull["hashes"]
	out := post(t, s, "/api/user/push", xuid, copy)
	assertRPCSuccess(t, out)
	return out
}

func TestDeltaCommLoopOverHTTP(t *testing.T) {
	s := newDBServer(t)
	xuid := fmt.Sprintf("e2e-%d", time.Now().UnixNano())

	// confirm on a fresh account -> empty hashes (rendered as an empty array,
	// matching the client's empty-dictionary serialization).
	conf := post(t, s, "/api/user/confirm", xuid, map[string]any{"hdr": ""})
	if conf["code"].(json.Number).String() != "0" {
		t.Fatalf("confirm code: %v", conf["code"])
	}
	if arr, ok := conf["hashes"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("fresh confirm hashes should be empty array: %v", conf["hashes"])
	}

	// push starter state.
	push := post(t, s, "/api/user/push", xuid, map[string]any{
		"hdr":     "",
		"trigger": "VersionUpdated",
		"deltas": map[string]any{
			"putItems": map[string]any{
				"user_card":    []any{map[string]any{"_masterCardId": 1001101, "lvl": 1}},
				"user_version": []any{map[string]any{"_bundleVersion": "1.1.0"}},
			},
		},
	})
	ph, ok := push["hashes"].(map[string]any)
	if !ok || len(ph) != 2 {
		t.Fatalf("push must return 2 table hashes: %v", push["hashes"])
	}

	// pull the pushed tables back.
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{
		"hdr": "", "tables": []any{"user_card", "user_version"},
	})
	tbls := pull["tables"].(map[string]any)
	cards := tbls["user_card"].([]any)
	if len(cards) != 1 {
		t.Fatalf("pull user_card rows: %v", cards)
	}

	// confirm now reports both tables, and the hashes match the push response.
	conf2 := post(t, s, "/api/user/confirm", xuid, map[string]any{"hdr": ""})
	h2 := conf2["hashes"].(map[string]any)
	if h2["user_card"] != ph["user_card"] {
		t.Fatalf("confirm hash != push hash: %v vs %v", h2["user_card"], ph["user_card"])
	}
}
