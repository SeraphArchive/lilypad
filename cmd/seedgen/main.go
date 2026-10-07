// Command seedgen derives the committed new-player seed template from the
// captured first session. It takes the full table set from the user/confirm
// response and the starter rows from the user/pull response, then anonymizes:
// strings are blanked, timestamps and account-unique snowflake ids are zeroed,
// and collection keys are re-sequenced so rows stay distinct. The real capture
// is never committed; only this anonymized template is.
//
// Usage: LILYPAD_FIXTURES=<capture.jsonl> go run ./cmd/seedgen -out internal/model/newplayer_seed.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"lilypad/internal/fixtures"
	"lilypad/internal/model"
)

var timestampKey = regexp.MustCompile(`(?i)(at|date|datetime|time)$`)

const snowflakeThreshold = 1_000_000_000_000 // 1e12: above master-id range

func main() {
	path := flag.String("path", os.Getenv("LILYPAD_FIXTURES"), "capture .jsonl path")
	out := flag.String("out", "internal/model/newplayer_seed.json", "output seed file")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "seedgen: set -path or LILYPAD_FIXTURES")
		os.Exit(2)
	}
	recs, err := fixtures.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seedgen:", err)
		os.Exit(1)
	}

	var confirm, pull map[string]any
	for _, p := range fixtures.Pairs(recs) {
		switch p.Route {
		case "/api/user/confirm":
			if confirm == nil {
				confirm = decodeBody(p.Resp.Body)
			}
		case "/api/user/pull":
			if pull == nil {
				pull = decodeBody(p.Resp.Body)
			}
		}
	}
	if confirm == nil || pull == nil {
		fmt.Fprintln(os.Stderr, "seedgen: capture missing confirm and/or pull")
		os.Exit(1)
	}

	// Full table set from confirm hashes; starter rows from pull.
	tableNames := map[string]bool{}
	if h, ok := confirm["hashes"].(map[string]any); ok {
		for name := range h {
			tableNames[name] = true
		}
	}
	pullTables, _ := pull["tables"].(map[string]any)
	for name := range pullTables {
		tableNames[name] = true
	}

	seed := map[string][]map[string]any{}
	for name := range tableNames {
		rows := []map[string]any{}
		if raw, ok := pullTables[name].([]any); ok {
			for _, r := range raw {
				if m, ok := r.(map[string]any); ok {
					rows = append(rows, neutralizeRow(m))
				}
			}
		}
		resequenceKeys(name, rows)
		seed[name] = rows
	}

	doc := map[string]any{
		"_meta": map[string]any{
			"note": "Anonymized new-player seed derived from the captured first session. " +
				"Strings blanked, timestamps and snowflake ids zeroed, collection keys re-sequenced.",
			"tableCount": len(seed),
		},
		"tables": seed,
	}
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "seedgen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "seedgen:", err)
		os.Exit(1)
	}

	nonEmpty := 0
	for _, rows := range seed {
		if len(rows) > 0 {
			nonEmpty++
		}
	}
	fmt.Printf("wrote %s: %d tables (%d with starter rows)\n", *out, len(seed), nonEmpty)
}

func decodeBody(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// neutralizeRow returns an anonymized copy of a row.
func neutralizeRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = neutralizeValue(k, v)
	}
	return out
}

func neutralizeValue(key string, v any) any {
	switch t := v.(type) {
	case string:
		return "" // blank all free text (names, locale, comments)
	case map[string]any:
		return neutralizeRow(t)
	case []any:
		arr := make([]any, len(t))
		for i, e := range t {
			arr[i] = neutralizeValue(key, e)
		}
		return arr
	case float64:
		return scrubNumber(key, int64(t))
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0
		}
		return scrubNumber(key, n)
	case bool:
		return t
	default:
		return v
	}
}

func scrubNumber(key string, n int64) int64 {
	if timestampKey.MatchString(key) {
		return 0
	}
	if n >= snowflakeThreshold { // account-unique instance id, not a master id
		return 0
	}
	return n
}

// resequenceKeys makes collection rows distinct again after scrubbing zeroed a
// unique key (e.g. _giftId). Single-field keys are re-sequenced 1..n.
func resequenceKeys(table string, rows []map[string]any) {
	spec := model.SpecFor(table)
	if spec.Kind != model.Collection || len(spec.Key) != 1 || len(rows) < 2 {
		return
	}
	field := spec.Key[0]
	seen := map[any]bool{}
	dup := false
	for _, r := range rows {
		if seen[fmt.Sprint(r[field])] {
			dup = true
			break
		}
		seen[fmt.Sprint(r[field])] = true
	}
	if !dup {
		return
	}
	for i, r := range rows {
		r[field] = int64(i + 1)
	}
}
