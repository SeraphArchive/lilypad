// Package gem is the gacha-gem (quartz) domain: a per-player free/paid balance
// and the free-first spend policy. It holds no persistence — store/postgres
// implements Store. The gem is the Gree GameLib PAYMENT balance (the platform
// shim reports it, the gacha consumes it), not a game-API currency.
package gem

import (
	"context"
	"errors"
	"math"
)

// ErrInsufficient is returned by Apply/Consume when free+paid < the requested amount.
var ErrInsufficient = errors.New("gem: insufficient balance")

// Balance is a player's gem holdings: Free (無償石) and Paid (有償石 / charge).
type Balance struct {
	Free int64
	Paid int64
}

// Total is the spendable total.
func (b Balance) Total() int64 { return b.Free + b.Paid }

func (b Balance) Validate() error {
	if b.Free < 0 || b.Paid < 0 || b.Free > math.MaxInt64-b.Paid {
		return errors.New("invalid gem balance")
	}
	return nil
}

// Apply spends n gems from b, free first then paid. Returns the new balance, or
// ErrInsufficient with b unchanged when b.Total() < n. n <= 0 is a no-op.
func Apply(b Balance, n int64) (Balance, error) {
	if err := b.Validate(); err != nil {
		return b, err
	}
	if n <= 0 {
		return b, nil
	}
	if b.Total() < n {
		return b, ErrInsufficient
	}
	if n <= b.Free {
		b.Free -= n
		return b, nil
	}
	rem := n - b.Free
	b.Free = 0
	b.Paid -= rem
	return b, nil
}

// Store is the per-player gem-balance persistence surface, keyed by game userID.
// Implementations are atomic per player.
type Store interface {
	Get(ctx context.Context, userID int64) (Balance, error)
	Add(ctx context.Context, userID int64, free int64) (Balance, error)
	Consume(ctx context.Context, userID int64, n int64) (Balance, error)
	// Set overwrites the balance outright (save import). Returns the stored balance.
	Set(ctx context.Context, userID int64, b Balance) (Balance, error)
}
