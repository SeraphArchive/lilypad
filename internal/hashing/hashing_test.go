package hashing

import (
	"encoding/json"
	"testing"
)

func TestCanonicalKeyOrderStable(t *testing.T) {
	a, _ := CanonicalJSON(map[string]any{"b": 1, "a": 2})
	b, _ := CanonicalJSON(map[string]any{"a": 2, "b": 1})
	if string(a) != string(b) || string(a) != `{"a":2,"b":1}` {
		t.Fatalf("non-canonical: %s vs %s", a, b)
	}
}

func TestCanonicalNested(t *testing.T) {
	got, _ := CanonicalJSON(map[string]any{
		"z": []any{map[string]any{"y": 1, "x": 2}},
		"a": "s",
	})
	if string(got) != `{"a":"s","z":[{"x":2,"y":1}]}` {
		t.Fatalf("nested canonical wrong: %s", got)
	}
}

func TestTableHashDeterministic(t *testing.T) {
	h1, _ := TableHash([]any{map[string]any{"_id": 1, "x": "y"}})
	h2, _ := TableHash([]any{map[string]any{"x": "y", "_id": 1}})
	if h1 != h2 || len(h1) != 32 {
		t.Fatalf("hash not deterministic/32-hex: %q %q", h1, h2)
	}
}

func TestEmptyTableHash(t *testing.T) {
	if h, _ := TableHash([]any{}); len(h) != 32 {
		t.Fatalf("empty hash len %d", len(h))
	}
}

// json.Number (what codec.Decode produces) must hash identically to the same
// integer value, so hashes are stable across the pull/push/store round-trip.
func TestTableHashJSONNumberStable(t *testing.T) {
	hNum, _ := TableHash([]any{map[string]any{"_id": json.Number("123")}})
	hInt, _ := TableHash([]any{map[string]any{"_id": 123}})
	if hNum != hInt {
		t.Fatalf("json.Number vs int hash differ: %s != %s", hNum, hInt)
	}
}

func TestNumbersNormalizeWithoutLosingPrecision(t *testing.T) {
	for _, pair := range [][2]string{{"1e3", "1000"}, {"1.000", "1"}, {"-0.00", "0"}, {"1.2e-3", "0.0012"}, {"9007199254740993e0", "9007199254740993"}} {
		a, err := TableHash([]map[string]any{{"n": json.Number(pair[0])}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := TableHash([]map[string]any{{"n": json.Number(pair[1])}})
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Errorf("hash differs: %s vs %s", pair[0], pair[1])
		}
	}
}

func TestHugeExponentCannotAmplifySmallInput(t *testing.T) {
	if _, err := TableHash([]map[string]any{{"n": json.Number("1e10000")}}); err == nil {
		t.Fatal("unbounded exponent accepted")
	}
}
