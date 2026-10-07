package deltacomm

import "testing"

func TestUnknownTableCannotReplaceExistingRows(t *testing.T) {
	_, err := MergeTable("user_future_progress", []Row{{"_futureId": 1}, {"_futureId": 2}}, []Row{{"_futureId": 1}}, nil, nil, false)
	if err == nil {
		t.Fatal("unknown table silently replaced existing rows")
	}
}
func TestPartialRowPreservesFieldsUnknownToOlderClient(t *testing.T) {
	cur := []Row{{"_masterStoryId": 1, "_isClear": true, "_futureProgress": 99, "nested": map[string]any{"old": 1, "future": 2}}}
	puts := []Row{{"_masterStoryId": 1, "nested": map[string]any{"old": 3}}}
	out, err := MergeTable("user_story", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["_futureProgress"] != 99 || out[0]["_isClear"] != true || out[0]["nested"].(map[string]any)["future"] != 2 {
		t.Fatal("older client erased newer/external fields", out)
	}
}
func TestSortFilterPartialUpdatePreservesOtherTypes(t *testing.T) {
	out, err := MergeTable("user_sort_filter", []Row{{"_type": 1}, {"_type": 2}}, []Row{{"_type": 1}}, nil, nil, false)
	if err != nil || len(out) != 2 {
		t.Fatal("partial update erased other filter settings", out, err)
	}
}
func TestMalformedSingletonAndAmbiguousDeleteDoNotMutate(t *testing.T) {
	for _, test := range []struct {
		table              string
		cur, puts, deletes []Row
	}{
		{"user_profile", []Row{{"_userName": "player"}}, []Row{{}, {}}, nil},
		{"user_profile", []Row{{"_userName": "player"}}, []Row{{"_userName": "new"}}, []Row{{}}},
		{"user_story", []Row{{"_masterStoryId": 1}}, []Row{{"_masterStoryId": 1}}, []Row{{"_masterStoryId": 1}}},
	} {
		if _, err := MergeTable(test.table, test.cur, test.puts, test.deletes, nil, false); err == nil {
			t.Fatal("destructive ambiguous mutation accepted", test.table)
		}
	}
}

func TestNewCollectionRowDoesNotAliasInput(t *testing.T) {
	put := []Row{{"_masterStoryId": 1, "nested": map[string]any{"progress": 2}}}
	out, err := MergeTable("user_story", nil, put, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	out[0]["nested"].(map[string]any)["progress"] = 3
	if put[0]["nested"].(map[string]any)["progress"] != 2 {
		t.Fatal("merged new row aliases the request")
	}
}

func TestPartialObjectArrayCannotEraseNewerFields(t *testing.T) {
	cur := []Row{{"children": []any{map[string]any{"_id": 1, "future": 99, "score": 1}}}}
	put := []Row{{"children": []any{map[string]any{"_id": 1, "score": 2}}}}
	if _, err := MergeTable("user_profile", cur, put, nil, nil, false); err == nil {
		t.Fatal("partial object array erased externally added fields")
	}
	if cur[0]["children"].([]any)[0].(map[string]any)["future"] != 99 {
		t.Fatal("input was changed on rejection")
	}
	cur = []Row{{"children": []Row{{"_id": 1, "nested": map[string]any{"future": 99, "score": 1}}}}}
	put = []Row{{"children": []Row{{"_id": 1, "nested": map[string]any{"score": 2}}}}}
	if _, err := MergeTable("user_profile", cur, put, nil, nil, false); err == nil {
		t.Fatal("nested omission inside a typed object array was accepted")
	}
}

func TestObjectArrayFieldsCannotBeBorrowedFromAnotherRow(t *testing.T) {
	cur := []Row{{"children": []Row{{"id": 1, "future": 99}, {"id": 2}}}}
	for _, children := range [][]Row{
		{{"id": 1}, {"id": 2}, {"id": 3, "future": 0}},
		{{"id": 2, "future": 0}, {"id": 1}},
	} {
		if _, err := MergeTable("user_profile", cur, []Row{{"children": children}}, nil, nil, false); err == nil {
			t.Fatal("another object's field concealed a partial replacement", children)
		}
	}
	complete := []Row{{"children": []Row{{"id": 1, "future": 100}, {"id": 2, "future": 0}, {"id": 3, "future": 0}}}}
	if _, err := MergeTable("user_profile", cur, complete, nil, nil, false); err != nil {
		t.Fatal("complete object shapes were rejected", err)
	}
}
