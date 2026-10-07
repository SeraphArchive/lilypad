// Package fixtures loads captured REQ/RESP traffic (the behavioral oracle) from
// a JSONL file. The capture contains real account data and lives outside the
// repo; callers supply its path via config/env and must tolerate its absence.
package fixtures

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Record is one captured HTTP message. Body is the already-decoded JSON object.
type Record struct {
	TS      flexString        `json:"ts"`
	Kind    string            `json:"kind"` // REQ | RESP
	Route   string            `json:"route"`
	Status  flexString        `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// flexString accepts a JSON string, number, bool, or null — capture tooling has
// emitted `ts`/`status` as both quoted strings and bare numbers across versions.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = flexString(str)
		return nil
	}
	*s = flexString(b) // number/bool: keep the literal text
	return nil
}

func (s flexString) String() string { return string(s) }

// Pair is a request immediately followed by its response on the same route.
type Pair struct {
	Route     string
	Req, Resp Record
}

// Load reads all records from a JSONL capture file.
func Load(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("fixtures: open %s: %w", path, err)
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("fixtures: parse: %w", err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("fixtures: scan: %w", err)
	}
	return recs, nil
}

// Pairs matches each REQ with the next RESP on the same route.
func Pairs(recs []Record) []Pair {
	var out []Pair
	for i := 0; i+1 < len(recs); i++ {
		if recs[i].Kind == "REQ" && recs[i+1].Kind == "RESP" && recs[i].Route == recs[i+1].Route {
			out = append(out, Pair{Route: recs[i].Route, Req: recs[i], Resp: recs[i+1]})
		}
	}
	return out
}

// GroupByRoute groups REQ/RESP pairs by route path.
func GroupByRoute(recs []Record) map[string][]Pair {
	g := map[string][]Pair{}
	for _, p := range Pairs(recs) {
		g[p.Route] = append(g[p.Route], p)
	}
	return g
}
