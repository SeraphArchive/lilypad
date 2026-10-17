package store

import (
	"math"
	"testing"

	"lilypad/internal/gem"
)

func TestGemChangeProvenanceMatchesSettlement(t *testing.T) {
	for _, tc := range []struct {
		change  GemChange
		invalid bool
	}{
		{change: GemChange{}},
		{change: GemChange{Add: 50, Consume: 10}},
		{change: GemChange{Add: 50, Credits: []gem.Credit{{Amount: 20}, {Amount: 30, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 500}}}}},
		{change: GemChange{Add: -1}, invalid: true},
		{change: GemChange{Consume: -1}, invalid: true},
		{change: GemChange{Add: 50, Credits: []gem.Credit{{Amount: 20}}}, invalid: true},
		{change: GemChange{Credits: []gem.Credit{{Amount: 1}}}, invalid: true},
		{change: GemChange{Add: 1, Credits: []gem.Credit{{Amount: 0}}}, invalid: true},
		{change: GemChange{Add: 1, Credits: []gem.Credit{{Amount: -1}}}, invalid: true},
		{change: GemChange{Add: math.MaxInt64, Credits: []gem.Credit{{Amount: math.MaxInt64}, {Amount: 1}}}, invalid: true},
		{change: GemChange{Add: 1, Credits: []gem.Credit{{Amount: 1, Reason: gem.Reason{Type: 99}}}}, invalid: true},
		{change: GemChange{Consume: 1, ConsumeReason: gem.Reason{Type: gem.ReasonLottery}}, invalid: true},
	} {
		if err := tc.change.Validate(); (err != nil) != tc.invalid {
			t.Fatalf("change %+v: %v; invalid=%v", tc.change, err, tc.invalid)
		}
	}
}
