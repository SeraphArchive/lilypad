package fixtures

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndGroup(t *testing.T) {
	jsonl := `{"ts":"1","kind":"REQ","route":"/api/app/start","headers":{},"body":{"hdr":"x"}}
{"ts":"2","kind":"RESP","route":"/api/app/start","headers":{},"body":{"code":0}}
{"ts":"3","kind":"REQ","route":"/api/user/push","headers":{},"body":{"hdr":"x"}}
{"ts":"4","kind":"RESP","route":"/api/user/push","headers":{},"body":{"code":0}}
`
	p := filepath.Join(t.TempDir(), "cap.jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("want 4 recs, got %d", len(recs))
	}
	g := GroupByRoute(recs)
	if len(g["/api/app/start"]) != 1 || len(g["/api/user/push"]) != 1 {
		t.Fatalf("grouping wrong: %+v", g)
	}
}

func TestLoadMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestPairsSkipsUnmatched(t *testing.T) {
	recs := []Record{
		{Kind: "REQ", Route: "/a"},
		{Kind: "REQ", Route: "/a"},
		{Kind: "RESP", Route: "/a"},
	}
	if got := len(Pairs(recs)); got != 1 {
		t.Fatalf("want 1 pair, got %d", got)
	}
}

// The real capture has emitted ts/status as both quoted strings and bare
// numbers; Load must tolerate both.
func TestLoadFlexibleTSAndStatus(t *testing.T) {
	jsonl := `{"ts":1781528436.59,"kind":"REQ","route":"/a","status":null,"body":{}}
{"ts":"2","kind":"RESP","route":"/a","status":"None","body":{}}
`
	p := filepath.Join(t.TempDir(), "flex.jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := Load(p)
	if err != nil {
		t.Fatalf("flexible parse failed: %v", err)
	}
	if recs[0].TS.String() != "1781528436.59" {
		t.Fatalf("numeric ts not preserved: %q", recs[0].TS)
	}
	if recs[1].Status.String() != "None" {
		t.Fatalf("string status lost: %q", recs[1].Status)
	}
}
