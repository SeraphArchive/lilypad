// Package model holds shared LilyPad domain types and the static table key map
// used by the DeltaComm delta-merge engine.
package model

import (
	_ "embed"
	"encoding/json"
)

//go:embed tablekeys.json
var tableKeysJSON []byte

// TableKind distinguishes collection tables (arrays merged by primary key) from
// singleton tables (whole-row replace).
type TableKind int

const (
	Unknown TableKind = iota
	Singleton
	Collection
)

// TableSpec describes how to merge deltas for a user table.
type TableSpec struct {
	Kind TableKind
	Key  []string // primary-key field names; non-empty only for collections
}

var keys map[string]TableSpec

func init() {
	var raw struct {
		SingletonTables  []string `json:"singleton_tables"`
		CollectionTables map[string]struct {
			Key []string `json:"key"`
		} `json:"collection_tables"`
	}
	if err := json.Unmarshal(tableKeysJSON, &raw); err != nil {
		panic("model: bad tablekeys.json: " + err.Error())
	}
	keys = make(map[string]TableSpec, len(raw.CollectionTables))
	for name, c := range raw.CollectionTables {
		keys[name] = TableSpec{Kind: Collection, Key: c.Key}
	}
	for _, name := range raw.SingletonTables {
		if _, exists := keys[name]; exists {
			panic("model: conflicting table kind: " + name)
		}
		keys[name] = TableSpec{Kind: Singleton}
	}
}

// Keys returns all verified table specifications (read-only).
func Keys() map[string]TableSpec { return keys }

// SpecFor returns only verified table semantics. Unknown tables are preserved
// in storage/imports but cannot be mutated until their schema is supported.
func SpecFor(table string) TableSpec {
	if s, ok := keys[table]; ok {
		return s
	}
	return TableSpec{Kind: Unknown}
}
