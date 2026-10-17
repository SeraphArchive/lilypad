package gem

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"
)

type ReasonType int

const (
	ReasonNone         ReasonType = 0
	ReasonLottery      ReasonType = 1
	ReasonClientGift   ReasonType = 2
	ReasonServerGift   ReasonType = 5
	ReasonMission      ReasonType = 6
	ReasonLifeRecovery ReasonType = 8
)

// Reason carries client-localized master references, not a server-generated title.
// The native GamelibReason fields are signed 32-bit integers.
type Reason struct {
	Type            ReasonType `json:"gamelibReasonType"`
	MasterLotteryID int64      `json:"masterLotteryId,omitempty"`
	MasterReasonID  int64      `json:"masterReasonId,omitempty"`
	MasterMissionID int64      `json:"masterMissionId,omitempty"`
	RecoveryNum     int64      `json:"recoveryNum,omitempty"`
}

func (r Reason) Validate() error {
	for _, n := range []int64{r.MasterLotteryID, r.MasterReasonID, r.MasterMissionID, r.RecoveryNum} {
		if n < 0 || n > math.MaxInt32 {
			return errors.New("gem: reason field outside native integer range")
		}
	}
	valid := false
	switch r.Type {
	case ReasonNone:
		valid = r.MasterLotteryID == 0 && r.MasterReasonID == 0 && r.MasterMissionID == 0 && r.RecoveryNum == 0
	case ReasonLottery:
		valid = r.MasterLotteryID > 0 && r.MasterReasonID == 0 && r.MasterMissionID == 0 && r.RecoveryNum == 0
	case ReasonClientGift, ReasonServerGift:
		valid = r.MasterLotteryID == 0 && r.MasterMissionID == 0 && r.RecoveryNum == 0
	case ReasonMission:
		valid = r.MasterLotteryID == 0 && r.MasterReasonID == 0 && r.MasterMissionID > 0 && r.RecoveryNum == 0
	case ReasonLifeRecovery:
		valid = r.MasterLotteryID == 0 && r.MasterReasonID == 0 && r.MasterMissionID == 0 && r.RecoveryNum > 0
	}
	if !valid {
		return errors.New("gem: invalid transaction reason")
	}
	return nil
}

// MarshalJSON preserves explicit zero reason IDs for gifts and missions while
// omitting references belonging to other reason types.
func (r Reason) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	out := map[string]any{"gamelibReasonType": r.Type}
	switch r.Type {
	case ReasonLottery:
		out["masterLotteryId"] = r.MasterLotteryID
	case ReasonClientGift, ReasonServerGift:
		out["masterReasonId"] = r.MasterReasonID
	case ReasonMission:
		out["masterMissionId"] = r.MasterMissionID
		out["masterReasonId"] = 0
	case ReasonLifeRecovery:
		out["recoveryNum"] = r.RecoveryNum
	}
	return json.Marshal(out)
}

type Credit struct {
	Amount int64
	Reason Reason
}

const (
	TransactionConsume = 1
	TransactionIssue   = 3
)

// Transaction contains actual signed wallet changes; replacements are not game
// transactions. The platform adapter converts amounts and reason to wire strings.
type Transaction struct {
	Type   int
	Date   time.Time
	Reason Reason
	Free   int64
	Paid   int64
}

// HistoryStore reads committed transactions newest-first. No entries is an empty
// slice; hasNext is determined from an additional row, not from a separate count.
type HistoryStore interface {
	History(ctx context.Context, userID int64, offset, limit int) (transactions []Transaction, hasNext bool, err error)
}

func ValidateHistoryPage(offset, limit int) error {
	if offset < 0 || int64(offset) > math.MaxInt32 || limit < 1 || limit > 100 {
		return errors.New("gem: invalid history pagination")
	}
	return nil
}
