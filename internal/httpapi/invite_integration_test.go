package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// invite/reward/receive returns the invite's sender-condition rows, creating
// zero-count rows on first contact, and grants nothing (the official balance
// does not move on this route). Invite 145000002 has sender conditions
// 146000004/5/6 in the local master snapshot.
func TestInviteRewardReceiveEchoesConditionRows(t *testing.T) {
	s := newSeededEconomyServer(t)
	if _, ok := s.master.InviteSenderConditionIDs(145000002); !ok {
		t.Skip("invite 145000002 not in master data")
	}
	xuid := fmt.Sprintf("invite-%d", time.Now().UnixNano())

	out := post(t, s, "/api/invite/reward/receive", xuid, map[string]any{
		"hdr": "", "masterInviteId": 145000002,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("invite receive failed: %v", out)
	}
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows, ok := put["user_invite_sender_reward"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("expected 3 condition rows, got %v", put["user_invite_sender_reward"])
	}
	for i, r := range rows {
		row := r.(map[string]any)
		want := fmt.Sprintf("%d", 146000004+i)
		if row["_masterInviteConditionId"].(json.Number).String() != want {
			t.Fatalf("row %d: %v", i, row)
		}
		if row["_rewardIssueCount"].(json.Number).String() != "0" {
			t.Fatalf("row %d issueCount: %v", i, row)
		}
	}
	if out["hashes"].(map[string]any)["user_invite_sender_reward"] == "" {
		t.Fatal("missing table hash")
	}

	// Second call returns the persisted rows unchanged.
	out = post(t, s, "/api/invite/reward/receive", xuid, map[string]any{
		"hdr": "", "masterInviteId": 145000002,
	})
	put = out["tables"].(map[string]any)["putItems"].(map[string]any)
	if len(put["user_invite_sender_reward"].([]any)) != 3 {
		t.Fatalf("second call rows: %v", put)
	}
	// And the rows survive a pull (collection merge, not singleton replace).
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{
		"hdr": "", "tables": []any{"user_invite_sender_reward"},
	})
	if len(pull["tables"].(map[string]any)["user_invite_sender_reward"].([]any)) != 3 {
		t.Fatal("invite rows did not persist")
	}
}
