// Package account resolves the durable platform XUID to a game userId. The
// HTTP layer depends only on Provider; the in-memory implementation here backs
// Phase-2 / test builds, while the Postgres store implements the same contract
// for production.
package account

import (
	"context"
	"sync"
)

// Account is the game-side identity for a platform XUID.
type Account struct {
	XUID   string
	UserID int64
}

// Provider maps a platform XUID to a stable game userId, creating one on first
// contact (the user/client/id contract).
type Provider interface {
	GetOrCreate(ctx context.Context, xuid string) (Account, error)
}

// Memory is a thread-safe in-memory Provider assigning incremental userIds.
// Intended for tests and verify-disabled local runs; not durable.
type Memory struct {
	mu   sync.Mutex
	next int64
	byID map[string]Account
}

// NewMemory returns an empty in-memory provider. Base is the first userId-1
// (so the first account gets base+1); pass 0 to start at 1.
func NewMemory(base int64) *Memory {
	return &Memory{next: base, byID: map[string]Account{}}
}

func (m *Memory) GetOrCreate(_ context.Context, xuid string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byID[xuid]; ok {
		return a, nil
	}
	m.next++
	a := Account{XUID: xuid, UserID: m.next}
	m.byID[xuid] = a
	return a, nil
}
