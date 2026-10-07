package portal

import (
	"os"
	"testing"
)

// Optional interop check against a supplied save archive. Point
// LILYPAD_TEST_EXPORT at a lilypad-export/1 file to enable (skipped otherwise);
// the archive is a live player save and is never committed as a fixture.
func TestRealExportInterop(t *testing.T) {
	path := os.Getenv("LILYPAD_TEST_EXPORT")
	if path == "" {
		t.Skip("LILYPAD_TEST_EXPORT not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip("real export not present:", err)
	}
	tables, extras, err := parseArchive(raw)
	if err != nil {
		t.Fatalf("parseArchive rejected real export: %v", err)
	}
	rows := 0
	for _, tb := range tables {
		rows += len(tb)
	}
	t.Logf("tables=%d rows=%d", len(tables), rows)
	if len(tables) == 0 || rows == 0 {
		t.Fatal("empty archive accepted")
	}
	free, paid, ok := paymentBalanceFrom(extras)
	t.Logf("quartz: free=%d paid=%d ok=%v", free, paid, ok)
}
