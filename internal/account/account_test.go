package account

import (
	"context"
	"testing"
)

func TestMemoryStableAndIncremental(t *testing.T) {
	m := NewMemory(0)
	a1, _ := m.GetOrCreate(context.Background(), "xuidA")
	a2, _ := m.GetOrCreate(context.Background(), "xuidB")
	a1again, _ := m.GetOrCreate(context.Background(), "xuidA")

	if a1.UserID != 1 || a2.UserID != 2 {
		t.Fatalf("expected incremental ids 1,2 got %d,%d", a1.UserID, a2.UserID)
	}
	if a1again.UserID != a1.UserID {
		t.Fatalf("same xuid must map to same userId: %d vs %d", a1again.UserID, a1.UserID)
	}
	if a1again.XUID != "xuidA" {
		t.Fatalf("xuid lost: %q", a1again.XUID)
	}
}

func TestMemoryBase(t *testing.T) {
	m := NewMemory(1000)
	a, _ := m.GetOrCreate(context.Background(), "x")
	if a.UserID != 1001 {
		t.Fatalf("base offset wrong: %d", a.UserID)
	}
}
