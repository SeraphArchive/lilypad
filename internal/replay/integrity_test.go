package replay_test

import (
	"encoding/json"
	"fmt"
	"lilypad/internal/codec"
	"lilypad/internal/fixtures"
	"lilypad/internal/replay"
	"net/http"
	"testing"
)

func TestReplayRejectsBadStatusAndLaterRows(t *testing.T) {
	want := `{"code":0,"rows":[{"id":1},{"id":2}],"empty":[]}`
	got := map[string]any{"code": 1, "rows": []any{map[string]any{"id": 1}, "malformed"}, "empty": []any{1}}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { raw, _ := codec.Encode(got); w.Write(raw) })
	records := []fixtures.Record{{Kind: "REQ", Route: "/api/test", Body: json.RawMessage(`{}`)}, {Kind: "RESP", Route: "/api/test", Body: json.RawMessage(want)}}
	results := replay.ReplayPairs(h, records, replay.Options{Strict: true})
	if len(results) != 1 || results[0].OK() {
		t.Fatal("bad response counted as match")
	}
}

func TestReplaySessionExceptionDoesNotHideServerErrors(t *testing.T) {
	var response fixtures.Record
	if err := json.Unmarshal([]byte(`{"kind":"RESP","route":"/api/test","status":401,"body":{}}`), &response); err != nil {
		t.Fatal(err)
	}
	records := []fixtures.Record{{Kind: "REQ", Route: "/api/test", Body: json.RawMessage(`{}`)}, response}
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			results := replay.ReplayPairs(h, records, replay.Options{Strict: true})
			wantOK := status == http.StatusOK || status == http.StatusUnauthorized
			if len(results) != 1 || results[0].OK() != wantOK {
				t.Fatalf("results = %+v, want match %v", results, wantOK)
			}
		})
	}
}

func TestReplayRejectsMalformedCapturedRequestBeforeDispatch(t *testing.T) {
	for _, body := range []string{`{"incomplete":`, `{} {}`, `{} trailing`} {
		t.Run(body, func(t *testing.T) {
			called := false
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			records := []fixtures.Record{
				{Kind: "REQ", Route: "/api/test", Body: json.RawMessage(body)},
				{Kind: "RESP", Route: "/api/test", Body: json.RawMessage(`{}`)},
			}
			results := replay.ReplayPairs(h, records, replay.Options{})
			if called || len(results) != 1 || results[0].OK() {
				t.Fatalf("malformed request dispatched=%v, results=%+v", called, results)
			}
		})
	}
}
