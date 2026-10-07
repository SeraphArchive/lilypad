package codec

import "testing"

// KAT vectors computed from the verified reference implementation.
func TestScrambleKAT(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello world", "ecdc2aa5caebaaea24a5d3"},
	}
	for _, c := range cases {
		if got := hexs(Scramble([]byte(c.in))); got != c.want {
			t.Fatalf("Scramble(%q)=%s want %s", c.in, got, c.want)
		}
	}
}

func TestScrambleBinaryKAT(t *testing.T) {
	in := make([]byte, 20)
	for i := range in {
		in[i] = byte(i)
	}
	if got := hexs(Scramble(in)); got != "84d048a8aca898b004e0f848fce8c840a430a828" {
		t.Fatalf("binary KAT mismatch: %s", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []string{"", "a", "hello world", "日本語テスト"} {
		if got := string(Unscramble(Scramble([]byte(s)))); got != s {
			t.Fatalf("roundtrip %q -> %q", s, got)
		}
	}
}

func TestScrambleDoesNotMutateInput(t *testing.T) {
	in := []byte{1, 2, 3, 4, 5}
	cp := append([]byte(nil), in...)
	_ = Scramble(in)
	for i := range in {
		if in[i] != cp[i] {
			t.Fatalf("Scramble mutated input at %d", i)
		}
	}
}
