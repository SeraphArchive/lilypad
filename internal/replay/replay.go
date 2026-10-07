// Package replay drives captured requests through the live handler stack and
// structurally diffs the decoded responses against the captured responses. It
// compares JSON *shape* (object keys, array emptiness, leaf types) rather than
// volatile values (server-owned hashes, generated ids, timestamps, RNG), so it
// catches missing/extra fields and table sets without false positives on values.
package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"lilypad/internal/codec"
	"lilypad/internal/fixtures"
)

// Result is the structural comparison outcome for one captured pair.
type Result struct {
	Route string
	Diffs []string // empty => structurally matched
}

// OK reports whether the response matched structurally.
func (r Result) OK() bool { return len(r.Diffs) == 0 }

// Options controls the replay.
type Options struct {
	// IgnoreLeafKeys are object keys whose subtree shape is not compared (still
	// reported as present/absent). Use for deeply volatile blobs.
	IgnoreLeafKeys map[string]bool
	// OverridePlayerID, when set, replaces the x-player-id header on every
	// request so the replay runs against a single, freshly-seeded account
	// (independent of the captured account already existing in the database).
	OverridePlayerID string
	// Strict checks array cardinality/emptiness as well as every row's shape.
	// Volatile hashes, generated IDs, timestamps and RNG values remain opaque.
	Strict bool
}

// ReplayPairs sends each captured request through h (in capture order, so player
// state accumulates) and returns one Result per pair.
func ReplayPairs(h http.Handler, recs []fixtures.Record, opts Options) []Result {
	var out []Result
	for _, p := range fixtures.Pairs(recs) {
		out = append(out, replayOne(h, p, opts))
	}
	return out
}

func replayOne(h http.Handler, p fixtures.Pair, opts Options) Result {
	res := Result{Route: p.Route}

	var reqBody any
	if err := unmarshalNumber(p.Req.Body, &reqBody); err != nil {
		if len(bytes.TrimSpace(p.Req.Body)) != 0 {
			res.Diffs = []string{"parse captured request: " + err.Error()}
			return res
		}
		reqBody = map[string]any{}
	}
	wire, err := codec.Encode(reqBody)
	if err != nil {
		res.Diffs = []string{"encode request: " + err.Error()}
		return res
	}
	req := httptest.NewRequest(http.MethodPost, p.Route, bytes.NewReader(wire))
	for k, v := range p.Req.Headers {
		req.Header.Set(k, v)
	}
	if opts.OverridePlayerID != "" {
		req.Header.Set("x-player-id", opts.OverridePlayerID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if p.Resp.Status == "401" {
		// Captured concurrent-login invalidation (a second device took over —
		// not reproducible by replaying a single session). The stale-session 401
		// behavior is covered by its own integration test; successful replay
		// and session rejection are acceptable, unrelated HTTP failures are not.
		if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
			res.Diffs = []string{fmt.Sprintf("status %d", rec.Code)}
		}
		return res
	}
	if rec.Code != http.StatusOK {
		res.Diffs = []string{fmt.Sprintf("status %d", rec.Code)}
		return res
	}
	got, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		res.Diffs = []string{"decode response: " + err.Error()}
		return res
	}
	var want any
	if err := unmarshalNumber(p.Resp.Body, &want); err != nil {
		res.Diffs = []string{"parse captured response: " + err.Error()}
		return res
	}
	res.Diffs = structDiff(any(map[string]any(got)), want, "", opts)
	sort.Strings(res.Diffs)
	return res
}

// structDiff compares the shape of got against want, reporting differences.
func structDiff(got, want any, path string, opts Options) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: type mismatch (got %s, want object)", at(path), kind(got))}
		}
		var diffs []string
		for k, wv := range w {
			cp := join(path, k)
			gv, present := g[k]
			if !present {
				diffs = append(diffs, "missing key "+cp)
				continue
			}
			if opts.IgnoreLeafKeys[k] {
				continue
			}
			diffs = append(diffs, structDiff(gv, wv, cp, opts)...)
		}
		for k := range g {
			if _, present := w[k]; !present {
				diffs = append(diffs, "extra key "+join(path, k))
			}
		}
		return diffs
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: type mismatch (got %s, want array)", at(path), kind(got))}
		}
		if len(w) > 0 && len(g) == 0 {
			return []string{fmt.Sprintf("%s: empty array (capture has %d elems)", at(path), len(w))}
		}
		if opts.Strict && len(g) != len(w) {
			return []string{fmt.Sprintf("%s: array length mismatch (got %d, want %d)", at(path), len(g), len(w))}
		}
		if len(w) > 0 && len(g) > 0 {
			var diffs []string
			for i, gv := range g {
				wv := w[0]
				if i < len(w) {
					wv = w[i]
				}
				diffs = append(diffs, structDiff(gv, wv, path+"[]", opts)...)
			}
			return diffs
		}
		return nil
	default:
		if path == "code" && fmt.Sprint(got) != fmt.Sprint(want) {
			return []string{fmt.Sprintf("%s: status mismatch", at(path))}
		}
		if kind(got) != kind(want) {
			return []string{fmt.Sprintf("%s: leaf type mismatch (got %s, want %s)", at(path), kind(got), kind(want))}
		}
		return nil
	}
}

func unmarshalNumber(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

func kind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		return "number"
	case string:
		return "string"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func at(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}

// Summary aggregates results by route for reporting.
func Summary(results []Result) string {
	type agg struct {
		total, ok int
		diffs     map[string]bool
	}
	byRoute := map[string]*agg{}
	for _, r := range results {
		a := byRoute[r.Route]
		if a == nil {
			a = &agg{diffs: map[string]bool{}}
			byRoute[r.Route] = a
		}
		a.total++
		if r.OK() {
			a.ok++
		}
		for _, d := range r.Diffs {
			a.diffs[d] = true
		}
	}
	routes := make([]string, 0, len(byRoute))
	for r := range byRoute {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	var b strings.Builder
	for _, r := range routes {
		a := byRoute[r]
		fmt.Fprintf(&b, "%-34s %d/%d match\n", r, a.ok, a.total)
		ds := make([]string, 0, len(a.diffs))
		for d := range a.diffs {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		for _, d := range ds {
			fmt.Fprintf(&b, "      - %s\n", d)
		}
	}
	return b.String()
}
