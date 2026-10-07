package httpapi

import (
	"errors"
	"math"

	"lilypad/internal/deltacomm"
)

// Check before adding so an invalid settlement rolls back its claim and debit.
// Do not clamp a reward or replace an existing balance with a wrapped value.
func addReward(current, amount int64) (int64, error) {
	if amount < 0 || current > math.MaxInt64-amount {
		return 0, errors.New("reward amount overflows")
	}
	return current + amount, nil
}

func addRowReward(row deltacomm.Row, field string, amount int64) error {
	total, err := addReward(asInt(row[field]), amount)
	if err == nil {
		row[field] = total
	}
	return err
}

func accumulateReward[K comparable](amounts map[K]int64, key K, amount int64) error {
	total, err := addReward(amounts[key], amount)
	if err == nil {
		amounts[key] = total
	}
	return err
}
