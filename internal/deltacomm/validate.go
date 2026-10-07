package deltacomm

import (
	"fmt"
	"lilypad/internal/model"
)

// ValidateRows checks identities rather than assuming a missing key means null.
// Unknown mutation semantics fail closed rather than replacing a whole table.
func ValidateRows(table string, rows []Row) error {
	spec := model.SpecFor(table)
	if spec.Kind == model.Unknown {
		return fmt.Errorf("unsupported save table %s", table)
	}
	if spec.Kind == model.Singleton && len(rows) > 1 {
		return fmt.Errorf("%s singleton contains multiple rows", table)
	}
	seen := map[string]bool{}
	for i, row := range rows {
		if row == nil {
			return fmt.Errorf("%s row %d is not an object", table, i)
		}
		if table == "user_world_state" {
			if flags, present := row["_localFlag"]; present {
				if _, err := worldLocalFlags(flags); err != nil {
					return fmt.Errorf("%s row %d field _localFlag: %w", table, i, err)
				}
			}
		}
		if spec.Kind != model.Collection {
			continue
		}
		for _, field := range spec.Key {
			if v, ok := row[field]; !ok || v == nil {
				return fmt.Errorf("%s row %d missing key %s", table, i, field)
			}
		}
		key, err := keyOf(row, spec.Key)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("%s duplicate row identity", table)
		}
		seen[key] = true
	}
	return nil
}

// Archives retain opaque tables from newer clients. Their contents can be
// exported unchanged, but an unsupported incremental mutation is refused.
func ValidateArchiveRows(table string, rows []Row) error {
	if model.SpecFor(table).Kind != model.Unknown {
		return ValidateRows(table, rows)
	}
	for i, row := range rows {
		if row == nil {
			return fmt.Errorf("%s row %d is not an object", table, i)
		}
	}
	return nil
}
