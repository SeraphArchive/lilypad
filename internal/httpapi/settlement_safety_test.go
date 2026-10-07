package httpapi

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"lilypad/internal/gem"
	"lilypad/internal/store"
)

func TestLoopMissionDelayedBatchPreservesNewerProgress(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
		"user_loop_mission": {{"_masterMissionId": 500, "_clearCount": 10, "_receivedCount": 5, "_isDisplayed": false, "_future": 99}},
	}); err != nil {
		t.Fatal(err)
	}
	for batch, entries := range [][]map[string]any{
		{{"masterMissionId": 500, "receivedCount": 3, "clearCount": 3}},
		{{"masterMissionId": 500, "receivedCount": 6, "clearCount": 6}, {"masterMissionId": 500, "receivedCount": 7, "clearCount": 7}, {"masterMissionId": 500, "receivedCount": 6, "clearCount": 6}},
	} {
		for i := 0; i < 2; i++ {
			assertRPCSuccess(t, post(t, s, "/api/mission/loop/receive", xuid, map[string]any{"receiveMissions": entries}))
		}
		snapshot, err := pg.ExportSnapshot(context.Background(), uid)
		if err != nil {
			t.Fatal(err)
		}
		if asBool(snapshot.Tables["user_loop_mission"][0]["_isDisplayed"]) != (batch > 0) {
			t.Fatal("delayed and new loop claims did not preserve their display behavior", snapshot)
		}
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	rows := snapshot.Tables["user_loop_mission"]
	if len(rows) != 1 || asInt(rows[0]["_clearCount"]) != 10 || asInt(rows[0]["_receivedCount"]) != 7 || !asBool(rows[0]["_isDisplayed"]) || asInt(rows[0]["_future"]) != 99 {
		t.Fatal("loop claim regressed stored progress", rows)
	}
	if snapshot.Balance.Free != 40 {
		t.Fatal("loop batch did not grant exactly the unreceived interval", snapshot.Balance)
	}
}

func TestSettlementOverflowRollsBackClaimAndPayment(t *testing.T) {
	for _, kind := range []string{"gift_inventory", "gift_total", "mission_currency", "mission_profile", "mission_batch", "loop_batch", "item_lottery_batch", "recover"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				switch kind {
				case "mission_currency":
					defs["MasterCurrency"] = []map[string]any{{"id": 701, "label": "currency"}}
					defs["MasterReward"][0] = map[string]any{"id": 600, "groupLabel": "reward", "category": 10, "masterLabel": "currency", "num": 1}
				case "mission_profile":
					defs["MasterReward"][0]["category"] = 13
				case "mission_batch", "loop_batch":
					defs["MasterReward"][0] = map[string]any{"id": 600, "groupLabel": "reward", "category": 1, "masterLabel": "ticket", "num": int64(1 << 62)}
					defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "groupLabel": "reward", "category": 1, "masterLabel": "ticket", "num": int64(1 << 62)})
				case "item_lottery_batch":
					defs["MasterReward"][1]["num"] = int64(1 << 62)
					defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "groupLabel": "batch", "category": 13, "num": int64(1 << 62)})
				}
			})
			tables := map[string][]store.Row{
				"user_item":        {{"_id": 700, "_num": 10, "_totalNum": 10}},
				"user_profile":     {{"_limitBreakPower": 0}},
				"user_currency":    {{"_id": 701, "_num": int64(math.MaxInt64)}},
				"user_life":        {{"_num": int64(math.MaxInt64)}},
				"user_server_gift": {{"_giftId": 1, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 1, "_isReceived": false}},
			}
			route := "/api/mission/receive"
			body := map[string]any{"masterMissionIds": []int{500}}
			switch kind {
			case "gift_inventory", "gift_total":
				field := "_num"
				if kind == "gift_total" {
					field = "_totalNum"
				}
				tables["user_item"][0][field] = int64(math.MaxInt64)
				route, body = "/api/gift/receive", map[string]any{"serverGiftIds": []int{1}}
			case "mission_profile":
				tables["user_profile"][0]["_limitBreakPower"] = int64(math.MaxInt64)
			case "loop_batch":
				route, body = "/api/mission/loop/receive", map[string]any{"receiveMissions": []map[string]any{{"masterMissionId": 500, "receivedCount": 1, "clearCount": 1}}}
			case "item_lottery_batch":
				route, body = "/api/item/lottery/draw", map[string]any{"masterItemLotteryId": 400, "drawCount": 1}
			case "recover":
				route, body = "/api/life/recover", map[string]any{"recoveryNum": 1, "consumedHc": 10}
			}
			if err := pg.ImportSnapshot(context.Background(), uid, tables, &gem.Balance{Free: 100}); err != nil {
				t.Fatal(err)
			}
			before, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil {
				t.Fatal(err)
			}
			out := post(t, s, route, xuid, body)
			if asInt(out["code"]) == 0 {
				t.Fatal("overflowing settlement succeeded", out)
			}
			after, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed settlement changed inventory, claim, or wallet")
			}
		})
	}
}

func TestLotteryTicketRewardAppliesDebitBeforeCreditAndRetries(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterLottery"][0]["consumeItemId"] = 700
		defs["MasterLottery"][0]["ticketCost"] = 1
		defs["MasterLotteryReward"][0]["rewardCategory"] = 1
		defs["MasterLotteryReward"][0]["rewardId"] = 700
	})
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
		"user_item": {{"_id": 700, "_num": int64(math.MaxInt64), "_totalNum": 10, "_future": 99}},
	}); err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "ticket")
	if err := pg.BeginSession(context.Background(), uid, token, 1); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "consumeType": 2, "ticketCost": 1}
	_, first := postWithSession(t, s, "/api/lottery/draw", xuid, token, 12345, body)
	assertRPCSuccess(t, first)
	_, retry := postWithSession(t, s, "/api/lottery/draw", xuid, token, 12345, body)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("ticket draw retry changed its response")
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.Tables["user_item"][0]
	if asInt(row["_num"]) != math.MaxInt64 || asInt(row["_totalNum"]) != 11 || asInt(row["_future"]) != 99 || asInt(snapshot.Tables["user_lottery"][0]["_drawCount"]) != 1 {
		t.Fatal("ticket reward lost a balance or debited twice", snapshot)
	}
}

func TestRewardCanReachExactIntegerLimit(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
		"user_item":        {{"_id": 700, "_num": int64(math.MaxInt64 - 1), "_totalNum": int64(math.MaxInt64 - 1)}},
		"user_server_gift": {{"_giftId": 1, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 1, "_isReceived": false}},
	}); err != nil {
		t.Fatal(err)
	}
	assertRPCSuccess(t, post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int{1}}))
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.Tables["user_item"][0]
	if asInt(row["_num"]) != math.MaxInt64 || asInt(row["_totalNum"]) != math.MaxInt64 || !asBool(snapshot.Tables["user_server_gift"][0]["_isReceived"]) {
		t.Fatal("valid reward at the exact integer limit was lost", snapshot)
	}
}

func TestSettlementCounterOverflowRollsBack(t *testing.T) {
	for _, tc := range []struct {
		table, key, field string
		id                int64
		route             string
	}{
		{"user_stockable_regular_reward", "_masterStockableRegularRewardId", "_totalReceivedNum", 900, "/api/stockable_regular_reward/receive"},
		{"user_lottery", "_masterLotteryId", "_drawCount", 100, "/api/lottery/draw"},
		{"user_lottery", "_masterLotteryId", "_dailyDrawCount", 100, "/api/lottery/draw"},
		{"user_lottery", "_masterLotteryId", "_issueRewardCount", 100, "/api/lottery/draw"},
		{"user_lottery", "_masterLotteryId", "_step", 100, "/api/lottery/draw"},
		{"user_item_lottery", "_masterItemLotteryId", "_totalDrawCount", 400, "/api/item/lottery/draw"},
		{"user_item_lottery", "_masterItemLotteryId", "_dailyDrawCount", 400, "/api/item/lottery/draw"},
		{"user_item_lottery", "_masterItemLotteryId", "_nonPickupDrawCount", 400, "/api/item/lottery/draw"},
		{"user_item_lottery", "_masterItemLotteryId", "_resetCount", 400, "/api/item/lottery/draw"},
		{"user_item_lottery_reward", "_masterItemLotteryRewardId", "_dropCount", 401, "/api/item/lottery/draw"},
		{"user_select_ticket_lottery", "_masterSelectTicketLotteryId", "_drawCount", 970, "/api/lottery/card/exchange"},
		{"user_lottery_encore", "_masterLotteryShopId", "_dailyDrawCount", 800, "/api/lottery/draw"},
		{"user_lottery_encore", "_masterLotteryShopId", "_dailyTotalRewardCount", 800, "/api/lottery/draw"},
		{"user_lottery_encore", "_masterLotteryShopId", "_drawDay", 800, "/api/lottery/prepare"},
	} {
		t.Run(tc.table+tc.field, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				defs["MasterStockableRegularReward"] = []map[string]any{{"id": 900, "category": 1, "rewardLabel": "stock", "stockPeriod": "period", "releaseLabel": "release.default", "maxRewardNumPerTerm": 10}}
				defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "label": "stock", "category": 1, "masterLabel": "ticket", "num": 1})
				defs["MasterLottery"][0]["masterLotteryShopId"] = 800
				defs["MasterLotteryShop"] = []map[string]any{{"id": 800, "masterLotteryEncoreLabel": "encore"}}
				defs["MasterLotteryEncore"] = []map[string]any{{"id": 801, "label": "encore"}}
				defs["MasterItemLottery"][0]["dailyDrawLimit"] = 0
				defs["MasterItemLottery"][0]["totalDrawLimit"] = 0
				defs["MasterItemLotteryReward"][0]["dropLimit"] = 0
				if tc.field == "_resetCount" {
					defs["MasterItemLottery"][0]["afterPickupCompleteType"] = 2
					defs["MasterItemLotteryReward"][0]["dropLimit"] = 1
					defs["MasterItemLotteryReward"][0]["isPickup"] = true
				}
				defs["MasterSelectTicketLottery"] = []map[string]any{{"id": 970, "termStockItemCost": 1}}
				defs["MasterSelectTicketLotteryReward"] = []map[string]any{{"id": 971, "rewardCardLabel": "card"}}
				defs["MasterCard"] = []map[string]any{{"id": 1001, "label": "card"}}
			})
			s.lotteryShop = []int{800}
			row := store.Row{tc.key: tc.id, tc.field: int64(math.MaxInt64)}
			if tc.table == "user_stockable_regular_reward" {
				row["_lastReceivedAt"] = time.Now().Unix() - 8*86400
				row["_nextReceivableAt"] = 0
			}
			if tc.table == "user_item_lottery" {
				row["_lastDrawAt"] = time.Now().Unix()
			}
			if tc.table == "user_lottery_encore" && tc.field != "_drawDay" {
				row["_drawDay"] = 1
			}
			tables := map[string][]store.Row{
				tc.table:               {row},
				"user_item":            {{"_id": 700, "_num": 10}},
				"user_profile":         {{"_limitBreakPower": 0}},
				"user_term_stock_item": {{"_uid": "ticket", "_value": 1}},
			}
			if err := pg.ImportSnapshot(context.Background(), uid, tables, &gem.Balance{Free: 1000}); err != nil {
				t.Fatal(err)
			}
			before, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"category": 1, "masterLotteryId": 100, "rewardCount": 1, "gemCost": 300, "masterItemLotteryId": 400, "drawCount": 1, "masterSelectTicketLotteryId": 970, "selectRewardId": 971, "termStockItemUids": []string{"ticket"}}
			out := post(t, s, tc.route, xuid, body)
			if asInt(out["code"]) == 0 {
				t.Fatal("counter overflow was committed", out)
			}
			after, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed counter update consumed payment, rewards or progress")
			}
		})
	}
}
