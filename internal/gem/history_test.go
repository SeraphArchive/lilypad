package gem

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestReasonWireFields(t *testing.T) {
	for _, tc := range []struct {
		reason Reason
		wire   string
	}{
		{Reason{}, `{"gamelibReasonType":0}`},
		{Reason{Type: ReasonLottery, MasterLotteryID: 100}, `{"gamelibReasonType":1,"masterLotteryId":100}`},
		{Reason{Type: ReasonClientGift}, `{"gamelibReasonType":2,"masterReasonId":0}`},
		{Reason{Type: ReasonServerGift, MasterReasonID: 900}, `{"gamelibReasonType":5,"masterReasonId":900}`},
		{Reason{Type: ReasonMission, MasterMissionID: 500}, `{"gamelibReasonType":6,"masterMissionId":500,"masterReasonId":0}`},
		{Reason{Type: ReasonLifeRecovery, RecoveryNum: 2}, `{"gamelibReasonType":8,"recoveryNum":2}`},
	} {
		raw, err := json.Marshal(tc.reason)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.wire), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reason %+v: %s; want %s", tc.reason, raw, tc.wire)
		}
		var roundtrip Reason
		if err := json.Unmarshal(raw, &roundtrip); err != nil || roundtrip != tc.reason {
			t.Fatal("reason roundtrip", roundtrip, err)
		}
	}
}

func TestReasonRejectsInvalidNativeReferences(t *testing.T) {
	for _, reason := range []Reason{
		{Type: 99}, {Type: ReasonLottery}, {Type: ReasonMission}, {Type: ReasonLifeRecovery},
		{Type: ReasonServerGift, MasterReasonID: -1},
		{Type: ReasonClientGift, MasterReasonID: int64(math.MaxInt32) + 1},
		{Type: ReasonLottery, MasterLotteryID: 100, MasterMissionID: 500},
		{Type: ReasonNone, MasterReasonID: 900},
	} {
		if err := reason.Validate(); err == nil {
			t.Fatalf("invalid reason accepted: %+v", reason)
		}
		if _, err := json.Marshal(reason); err == nil {
			t.Fatalf("invalid reason serialized: %+v", reason)
		}
	}
}
