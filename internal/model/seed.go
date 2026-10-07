package model

import (
	"bytes"
	_ "embed"
	"encoding/json"
)

//go:embed newplayer_seed.json
var newPlayerSeedJSON []byte

// NewPlayerSeed returns the initial per-table state installed for a brand-new
// account: the full table set (so user/confirm reports every table) with
// anonymized starter rows for the few tables that carry them. Numbers decode as
// json.Number so seed-time hashes match the hashes computed on read-back.
func NewPlayerSeed() map[string][]map[string]any {
	var doc struct {
		Tables map[string][]map[string]any `json:"tables"`
	}
	dec := json.NewDecoder(bytes.NewReader(newPlayerSeedJSON))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		panic("model: bad newplayer_seed.json: " + err.Error())
	}
	if doc.Tables == nil {
		doc.Tables = map[string][]map[string]any{}
	}
	return doc.Tables
}
