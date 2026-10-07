// Package deltacomm implements the DeltaComm save-sync protocol: the
// confirm/pull/push loop and the per-table delta-merge engine. The merge engine
// is pure (no storage dependency) and uses the static table key map to decide
// collection vs singleton semantics.
package deltacomm

import (
	"fmt"

	"lilypad/internal/hashing"
	"lilypad/internal/model"
)

// Row is a single user-table row.
type Row = map[string]any

// Deltas is the push payload's mutation set. Each map is table name -> rows.
type Deltas struct {
	PutItems     map[string][]Row `json:"putItems"`
	DeleteItems  map[string][]Row `json:"deleteItems"`
	ReplaceItems map[string][]Row `json:"replaceItems"`
}

// TouchedTables returns the set of table names referenced by any delta op.
func (d Deltas) TouchedTables() []string {
	seen := map[string]bool{}
	var out []string
	add := func(m map[string][]Row) {
		for t := range m {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	add(d.ReplaceItems)
	add(d.PutItems)
	add(d.DeleteItems)
	return out
}

// MergeTable applies one table's deltas to its current rows and returns the new
// row set. replaceItems wins outright (whole-table replace); otherwise the
// merge depends on the table kind from the key map.
func MergeTable(table string, current, puts, deletes, replace []Row, hasReplace bool) ([]Row, error) {
	for _, rows := range [][]Row{current, puts, deletes, replace} {
		if err := ValidateRows(table, rows); err != nil {
			return nil, err
		}
	}
	if hasReplace {
		return cloneRows(replace), nil
	}
	spec := model.SpecFor(table)
	if spec.Kind == model.Singleton {
		if len(puts) > 0 && len(deletes) > 0 {
			return nil, fmt.Errorf("%s ambiguous put and delete for the singleton", table)
		}
		switch {
		case len(puts) > 0:
			if len(current) == 0 {
				return cloneRows(puts), nil
			}
			merged, err := mergeRow(table, current[0], puts[0])
			if err != nil {
				return nil, err
			}
			return []Row{merged}, nil
		case len(deletes) > 0:
			return []Row{}, nil
		default:
			return cloneRows(current), nil
		}
	}
	return mergeCollection(table, current, puts, deletes, spec.Key)
}

func mergeCollection(table string, current, puts, deletes []Row, key []string) ([]Row, error) {
	out := cloneRows(current)
	index := make(map[string]int, len(out))
	for i, r := range out {
		k, err := keyOf(r, key)
		if err != nil {
			return nil, fmt.Errorf("deltacomm: %s: current row: %w", table, err)
		}
		index[k] = i
	}
	for _, p := range puts {
		k, err := keyOf(p, key)
		if err != nil {
			return nil, fmt.Errorf("deltacomm: %s: put row: %w", table, err)
		}
		if i, ok := index[k]; ok {
			merged, err := mergeRow(table, out[i], p)
			if err != nil {
				return nil, err
			}
			out[i] = merged
		} else {
			index[k] = len(out)
			out = append(out, cloneValue(p).(Row))
		}
	}
	for _, d := range deletes {
		k, err := keyOf(d, key)
		if err != nil {
			return nil, fmt.Errorf("deltacomm: %s: delete row: %w", table, err)
		}
		for _, p := range puts {
			pk, _ := keyOf(p, key)
			if pk == k {
				return nil, fmt.Errorf("%s ambiguous put and delete for the same row", table)
			}
		}
		if i, ok := index[k]; ok {
			out = append(out[:i], out[i+1:]...)
			delete(index, k)
			// reindex rows after the removed position
			for j := i; j < len(out); j++ {
				kk, err := keyOf(out[j], key)
				if err != nil {
					return nil, err
				}
				index[kk] = j
			}
		}
	}
	if out == nil {
		out = []Row{}
	}
	return out, nil
}

// keyOf builds a stable composite key string from the named fields of a row.
func keyOf(r Row, fields []string) (string, error) {
	vals := make([]any, len(fields))
	for i, f := range fields {
		value, ok := r[f]
		if !ok || value == nil {
			return "", fmt.Errorf("missing collection key %s", f)
		}
		vals[i] = value
	}
	b, err := hashing.CanonicalJSON(vals)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func cloneRows(in []Row) []Row {
	out := make([]Row, len(in))
	for i, r := range in {
		out[i] = cloneValue(r).(map[string]any)
	}
	return out
}

// CloneRows isolates JSON trees used by read/modify/write callbacks, including
// nested objects/arrays; a shallow copy would mutate the merge base itself.
func CloneRows(in []Row) []Row { return cloneRows(in) }

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		if v == nil {
			return map[string]any(nil)
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = cloneValue(x)
		}
		return out
	case []any:
		if v == nil {
			return []any(nil)
		}
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cloneValue(x)
		}
		return out
	case []Row:
		if v == nil {
			return []Row(nil)
		}
		return cloneRows(v)
	default:
		return value
	}
}

// A previous/foreign client does not know newer fields. Missing fields are
// retained; explicit values (including false, zero and null) still update them.
func mergeRow(table string, current, put Row) (Row, error) {
	out := make(Row, len(current)+len(put))
	for k, v := range current {
		out[k] = cloneValue(v)
	}
	for k, v := range put {
		if table == "user_world_state" && k == "_localFlag" {
			flags, err := mergeWorldLocalFlags(out[k], v)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", k, err)
			}
			out[k] = flags
			continue
		}
		old, oldOK := out[k].(map[string]any)
		updated, newOK := v.(map[string]any)
		if oldOK && newOK {
			merged, err := mergeRow("", old, updated)
			if err != nil {
				return nil, err
			}
			out[k] = merged
		} else {
			if err := validateObjectArrayReplacement(out[k], v); err != nil {
				return nil, fmt.Errorf("field %s: %w", k, err)
			}
			out[k] = cloneValue(v)
		}
	}
	return out, nil
}

// With no declared nested key, guessing which object a partial replacement
// belongs to could move/drop newer fields. Refuse it instead of silently losing
// them. Scalar arrays and complete homogeneous object-array updates still work.
func validateObjectArrayReplacement(current, updated any) error {
	old, oldOK := objectSlice(current)
	next, newOK := objectSlice(updated)
	if !oldOK || !newOK {
		return nil
	}
	for _, value := range old {
		row, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if len(row) > 0 && len(next) == 0 {
			return fmt.Errorf("object-array replacement would discard stored fields")
		}
		// Without nested keys neither array position nor a different object's
		// field proves that a partial row preserved this object's unknown data.
		// Every replacement object must carry the complete stored field shape.
		for _, candidate := range next {
			r, ok := candidate.(map[string]any)
			if !ok {
				return fmt.Errorf("object-array replacement contains a non-object")
			}
			if err := validateNestedObjectFields(row, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func objectSlice(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case []Row:
		out := make([]any, len(v))
		for i, row := range v {
			out[i] = row
		}
		return out, true
	default:
		return nil, false
	}
}

func validateNestedObjectFields(current, updated any) error {
	old, oldOK := current.(map[string]any)
	next, newOK := updated.(map[string]any)
	if oldOK && newOK {
		for key, prior := range old {
			replacement, present := next[key]
			if !present {
				return fmt.Errorf("object-array replacement would discard stored field %s", key)
			}
			if err := validateNestedObjectFields(prior, replacement); err != nil {
				return err
			}
		}
		return nil
	}
	return validateObjectArrayReplacement(current, updated)
}

// HashTable computes the per-table hash for a row set.
func HashTable(rows []Row) (string, error) {
	if rows == nil {
		rows = []Row{}
	}
	return hashing.TableHash(rows)
}

// hashExcludedTables are write-only/ephemeral tables the real server accepts via
// push but never reports a hash for (absent from every confirm), plus
// server-computed tables that never carry a content hash
// (user_web_shop_product is served with an empty-string hash).
var hashExcludedTables = map[string]bool{
	"user_reward_group_log": true,
	"user_web_shop_product": true,
}

// HashExcluded reports whether a table is excluded from save-state hashing.
func HashExcluded(table string) bool { return hashExcludedTables[table] }
