package clientversion

import (
	"context"
	"sync"
	"testing"
	"time"
)

// memStore is a GlobalStore backed by memory, for exercising load/write-through.
type memStore struct {
	mu  sync.Mutex
	rep *Report
}

func (m *memStore) GetClientVersion(_ context.Context) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rep, nil
}

func (m *memStore) SetClientVersion(_ context.Context, r Report) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rep = &r
	return nil
}

func TestResolvePrefersClientOverGlobal(t *testing.T) {
	r := New(nil, nil)
	r.Report("c1", Report{ProgramVersion: "6.8.0", AssetVersion: "6.8.0", AssetHash: "aaa"})
	r.Report("c2", Report{ProgramVersion: "6.9.0", AssetVersion: "6.9.0", AssetHash: "bbb"})

	rep, ok := r.Resolve("c1")
	if !ok || rep.AssetHash != "aaa" {
		t.Fatalf("c1 resolve = %+v, %v; want aaa", rep, ok)
	}
	// The most recent report is the global fallback.
	rep, ok = r.Resolve("unknown")
	if !ok || rep.AssetHash != "bbb" {
		t.Fatalf("global resolve = %+v, %v; want bbb", rep, ok)
	}
}

func TestResolveEmptyRegistry(t *testing.T) {
	r := New(nil, nil)
	if _, ok := r.Resolve(""); ok {
		t.Fatal("empty registry must not resolve")
	}
	if _, ok := r.Resolve("nobody"); ok {
		t.Fatal("empty registry must not resolve")
	}
}

func TestReportIgnoredForEmptyClientID(t *testing.T) {
	r := New(nil, nil)
	r.Report("", Report{AssetVersion: "6.9.0", AssetHash: "x"})
	if _, ok := r.Resolve(""); ok {
		t.Fatal("empty clientID report must not create a global")
	}
}

func TestLoadsGlobalFromStore(t *testing.T) {
	store := &memStore{rep: &Report{AssetVersion: "6.7.0", AssetHash: "ccc"}}
	r := New(store, nil)
	rep, ok := r.Resolve("any")
	if !ok || rep.AssetHash != "ccc" {
		t.Fatalf("global from store = %+v, %v; want ccc", rep, ok)
	}
}

func TestReportWritesThroughToStore(t *testing.T) {
	store := &memStore{}
	r := New(store, nil)
	r.Report("c1", Report{ProgramVersion: "6.9.0", AssetVersion: "6.9.0", AssetHash: "ddd"})
	if store.rep == nil || store.rep.AssetHash != "ddd" {
		t.Fatalf("store not updated: %+v", store.rep)
	}
}

func TestExpiredClientReportsEvictedAndReplicaFallbackRefreshes(t *testing.T) {
	store := &memStore{rep: &Report{AssetHash: "old"}}
	r := New(store, nil)
	r.byClient["expired"] = Report{AssetHash: "expired"}
	r.seen["expired"] = time.Now().Add(-48 * time.Hour)
	r.Report("current", Report{AssetHash: "current"})
	if _, exists := r.byClient["expired"]; exists {
		t.Fatal("expired client retained")
	}
	store.SetClientVersion(context.Background(), Report{AssetHash: "other-replica"})
	rep, ok := r.Resolve("unknown")
	if !ok || rep.AssetHash != "other-replica" {
		t.Fatal("replica fallback stayed stale")
	}
}

type delayedStore struct {
	memStore
	getStarted chan struct{}
	getRelease chan struct{}
	setStarted chan struct{}
	setRelease chan struct{}
	setHash    string
	getOnce    sync.Once
	setOnce    sync.Once
}

func (s *delayedStore) GetClientVersion(ctx context.Context) (*Report, error) {
	rep, err := s.memStore.GetClientVersion(ctx)
	if s.getRelease != nil {
		s.getOnce.Do(func() { close(s.getStarted) })
		select {
		case <-s.getRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return rep, err
}

func (s *delayedStore) SetClientVersion(ctx context.Context, rep Report) error {
	if s.setRelease != nil && (s.setHash == "" || rep.AssetHash == s.setHash) {
		s.setOnce.Do(func() { close(s.setStarted) })
		select {
		case <-s.setRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.memStore.SetClientVersion(ctx, rep)
}

func TestSlowGlobalReadDoesNotBlockCachedClientsOrReplaceNewReport(t *testing.T) {
	r := New(nil, nil)
	r.Report("cached", Report{AssetHash: "cached"})
	s := &delayedStore{
		memStore:   memStore{rep: &Report{AssetHash: "old"}},
		getStarted: make(chan struct{}), getRelease: make(chan struct{}),
	}
	r.store = s
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(s.getRelease) }) })
	resolved := make(chan Report, 1)
	go func() { rep, _ := r.Resolve("new"); resolved <- rep }()
	<-s.getStarted

	cached := make(chan Report, 1)
	go func() { rep, _ := r.Resolve("cached"); cached <- rep }()
	select {
	case rep := <-cached:
		if rep.AssetHash != "cached" {
			t.Fatalf("cached report = %+v", rep)
		}
	case <-time.After(time.Second):
		t.Fatal("slow global read blocked a cached client")
	}
	r.Report("new", Report{AssetHash: "new"})
	r.Report("other", Report{AssetHash: "other"})
	release.Do(func() { close(s.getRelease) })
	if rep := <-resolved; rep.AssetHash != "new" {
		t.Fatalf("pending global read ignored newly matching client report: %+v", rep)
	}
	r.mu.Lock()
	global := *r.global
	r.mu.Unlock()
	if global.AssetHash != "other" {
		t.Fatalf("stale global read replaced new report: %+v", global)
	}
}

func TestSlowReportWriteDoesNotBlockResolutionAndPreservesWriteOrder(t *testing.T) {
	r := New(nil, nil)
	r.Report("cached", Report{AssetHash: "cached"})
	s := &delayedStore{
		memStore:   memStore{rep: &Report{AssetHash: "old"}},
		setStarted: make(chan struct{}), setRelease: make(chan struct{}),
		setHash: "first",
	}
	r.store = s
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(s.setRelease) }) })
	firstDone := make(chan struct{})
	go func() { r.Report("first", Report{AssetHash: "first"}); close(firstDone) }()
	<-s.setStarted
	resolved := make(chan Report, 2)
	go func() {
		rep, _ := r.Resolve("cached")
		resolved <- rep
		rep, _ = r.Resolve("unknown")
		resolved <- rep
	}()
	for _, want := range []string{"cached", "first"} {
		select {
		case rep := <-resolved:
			if rep.AssetHash != want {
				t.Fatalf("resolved %+v, want %s", rep, want)
			}
		case <-time.After(time.Second):
			t.Fatal("pending persistence blocked version resolution")
		}
	}
	secondDone := make(chan struct{})
	go func() { r.Report("second", Report{AssetHash: "second"}); close(secondDone) }()
	select {
	case <-secondDone:
		t.Fatal("newer durable write overtook the pending older report")
	case <-time.After(100 * time.Millisecond):
	}
	release.Do(func() { close(s.setRelease) })
	<-firstDone
	<-secondDone
	stored, _ := s.memStore.GetClientVersion(context.Background())
	if stored.AssetHash != "second" {
		t.Fatalf("new report overwritten in durable store: %+v", stored)
	}
}
