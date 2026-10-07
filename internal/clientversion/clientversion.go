// Package clientversion holds the installed-build version that clientpatch
// reports at game launch, and resolves it back to /api/app/start. It is the
// server-side counterpart of the clientpatch [report] module: a reported
// build's assetVersion/assetHash are echoed on app/start, so LilyPad's config
// does not need hand-editing on every game patch (the cause of the launch
// black screen when the config's version goes stale).
package clientversion

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Report is one installed build's version triple, as reported by clientpatch.
// ProgramVersion is informational (the game already sends x-client-version on
// app/start); AssetVersion/AssetHash are what the client validates.
type Report struct {
	ProgramVersion string `json:"programVersion"`
	AssetVersion   string `json:"assetVersion"`
	AssetHash      string `json:"assetHash"`
}

// GlobalStore is the durable store for the "latest seen" report, used as the
// app/start fallback for clients that carry no x-clientpatch-id header (a
// proxy, or a client without clientpatch). store/postgres implements it; a nil
// store keeps the global in memory only (dev mode).
type GlobalStore interface {
	GetClientVersion(ctx context.Context) (*Report, error)
	SetClientVersion(ctx context.Context, r Report) error
}

// Registry resolves a request's installed build. Per-client reports live in
// memory (re-reported on every launch); the global "latest seen" is read from
// and written through to a GlobalStore when one is configured, so the fallback
// survives a server restart.
type Registry struct {
	mu         sync.Mutex
	reportMu   sync.Mutex // keep durable writes in report order without locking readers
	byClient   map[string]Report
	seen       map[string]time.Time
	global     *Report
	revision   uint64
	persisting bool
	store      GlobalStore
	log        *slog.Logger
}

// New returns a Registry, loading the durable global from store (if present).
func New(store GlobalStore, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	r := &Registry{byClient: map[string]Report{}, seen: map[string]time.Time{}, store: store, log: log}
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rep, err := store.GetClientVersion(ctx)
		if err != nil {
			log.Warn("clientversion: load global", "err", err)
		} else if rep != nil {
			r.global = rep
		}
	}
	return r
}

// Report records a client's installed build and advances the global latest.
func (r *Registry) Report(clientID string, rep Report) {
	if clientID == "" {
		return
	}
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	r.mu.Lock()
	for id, when := range r.seen {
		if time.Since(when) > 24*time.Hour {
			delete(r.seen, id)
			delete(r.byClient, id)
		}
	}
	if _, exists := r.byClient[clientID]; !exists && len(r.byClient) >= 4096 {
		var oldestID string
		var oldest time.Time
		for id, when := range r.seen {
			if oldestID == "" || when.Before(oldest) {
				oldestID, oldest = id, when
			}
		}
		delete(r.byClient, oldestID)
		delete(r.seen, oldestID)
	}
	r.byClient[clientID] = rep
	r.seen[clientID] = time.Now()
	r.global = &rep
	r.revision++
	r.persisting = r.store != nil
	r.mu.Unlock()
	if r.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := r.store.SetClientVersion(ctx, rep); err != nil {
			r.log.Warn("clientversion: persist global", "err", err)
		}
		r.mu.Lock()
		r.persisting = false
		r.mu.Unlock()
	}
}

// Resolve returns the version to serve for clientID: the client's own report,
// else the global latest, else ok=false (caller falls back to config).
func (r *Registry) Resolve(clientID string) (Report, bool) {
	r.mu.Lock()
	if clientID != "" {
		if rep, ok := r.byClient[clientID]; ok && time.Since(r.seen[clientID]) <= 24*time.Hour {
			r.mu.Unlock()
			return rep, true
		}
	}
	revision := r.revision
	refresh := r.store != nil && !r.persisting
	r.mu.Unlock()
	if refresh {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if rep, err := r.store.GetClientVersion(ctx); err == nil && rep != nil {
			r.mu.Lock()
			// A report or another refresh may have completed while this read
			// was pending. Do not replace that newer cached value with it.
			if r.revision == revision {
				copy := *rep
				r.global = &copy
				r.revision++
			}
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// A matching report may have arrived while the global lookup was pending.
	if clientID != "" {
		if rep, ok := r.byClient[clientID]; ok && time.Since(r.seen[clientID]) <= 24*time.Hour {
			return rep, true
		}
	}
	if r.global != nil {
		return *r.global, true
	}
	return Report{}, false
}
