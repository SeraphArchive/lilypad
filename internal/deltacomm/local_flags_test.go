package deltacomm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorldLocalFlagsClearOnFieldTransition(t *testing.T) {
	current := []Row{
		{"_index": 1, "_depth": 0, "_currentFieldLabel": "old-field", "_futureWorldField": 99,
			"_localFlag": []any{Row{"_id": "old-local", "_value": 1}}},
		{"_index": 1, "_depth": 1, "_localFlag": []any{Row{"_id": "other-world", "_value": 2}}},
	}
	put := []Row{{"_index": 1, "_depth": 0, "_currentFieldLabel": "new-field", "_localFlag": []any{}}}
	out, err := MergeTable("user_world_state", current, put, nil, nil, false)
	if err != nil {
		t.Fatal("valid field transition rejected", err)
	}
	if len(out) != 2 || len(out[0]["_localFlag"].([]any)) != 0 || out[0]["_currentFieldLabel"] != "new-field" || out[0]["_futureWorldField"] != 99 || !reflect.DeepEqual(out[1], current[1]) {
		t.Fatal("field transition did not clear only the intended local flags", out)
	}
	if len(current[0]["_localFlag"].([]any)) != 1 {
		t.Fatal("merge changed its input")
	}
}

func TestWorldLocalFlagsReplaceByIdentityAndPreserveUnknownFields(t *testing.T) {
	current := []Row{{"_index": 1, "_depth": 0, "_localFlag": []Row{
		{"_id": "removed", "_value": 1},
		{"_id": "retained", "_value": 2, "_future": map[string]any{"progress": 99}},
	}}}
	put := []Row{{"_index": 1, "_depth": 0, "_localFlag": []Row{
		{"_id": "new", "_value": 3},
		{"_id": "retained", "_value": 4},
	}}}
	out, err := MergeTable("user_world_state", current, put, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	flags := out[0]["_localFlag"].([]any)
	if len(flags) != 2 || flags[0].(Row)["_id"] != "new" || flags[1].(Row)["_value"] != 4 || flags[1].(Row)["_future"].(map[string]any)["progress"] != 99 {
		t.Fatal("flag identities or unknown data were lost", flags)
	}
	flags[1].(Row)["_future"].(map[string]any)["progress"] = 100
	if current[0]["_localFlag"].([]Row)[1]["_future"].(map[string]any)["progress"] != 99 {
		t.Fatal("merged flags alias the stored input")
	}
}

func TestWorldLocalFlagsCannotDiscardUnknownFlagData(t *testing.T) {
	current := []Row{{"_index": 1, "_depth": 0, "_localFlag": []Row{{"_id": "future-local", "_value": 1, "_future": 99}}}}
	for _, replacement := range [][]Row{{}, {{"_id": "different", "_value": 2}}} {
		if _, err := MergeTable("user_world_state", current, []Row{{"_index": 1, "_depth": 0, "_localFlag": replacement}}, nil, nil, false); err == nil {
			t.Fatal("flag replacement discarded unsupported stored data")
		}
	}
}

func TestWorldLocalFlagsRejectMalformedSnapshots(t *testing.T) {
	for _, flags := range []any{
		nil, "not-an-array", []any{1},
		[]Row{{"_value": 1}}, []Row{{"_id": "local"}},
		[]Row{{"_id": 1, "_value": 1}}, []Row{{"_id": "local", "_value": false}},
		[]Row{{"_id": "local", "_value": json.Number("2147483648")}},
		[]Row{{"_id": "local", "_value": 1}, {"_id": "local", "_value": 2}},
	} {
		put := []Row{{"_index": 1, "_depth": 0, "_localFlag": flags}}
		if _, err := MergeTable("user_world_state", nil, put, nil, nil, false); err == nil {
			t.Fatalf("malformed flag snapshot accepted: %#v", flags)
		}
	}
}

func TestLocalFlagSemanticsAreLimitedToWorldStateField(t *testing.T) {
	for _, test := range []struct {
		table    string
		cur, put Row
	}{
		{"user_profile", Row{"_localFlag": []Row{{"_id": "local", "_value": 1}}}, Row{"_localFlag": []Row{}}},
		{"user_world_state", Row{"_index": 1, "_depth": 0, "nested": Row{"_localFlag": []Row{{"_id": "local", "_value": 1}}}}, Row{"_index": 1, "_depth": 0, "nested": Row{"_localFlag": []Row{}}}},
	} {
		if _, err := MergeTable(test.table, []Row{test.cur}, []Row{test.put}, nil, nil, false); err == nil {
			t.Fatal("unrelated object-array protection was bypassed", test.table)
		}
	}
}
