package deltacomm

import "testing"

func row(kv ...any) Row {
	r := Row{}
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i].(string)] = kv[i+1]
	}
	return r
}

func TestCollectionUpsertByKey(t *testing.T) {
	cur := []Row{row("_masterCardId", 1, "lvl", 1), row("_masterCardId", 2, "lvl", 1)}
	puts := []Row{row("_masterCardId", 1, "lvl", 5), row("_masterCardId", 3, "lvl", 1)}
	out, err := MergeTable("user_card", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 rows, got %d: %v", len(out), out)
	}
	if out[0]["lvl"] != 5 {
		t.Fatalf("card 1 not upserted: %v", out[0])
	}
	if out[2]["_masterCardId"] != 3 {
		t.Fatalf("card 3 not appended: %v", out[2])
	}
}

func TestCollectionDelete(t *testing.T) {
	cur := []Row{row("_masterCardId", 1), row("_masterCardId", 2), row("_masterCardId", 3)}
	out, err := MergeTable("user_card", cur, nil, []Row{row("_masterCardId", 2)}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0]["_masterCardId"] != 1 || out[1]["_masterCardId"] != 3 {
		t.Fatalf("delete wrong: %v", out)
	}
}

func TestCompositeKey(t *testing.T) {
	cur := []Row{row("_deckIndex", 0, "_slotIndex", 0, "v", "a")}
	puts := []Row{
		row("_deckIndex", 0, "_slotIndex", 0, "v", "b"), // upsert
		row("_deckIndex", 0, "_slotIndex", 1, "v", "c"), // new
	}
	out, err := MergeTable("user_slot", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0]["v"] != "b" {
		t.Fatalf("composite upsert wrong: %v", out)
	}
}

func TestSingletonWholeRowReplace(t *testing.T) {
	cur := []Row{row("_bundleVersion", "0.0.1")}
	out, err := MergeTable("user_version", cur, []Row{row("_bundleVersion", "1.1.0")}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["_bundleVersion"] != "1.1.0" {
		t.Fatalf("singleton replace wrong: %v", out)
	}
}

func TestReplaceItemsWins(t *testing.T) {
	cur := []Row{row("_masterCardId", 1), row("_masterCardId", 2)}
	repl := []Row{row("_masterCardId", 9)}
	out, err := MergeTable("user_card", cur, []Row{row("_masterCardId", 5)}, nil, repl, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["_masterCardId"] != 9 {
		t.Fatalf("replace must win: %v", out)
	}
}

func TestMergeDoesNotMutateInput(t *testing.T) {
	cur := []Row{row("_masterCardId", 1, "lvl", 1)}
	_, _ = MergeTable("user_card", cur, []Row{row("_masterCardId", 1, "lvl", 9)}, nil, nil, false)
	if cur[0]["lvl"] != 1 {
		t.Fatalf("input mutated: %v", cur[0])
	}
}

func TestTouchedTables(t *testing.T) {
	d := Deltas{
		PutItems:    map[string][]Row{"a": nil, "b": nil},
		DeleteItems: map[string][]Row{"b": nil, "c": nil},
	}
	if got := len(d.TouchedTables()); got != 3 {
		t.Fatalf("want 3 touched, got %d", got)
	}
}

func TestHashTableNilIsEmpty(t *testing.T) {
	h1, _ := HashTable(nil)
	h2, _ := HashTable([]Row{})
	if h1 != h2 || len(h1) != 32 {
		t.Fatalf("nil/empty hash mismatch: %q %q", h1, h2)
	}
}

// Regression: user_random (13 rows keyed by _lotteryType) was misclassified as
// a singleton, so a partial put of 1 row wiped all 12 siblings. This test
// verifies the tablekeys.json fix makes it a collection.
func TestUserRandomPartialPutPreservesSiblings(t *testing.T) {
	cur := make([]Row, 13)
	for i := 1; i <= 13; i++ {
		cur[i-1] = row("_lotteryType", int64(i), "_seed", int64(i*100))
	}
	// Client pushes only lottery type 5 (partial update).
	puts := []Row{row("_lotteryType", int64(5), "_seed", int64(999))}
	out, err := MergeTable("user_random", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 13 {
		t.Fatalf("partial put on user_random wiped siblings: got %d rows, want 13", len(out))
	}
	// Verify the updated row and that others survived unchanged.
	var found bool
	for _, r := range out {
		lt := r["_lotteryType"].(int64)
		if lt == 5 {
			found = true
			if r["_seed"].(int64) != 999 {
				t.Fatalf("type 5 not updated: %v", r)
			}
		} else if r["_seed"].(int64) != lt*100 {
			t.Fatalf("sibling type %d corrupted: %v", lt, r)
		}
	}
	if !found {
		t.Fatal("type 5 row missing after put")
	}
}

// Regression: user_currency ({_id, _num}) was misclassified as a singleton,
// so a partial put of one currency wiped all others.
func TestUserCurrencyPartialPutPreservesSiblings(t *testing.T) {
	cur := []Row{
		row("_id", int64(101000001), "_num", int64(5000)),
		row("_id", int64(101000002), "_num", int64(200)),
	}
	puts := []Row{row("_id", int64(101000001), "_num", int64(4700))}
	out, err := MergeTable("user_currency", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("partial currency put wiped siblings: got %d rows, want 2", len(out))
	}
	for _, r := range out {
		id := r["_id"].(int64)
		if id == 101000001 && r["_num"].(int64) != 4700 {
			t.Fatalf("currency 1 not updated: %v", r)
		}
		if id == 101000002 && r["_num"].(int64) != 200 {
			t.Fatalf("currency 2 corrupted: %v", r)
		}
	}
}

// Regression: user_world_state is keyed on (_index, _depth).
// Home-entry WorldScriptInitialized puts only _index=0; treating the table
// as a singleton would wipe the field/conquest resume rows.
func TestUserWorldStatePartialPutPreservesSiblings(t *testing.T) {
	cur := []Row{
		row("_index", int64(0), "_depth", int64(0), "_currentFieldLabel", "Jamaisvu"),
		row("_index", int64(1), "_depth", int64(0), "_currentFieldLabel", "Coastline_Port_Right"),
		row("_index", int64(3), "_depth", int64(0), "_currentFieldLabel", "Conquest_Desert_Wild_01"),
	}
	puts := []Row{row("_index", int64(0), "_depth", int64(0), "_currentFieldLabel", "Jamaisvu", "_rotationY", 45.0)}
	out, err := MergeTable("user_world_state", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("partial world_state put wiped siblings: got %d rows, want 3", len(out))
	}
	byIdx := map[int64]Row{}
	for _, r := range out {
		byIdx[r["_index"].(int64)] = r
	}
	if byIdx[0]["_rotationY"] != 45.0 {
		t.Fatalf("index 0 not updated: %v", byIdx[0])
	}
	if byIdx[1]["_currentFieldLabel"] != "Coastline_Port_Right" {
		t.Fatalf("index 1 wiped: %v", byIdx[1])
	}
	if byIdx[3]["_currentFieldLabel"] != "Conquest_Desert_Wild_01" {
		t.Fatalf("index 3 wiped: %v", byIdx[3])
	}
}

func TestUserClientGiftPartialPutPreservesSiblings(t *testing.T) {
	cur := []Row{
		row("_giftId", int64(1), "_isReceived", false),
		row("_giftId", int64(2), "_isReceived", false),
	}
	puts := []Row{row("_giftId", int64(1), "_isReceived", true)}
	out, err := MergeTable("user_client_gift", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("partial client_gift put wiped siblings: got %d rows, want 2", len(out))
	}
}

// createUserSkill(long) -> UserSkill.ctor(_masterSkillId). A partial skill put
// must not wipe the rest of the list.
func TestUserSkillPartialPutPreservesSiblings(t *testing.T) {
	cur := []Row{
		row("_masterSkillId", int64(1), "_enableOnAuto", true),
		row("_masterSkillId", int64(2), "_enableOnAuto", true),
		row("_masterSkillId", int64(3), "_enableOnAuto", false),
	}
	puts := []Row{row("_masterSkillId", int64(2), "_enableOnAuto", false)}
	out, err := MergeTable("user_skill", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("partial skill put wiped siblings: got %d rows, want 3", len(out))
	}
}

func TestFieldEnemyPartialPutPreservesSiblings(t *testing.T) {
	cur := []Row{
		row("_worldStateKey", int64(0), "_masterFieldEnemyId", int64(10), "_state", int64(1)),
		row("_worldStateKey", int64(0), "_masterFieldEnemyId", int64(11), "_state", int64(1)),
		row("_worldStateKey", int64(1), "_masterFieldEnemyId", int64(10), "_state", int64(0)),
	}
	puts := []Row{row("_worldStateKey", int64(0), "_masterFieldEnemyId", int64(10), "_state", int64(2))}
	out, err := MergeTable("user_field_enemy", cur, puts, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("partial field_enemy put wiped siblings: got %d rows, want 3", len(out))
	}
}
