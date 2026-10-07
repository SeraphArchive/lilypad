package portal

import (
	"encoding/base64"
	"strings"
	"testing"
)

// clientMigrationForm mirrors the real native GameLib transport for
// migration_password: ROT13(reverse(base64(utf8(pw)))). Used to drive
// ResolveMigration in tests.
func clientMigrationForm(pw string) string {
	return rot13(reverseString(base64.StdEncoding.EncodeToString([]byte(pw))))
}

// TestMigrationPasswordTransport uses synthetic known-answer vectors for the
// native wire transform and checks the Argon2 round-trip.
func TestMigrationPasswordTransport(t *testing.T) {
	// Synthetic wire -> plaintext, via ROT13 + reverse + base64.
	cases := map[string]string{
		"==jZlRmpmSTHwyTqyuTqhy3H": "SyntheticPass123",
		"==tA1DQqyW3LyAIMfOKouuKE": "ExampleSecret456",
	}
	for wire, want := range cases {
		got, err := DecodeMigrationPassword(wire)
		if err != nil {
			t.Fatalf("decode %q: %v", wire, err)
		}
		if got != want {
			t.Fatalf("decode %q = %q, want %q", wire, got, want)
		}
		if enc := clientMigrationForm(want); enc != wire {
			t.Fatalf("encode %q = %q, want %q", want, enc, wire)
		}
	}

	// argon2 round-trip: a stored hash of the plaintext verifies the encoded wire.
	pw := "ExamplePassw0rd1"
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyMigrationPassword(h, clientMigrationForm(pw)); !ok {
		t.Fatal("round-trip verify should succeed")
	}
	if ok, _ := VerifyMigrationPassword(h, clientMigrationForm("other")); ok {
		t.Fatal("verify must reject a different password")
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("not argon2id PHC: %s", h)
	}
	ok, err := VerifyPassword(h, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify should succeed: ok=%v err=%v", ok, err)
	}
	bad, err := VerifyPassword(h, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if bad {
		t.Fatal("verify must fail for wrong password")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	if _, err := VerifyPassword("not-a-hash", "x"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}

func TestGeneratorsShapeAndUniqueness(t *testing.T) {
	if x := GenerateXUID(); len(x) != 32 {
		t.Fatalf("xuid len %d", len(x))
	}
	if c := GenerateMigrationCode(); len(c) != 16 {
		t.Fatalf("code len %d", len(c))
	}
	if GenerateXUID() == GenerateXUID() {
		t.Fatal("xuids must be unique")
	}
	if GenerateMigrationCode() == GenerateMigrationCode() {
		t.Fatal("codes must be unique")
	}
	// migration code uses the unambiguous alphabet
	for _, ch := range GenerateMigrationCode() {
		if !strings.ContainsRune(migrationAlphabet, ch) {
			t.Fatalf("code char %q not in alphabet", ch)
		}
	}
}
