package httpapi

import (
	"context"
	"log/slog"
	"testing"

	"lilypad/internal/account"
	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
)

// fakeStore is a minimal store.Store for resolvePlayer tests: it tracks
// XUID->userId creation and userId existence; all other methods are no-ops.
type fakeStore struct {
	byXUID  map[string]int64
	exists  map[int64]bool
	nextID  int64
	created []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{byXUID: map[string]int64{}, exists: map[int64]bool{}, nextID: 1000000}
}

func (f *fakeStore) GetOrCreate(_ context.Context, xuid string) (account.Account, error) {
	if id, ok := f.byXUID[xuid]; ok {
		return account.Account{XUID: xuid, UserID: id}, nil
	}
	f.nextID++
	f.byXUID[xuid] = f.nextID
	f.exists[f.nextID] = true
	f.created = append(f.created, xuid)
	return account.Account{XUID: xuid, UserID: f.nextID}, nil
}
func (f *fakeStore) UserExists(_ context.Context, userID int64) (bool, error) {
	return f.exists[userID], nil
}
func (f *fakeStore) AllHashes(context.Context, int64) (map[string]string, error) { return nil, nil }
func (f *fakeStore) GetTables(context.Context, int64, []string) (map[string][]store.Row, error) {
	return nil, nil
}
func (f *fakeStore) ApplyDeltas(context.Context, int64, deltacomm.Deltas, map[string]string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeStore) MutateUnderLock(context.Context, int64, []string, func(map[string][]store.Row) (deltacomm.Deltas, error)) (map[string]string, error) {
	return nil, nil
}
func (f *fakeStore) MutateWithGems(_ context.Context, _ int64, _ []string, fn func(map[string][]store.Row) (deltacomm.Deltas, store.GemChange, error)) (map[string]string, error) {
	if fn != nil {
		_, _, err := fn(map[string][]store.Row{})
		return map[string]string{}, err
	}
	return nil, nil
}
func (f *fakeStore) SeedNewPlayer(context.Context, int64, map[string][]store.Row) error { return nil }
func (f *fakeStore) ImportTables(context.Context, int64, map[string][]store.Row) error  { return nil }
func (f *fakeStore) HasState(context.Context, int64) (bool, error)                      { return false, nil }
func (f *fakeStore) Close()                                                             {}

// TestResolvePlayerNoPhantom pins the identity-resolution fix: after first contact
// mints an account from the platform XUID, the client sends the numeric userId as
// x-player-id, which must resolve to the SAME account (not mint a phantom).
func TestResolvePlayerNoPhantom(t *testing.T) {
	f := newFakeStore()
	s := &Server{accounts: f, store: f, log: slog.Default()}
	ctx := context.Background()

	// First contact via the hex platform XUID -> creates one account.
	uid, ok := s.resolvePlayer(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
	if !ok || uid != 1000001 {
		t.Fatalf("xuid first-contact: uid=%d ok=%v", uid, ok)
	}
	// The client now sends the numeric userId as x-player-id -> SAME account, no new create.
	uid2, ok := s.resolvePlayer(ctx, "1000001")
	if !ok || uid2 != 1000001 {
		t.Fatalf("numeric userId resolve: uid=%d ok=%v", uid2, ok)
	}
	if len(f.created) != 1 {
		t.Fatalf("phantom account minted: created=%v", f.created)
	}
	// A brand-new numeric token that matches no account must NOT be created
	// as an XUID — that is the data-loss phantom.
	if uid3, ok := s.resolvePlayer(ctx, "777"); ok {
		t.Fatalf("unknown numeric token must not mint: uid=%d created=%v", uid3, f.created)
	}
	if len(f.created) != 1 {
		t.Fatalf("unknown numeric minted a phantom: created=%v", f.created)
	}
	// Empty id fails.
	if _, ok := s.resolvePlayer(ctx, ""); ok {
		t.Fatal("empty id should fail")
	}
}

func TestResolvePlayerUnknownNumericDoesNotMint(t *testing.T) {
	f := newFakeStore()
	s := &Server{accounts: f, store: f, log: slog.Default()}
	// Leftover decimal userId that matches no account.
	if _, ok := s.resolvePlayer(context.Background(), "9990000000000001"); ok {
		t.Fatal("unknown numeric userId must not mint")
	}
	if len(f.created) != 0 {
		t.Fatalf("created=%v", f.created)
	}
	// Hex XUID still creates on first contact.
	uid, ok := s.resolvePlayer(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
	if !ok || uid != 1000001 {
		t.Fatalf("hex first-contact: uid=%d ok=%v", uid, ok)
	}
}
