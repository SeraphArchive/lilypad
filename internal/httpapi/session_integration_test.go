package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lilypad/internal/codec"
	"lilypad/internal/store"
)

// postWithMsgid posts an encoded body with explicit x-player-id / x-msgid
// headers and returns the status code (and decoded body when 200).
func postWithMsgid(t *testing.T, s *Server, route, xuid string, msgid int64, body any) (int, map[string]any) {
	return postWithSession(t, s, route, xuid, testSessionToken(xuid+"a"), msgid, body)
}

func postWithSession(t *testing.T, s *Server, route, xuid, token string, msgid int64, body any) (int, map[string]any) {
	t.Helper()
	wire, err := codec.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(wire))
	if xuid != "" {
		req.Header.Set("x-player-id", xuid)
		req.Header.Set("x-lilypad-session", token)
	}
	if msgid > 0 {
		req.Header.Set("x-msgid", fmt.Sprintf("%d", msgid))
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	out, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode %s: %v", route, err)
	}
	return rec.Code, out
}

// A newer login (higher x-msgid base) invalidates older sessions: their next
// request is answered with the official stale-session signal — a bare 401 with
// an empty text/html body.
func TestConcurrentLoginInvalidatesOlderSession(t *testing.T) {
	s := newDBServer(t)
	xuid := fmt.Sprintf("session-%d", time.Now().UnixNano())

	// Device A logs in (session base 1000) and plays.
	if code, _ := postWithMsgid(t, s, "/api/user/client/id", xuid, 1000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("client/id status=%d", code)
	}
	if code, _ := postWithMsgid(t, s, "/api/user/confirm", xuid, 1001, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("confirm on the fresh session status=%d", code)
	}

	// Device B takes over (newer process -> higher msgid base).
	if code, _ := postWithSession(t, s, "/api/user/client/id", xuid, testSessionToken(xuid+"b"), 2000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("second client/id status=%d", code)
	}

	// A's next heartbeat is rejected; B works.
	code, _ := postWithMsgid(t, s, "/api/user/confirm", xuid, 1002, map[string]any{"hdr": ""})
	if code != http.StatusUnauthorized {
		t.Fatalf("stale confirm status=%d, want 401", code)
	}
	if code, _ := postWithSession(t, s, "/api/user/confirm", xuid, testSessionToken(xuid+"b"), 2001, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("new session confirm status=%d", code)
	}

	// The guard also covers push/pull.
	if code, _ := postWithMsgid(t, s, "/api/user/push", xuid, 1003, map[string]any{"hdr": ""}); code != http.StatusUnauthorized {
		t.Fatalf("stale push status=%d, want 401", code)
	}
	if code, _ := postWithMsgid(t, s, "/api/user/pull", xuid, 1004, map[string]any{"hdr": "", "tables": []any{}}); code != http.StatusUnauthorized {
		t.Fatalf("stale pull status=%d, want 401", code)
	}
}

// A stale device replaying its login must not drag the session base back
// down. Once B has taken over, A's older client/id is accepted (login routes
// are not guarded) but the base stays at B's value, so A's next heartbeat is
// still rejected.
func TestStaleLoginDoesNotLowerSessionBase(t *testing.T) {
	s := newDBServer(t)
	xuid := fmt.Sprintf("session-replay-%d", time.Now().UnixNano())

	if code, _ := postWithMsgid(t, s, "/api/user/client/id", xuid, 1000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("first client/id status=%d", code)
	}
	if code, _ := postWithSession(t, s, "/api/user/client/id", xuid, testSessionToken(xuid+"b"), 2000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("takeover client/id status=%d", code)
	}
	// A retries the login it already sent, with the smaller msgid.
	if code, _ := postWithMsgid(t, s, "/api/user/client/id", xuid, 1000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("replayed client/id status=%d", code)
	}
	if code, _ := postWithMsgid(t, s, "/api/user/confirm", xuid, 1001, map[string]any{"hdr": ""}); code != http.StatusUnauthorized {
		t.Fatalf("stale confirm after replayed login status=%d, want 401", code)
	}
	if code, _ := postWithSession(t, s, "/api/user/confirm", xuid, testSessionToken(xuid+"b"), 2001, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("live session confirm status=%d", code)
	}
}

// An out-of-band save write (Portal import) is a login: any live client's
// next heartbeat must 401 so they re-login and pull the rewritten state.
func TestImportInvalidatesLiveClient(t *testing.T) {
	s := newDBServer(t)
	xuid := fmt.Sprintf("import-kick-%d", time.Now().UnixNano())

	if code, _ := postWithMsgid(t, s, "/api/user/client/id", xuid, 1000, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("client/id status=%d", code)
	}
	if code, _ := postWithMsgid(t, s, "/api/user/confirm", xuid, 1001, map[string]any{"hdr": ""}); code != 200 {
		t.Fatalf("pre-import confirm status=%d", code)
	}

	userID, ok := s.resolvePlayer(context.Background(), xuid)
	if !ok {
		t.Fatal("resolve player")
	}
	if err := s.store.ImportTables(context.Background(), userID, map[string][]store.Row{
		"user_item": {{"_id": 1, "_num": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.InvalidateSessions(context.Background(), userID); err != nil {
		t.Fatal(err)
	}

	if code, _ := postWithMsgid(t, s, "/api/user/confirm", xuid, 1002, map[string]any{"hdr": ""}); code != http.StatusUnauthorized {
		t.Fatalf("post-import confirm status=%d, want 401", code)
	}
}
