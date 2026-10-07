package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// stockable_regular_reward/receive: first receive on a fresh account pays out
// the capped elapsed-term count of the category's weekly reward (the 異時層
// ticket: MasterStockableRegularReward 228001001 -> Reward_2200101 -> item
// 7020412), stamps the row, and schedules the next Monday-04:00-JST reset.
func TestStockableRegularRewardReceive(t *testing.T) {
	s := newSeededEconomyServer(t)
	if s.master == nil || len(s.master.StockableRegularRewards(1)) == 0 {
		t.Skip("no category-1 stockable regular reward in master data")
	}
	xuid := fmt.Sprintf("srr-%d", time.Now().UnixNano())

	out := post(t, s, "/api/stockable_regular_reward/receive", xuid, map[string]any{
		"hdr": "", "category": 1,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("receive failed: %v", out)
	}
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows, ok := put["user_stockable_regular_reward"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 reward row, got %v", put["user_stockable_regular_reward"])
	}
	row := rows[0].(map[string]any)
	if row["_masterStockableRegularRewardId"].(json.Number).String() != "228001001" {
		t.Fatalf("wrong reward id: %v", row)
	}
	// First receive on a fresh account: elapsed terms since the stock period's
	// 2024 open, capped at maxRewardNumPerTerm (10).
	if row["_receivedNum"].(json.Number).String() != "10" ||
		row["_totalReceivedNum"].(json.Number).String() != "10" {
		t.Fatalf("first receive should grant the per-term cap: %v", row)
	}
	if row["_nextReceivableAt"].(json.Number).String() != fmt.Sprintf("%d", nextWeeklyReset(time.Now().Unix())) {
		t.Fatalf("nextReceivableAt wrong: %v", row)
	}
	items, ok := put["user_item"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 item row, got %v", put["user_item"])
	}
	item := items[0].(map[string]any)
	if item["_id"].(json.Number).String() != "7020412" || item["_num"].(json.Number).String() != "10" {
		t.Fatalf("wrong item grant: %v", item)
	}

	// Not due again until the next weekly reset: second call grants nothing.
	out = post(t, s, "/api/stockable_regular_reward/receive", xuid, map[string]any{
		"hdr": "", "category": 1,
	})
	put = out["tables"].(map[string]any)["putItems"].(map[string]any)
	if _, ok := put["user_item"]; ok {
		t.Fatalf("second receive must not grant items: %v", put)
	}
	row = put["user_stockable_regular_reward"].([]any)[0].(map[string]any)
	if row["_receivedNum"].(json.Number).String() != "10" || row["_totalReceivedNum"].(json.Number).String() != "10" {
		t.Fatalf("second receive changed the row: %v", row)
	}
}

func TestWeeklyTermIndexAndReset(t *testing.T) {
	// Official samples: receive 2026-06-16 06:57 JST -> next reset Monday
	// 2026-06-22 04:00 JST; 8 elapsed terms by 2026-08-14 02:33 JST -> 8.
	if got := nextWeeklyReset(1781560620); got != 1782068400 {
		t.Fatalf("nextWeeklyReset(prev sample) = %d, want 1782068400", got)
	}
	if got := nextWeeklyReset(1786642393); got != 1786906800 {
		t.Fatalf("nextWeeklyReset(new sample) = %d, want 1786906800", got)
	}
	if got := weeklyTermIndex(1786642393) - weeklyTermIndex(1781560620); got != 8 {
		t.Fatalf("elapsed terms = %d, want 8", got)
	}
}
