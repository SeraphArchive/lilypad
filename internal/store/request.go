package store

import (
	"context"
	"errors"
	"lilypad/internal/gem"
)

var ErrSession = errors.New("stale or revoked session")
var ErrRequestConflict = errors.New("request identifier reused with different payload")

// RequestStore protects the complete request, including its durable retry result.
// A process identity is required; a transport counter cannot authorize a session.
type RequestStore interface {
	CheckSession(context.Context, int64, string, int64) (int64, error)
	BeginSession(context.Context, int64, string, int64) error
	RunRequest(context.Context, int64, int64, string, string, func(context.Context) (Response, error)) (Response, error)
}

type Response struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header"`
	Body   []byte              `json:"body"`
}

type Snapshot struct {
	Tables  map[string][]Row
	Hashes  map[string]string
	Balance gem.Balance
}

// ArchiveStore owns the transaction boundary for full-state transfers.
type ArchiveStore interface {
	ExportSnapshot(context.Context, int64) (Snapshot, error)
	ImportSnapshot(context.Context, int64, map[string][]Row, *gem.Balance) error
}
