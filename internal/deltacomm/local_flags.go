package deltacomm

import (
	"fmt"
	"strconv"

	"lilypad/internal/hashing"
)

// UserWorldState serializes _localFlag as a complete snapshot keyed by the
// string _id. An explicit empty list clears it when the client resets a world.
func worldLocalFlags(value any) ([]Row, error) {
	values, ok := objectSlice(value)
	if !ok {
		return nil, fmt.Errorf("local flags must be an array")
	}
	rows := make([]Row, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("local flag is not an object")
		}
		id, ok := row["_id"].(string)
		if !ok || id == "" || seen[id] {
			return nil, fmt.Errorf("local flag has a missing or duplicate string _id")
		}
		// _value is the client's IntScrambler value, an exact signed Int32.
		raw, err := hashing.CanonicalJSON(row["_value"])
		if err != nil {
			return nil, fmt.Errorf("local flag has an invalid _value")
		}
		if _, err := strconv.ParseInt(string(raw), 10, 32); err != nil {
			return nil, fmt.Errorf("local flag _value must be an Int32")
		}
		seen[id] = true
		rows = append(rows, row)
	}
	return rows, nil
}

func mergeWorldLocalFlags(current, updated any) ([]any, error) {
	var old []Row
	if current != nil {
		var err error
		old, err = worldLocalFlags(current)
		if err != nil {
			return nil, err
		}
	}
	next, err := worldLocalFlags(updated)
	if err != nil {
		return nil, err
	}
	prior := make(map[string]Row, len(old))
	for _, row := range old {
		prior[row["_id"].(string)] = row
	}
	out := make([]any, 0, len(next))
	for _, row := range next {
		id := row["_id"].(string)
		merged, err := mergeRow("", prior[id], row)
		if err != nil {
			return nil, err
		}
		out = append(out, merged)
		delete(prior, id)
	}
	// Known flags may be removed, but an older client's snapshot must not
	// discard unsupported fields attached to a flag it no longer includes.
	for _, row := range prior {
		for field := range row {
			if field != "_id" && field != "_value" {
				return nil, fmt.Errorf("local flag removal would discard stored field %s", field)
			}
		}
	}
	return out, nil
}
