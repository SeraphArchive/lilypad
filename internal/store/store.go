// Package store defines the persistence contract for LilyPad. Handlers depend
// only on Store; the Postgres implementation lives in store/postgres. All
// mutating operations are atomic and serialized per player.
package store

import (
	"context"
	"errors"

	"lilypad/internal/account"
	"lilypad/internal/deltacomm"
)

// ErrConcurrency is returned by ApplyDeltas when a push baseline does not match
// the stored hash (optimistic-concurrency conflict / save-lock contention).
var ErrConcurrency = errors.New("save-lock concurrency conflict")

// Row is a single user-table row (alias of the delta-merge row type).
type Row = deltacomm.Row

// GemChange is applied inside MutateWithGems' transaction. Consume runs before
// Add. A zero value is a no-op.
type GemChange struct {
	Add     int64
	Consume int64
}

// Store is the full persistence surface used by the game API.
type Store interface {
	// GetOrCreate resolves a platform XUID to a stable game account, creating
	// one (and seeding initial state) on first contact. Implements
	// account.Provider.
	GetOrCreate(ctx context.Context, xuid string) (account.Account, error)

	// UserExists reports whether an account with the given game userID exists.
	// Used to resolve a numeric x-player-id (the game userId the client sends
	// after login) to its account, rather than minting a phantom XUID-keyed one.
	UserExists(ctx context.Context, userID int64) (bool, error)

	// AllHashes returns every stored table hash for a player (user/confirm).
	AllHashes(ctx context.Context, userID int64) (map[string]string, error)

	// GetTables returns the current rows for the named tables (user/pull).
	// Tables with no stored state come back as empty slices.
	GetTables(ctx context.Context, userID int64, names []string) (map[string][]Row, error)

	// ApplyDeltas applies a mutation set atomically under the per-player lock,
	// stores touched tables' versions and returns them. nil baselines are for
	// trusted server mutations only; client pushes must use ClientSaveStore.
	ApplyDeltas(ctx context.Context, userID int64, d deltacomm.Deltas, baselines map[string]string) (map[string]string, error)

	// MutateUnderLock locks the player, loads readTables, lets fn compute a
	// delta set from the current rows, applies it atomically, and returns the
	// changed table hashes. This is the read-modify-write path for
	// server-authoritative RPCs whose output depends on current state.
	MutateUnderLock(ctx context.Context, userID int64, readTables []string, fn func(current map[string][]Row) (deltacomm.Deltas, error)) (map[string]string, error)

	// MutateWithGems is MutateUnderLock plus a gem add/consume applied in the
	// same transaction, so a crash cannot debit quartz without granting rows
	// (or mark a gift received without crediting quartz).
	MutateWithGems(ctx context.Context, userID int64, readTables []string, fn func(current map[string][]Row) (deltacomm.Deltas, GemChange, error)) (map[string]string, error)

	// SeedNewPlayer installs a brand-new player's initial table state.
	SeedNewPlayer(ctx context.Context, userID int64, seed map[string][]Row) error

	// ImportTables replaces a player's save with a full-state snapshot
	// (lilypad-export/1, e.g. from the official-server exporter) under the
	// per-player lock: every stored table is deleted, then every payload
	// table is inserted with a freshly computed hash. Tables absent from
	// the payload do not survive — the snapshot is the whole save.
	ImportTables(ctx context.Context, userID int64, tables map[string][]Row) error

	// HasState reports whether a player has any stored tables yet.
	HasState(ctx context.Context, userID int64) (bool, error)

	// Close releases pooled resources.
	Close()
}

// Ensure the contract satisfies account.Provider.
var _ account.Provider = (Store)(nil)

// ClientSaveStore validates both table versions and the queued packet's creation
// time. DCC may rebuild its hashes after a pull while keeping an old packet.
type ClientSaveStore interface {
	ApplyClientDeltas(context.Context, int64, deltacomm.Deltas, map[string]string, int64) (map[string]string, error)
}

// RejectedSaveStore preserves a refused packet for recovery without applying it.
type RejectedSaveStore interface {
	RecordRejectedSave(context.Context, int64, any) error
}

// SessionStore is the optional concurrent-login invalidation surface,
// discovered by type assertion (like gem.Store). RequestStore provides process
// identities and generation fencing; a message counter cannot grant access.
type SessionStore interface {
	// SessionBase returns the account's current session base (0 = none set).
	SessionBase(ctx context.Context, userID int64) (int64, error)
	// SetSessionBase raises the account's session base to base when base is
	// higher than what is stored. A lower value is ignored, so a stale login
	// replay cannot reopen a session a newer login invalidated.
	SetSessionBase(ctx context.Context, userID int64, base int64) error
	// InvalidateSessions revokes the current session and advances its generation.
	// Counter values cannot reopen it; the client must perform a login.
	InvalidateSessions(ctx context.Context, userID int64) error
}
