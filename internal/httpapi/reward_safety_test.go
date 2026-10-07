package httpapi

import (
	"context"
	"testing"

	"lilypad/internal/gem"
	"lilypad/internal/store"
)

func TestPullAbsentTableBaselineCanCreateTable(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	out := post(t, s, "/api/user/pull", xuid, map[string]any{"tables": []string{"user_item"}})
	assertRPCSuccess(t, out)
	if out["hashes"].(map[string]any)["user_item"] != "" {
		t.Fatal("absent table acquired a fabricated baseline", out)
	}
	assertRPCSuccess(t, post(t, s, "/api/user/push", xuid, map[string]any{
		"hashes": out["hashes"], "deltas": map[string]any{"putItems": map[string]any{"user_item": []store.Row{{"_id": 700, "_num": 2}}}},
	}))
	hashes, err := pg.AllHashes(context.Background(), uid)
	if err != nil || hashes["user_item"] == "" {
		t.Fatal("created table has no durable version", hashes, err)
	}
}

func TestInviteRepeatReturnsStoredVersion(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterInvite"] = []map[string]any{{"id": 800, "senderConditionGroupLabel": "conditions"}}
		defs["MasterInviteCondition"] = []map[string]any{{"id": 801, "groupLabel": "conditions"}}
	})
	for i := 0; i < 2; i++ {
		out := post(t, s, "/api/invite/reward/receive", xuid, map[string]any{"masterInviteId": 800})
		assertRPCSuccess(t, out)
		hashes, err := pg.AllHashes(context.Background(), uid)
		if err != nil || out["hashes"].(map[string]any)["user_invite_sender_reward"] != hashes["user_invite_sender_reward"] {
			t.Fatal("invite response returned a different baseline", out, hashes, err)
		}
	}
}

func TestIncompleteMissionRewardKeepsClaimPending(t *testing.T) {
	for _, kind := range []string{"missing_group", "missing_item", "unsupported", "missing_profile"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				rw := defs["MasterReward"][0]
				switch kind {
				case "missing_group":
					defs["MasterMission"][0]["masterRewardGroupLabel"] = "missing"
				case "missing_item":
					rw["category"], rw["masterLabel"] = 1, "missing"
				case "unsupported":
					rw["category"] = 9
				case "missing_profile":
					rw["category"] = 13
				}
			})
			for _, route := range []string{"/api/mission/receive", "/api/mission/loop/receive"} {
				body := map[string]any{"masterMissionIds": []int{500}, "receiveMissions": []any{map[string]any{"masterMissionId": 500, "receivedCount": 1, "clearCount": 1}}}
				out := post(t, s, route, xuid, body)
				if asInt(out["code"]) == 0 {
					t.Fatal("incomplete reward marked received", route, out)
				}
			}
			snapshot, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil || len(snapshot.Tables["user_mission"])+len(snapshot.Tables["user_loop_mission"]) != 0 || snapshot.Balance.Free != 0 {
				t.Fatal("failed claims changed state", snapshot, err)
			}
		})
	}
}

func TestLotteryFailedSettlementKeepsWalletAndProgress(t *testing.T) {
	for _, kind := range []string{"unsupported", "missing_conversion", "missing_term_stock", "missing_ticket"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServer(t)
			req := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "gemCost": 300}
			switch kind {
			case "unsupported":
				f, _ := s.master.File("MasterLotteryReward")
				f.Items[0]["rewardCategory"] = 77
			case "missing_conversion":
				req["rewardCount"] = 2
			case "missing_term_stock":
				req["termStockItemCost"] = 1
			case "missing_ticket":
				req["consumeType"], req["ticketCost"] = 2, 1
			}
			if _, err := pg.Set(context.Background(), uid, gem.Balance{Free: 1000}); err != nil {
				t.Fatal(err)
			}
			out := post(t, s, "/api/lottery/draw", xuid, req)
			if asInt(out["code"]) == 0 {
				t.Fatal("invalid settlement succeeded", out)
			}
			snapshot, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil || snapshot.Balance.Free != 1000 || len(snapshot.Tables["user_lottery"])+len(snapshot.Tables["user_lottery_result"])+len(snapshot.Tables["user_card"]) != 0 {
				t.Fatal("failed lottery changed wallet or progress", snapshot, err)
			}
		})
	}
}

func TestStockableUnknownRewardDoesNotAdvanceClaim(t *testing.T) {
	for _, kind := range []string{"missing_reward", "missing_item", "unsupported", "oversized_term_stock"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				defs["MasterStockableRegularReward"] = []map[string]any{{"id": 900, "category": 1, "rewardLabel": "stock", "stockPeriod": "period", "releaseLabel": "release.default", "maxRewardNumPerTerm": 10}}
				defs["MasterRelease"] = []map[string]any{{"id": 901, "label": "period", "openAt": "2024/01/01 04:00:00"}}
				rw := map[string]any{"id": 603, "label": "stock", "category": 1, "masterLabel": "missing", "num": 1}
				switch kind {
				case "missing_reward":
					rw["label"] = "unrelated"
				case "unsupported":
					rw["category"] = 77
				case "oversized_term_stock":
					rw["category"], rw["num"] = 12, 10001
				}
				defs["MasterReward"] = append(defs["MasterReward"], rw)
			})
			out := post(t, s, "/api/stockable_regular_reward/receive", xuid, map[string]any{"category": 1})
			if asInt(out["code"]) == 0 {
				t.Fatal("unknown reward settled", out)
			}
			snapshot, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil || len(snapshot.Tables["user_stockable_regular_reward"])+len(snapshot.Tables["user_item"]) != 0 {
				t.Fatal("failed reward advanced its claim", snapshot, err)
			}
		})
	}
}

func TestDailyMissingRewardDoesNotAdvanceStamp(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterLoginBonus"] = []map[string]any{{"id": 950, "loginBonusContentGroupLabel": "days", "releaseLabel": "release.default"}}
		defs["MasterLoginBonusContent"] = []map[string]any{{"id": 951, "groupLabel": "days", "day": 1, "rewardGroupLabel": "missing"}}
	})
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_profile": {{"_lastDailyUpdatedAt": 0}}}); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/daily/update", xuid, nil)
	if asInt(out["code"]) == 0 {
		t.Fatal("daily update consumed a missing reward", out)
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil || asInt(snapshot.Tables["user_profile"][0]["_lastDailyUpdatedAt"]) != 0 || len(snapshot.Tables["user_login_bonus"])+len(snapshot.Tables["user_record"]) != 0 {
		t.Fatal("failed daily changed progress", snapshot, err)
	}
}

func TestUnknownServerGiftDoesNotConsumeBatch(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_server_gift": {
		{"_giftId": 1, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 2, "_isReceived": false},
		{"_giftId": 2, "_rewardCategory": 77, "_itemNum": 1, "_isReceived": false},
	}}); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int{1, 2}})
	if asInt(out["code"]) == 0 {
		t.Fatal("unsupported gift consumed", out)
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil || len(snapshot.Tables["user_item"]) != 0 {
		t.Fatal("failed gift batch granted items", snapshot, err)
	}
	for _, gift := range snapshot.Tables["user_server_gift"] {
		if asBool(gift["_isReceived"]) {
			t.Fatal("failed gift marked received", gift)
		}
	}
}

func TestIncompleteItemLotteryDoesNotConsumeTicket(t *testing.T) {
	for _, kind := range []string{"missing_group", "missing_completion", "missing_profile"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				switch kind {
				case "missing_group":
					defs["MasterItemLotteryReward"][0]["rewardGroupLabel"] = "missing"
				case "missing_completion":
					defs["MasterItemLotteryReward"][0]["isPickup"] = true
					defs["MasterItemLottery"][0]["completeRewardGroupLabel"] = "missing"
				}
			})
			tables := map[string][]store.Row{"user_item": {{"_id": 700, "_num": 10}}}
			if kind != "missing_profile" {
				tables["user_profile"] = []store.Row{{"_limitBreakPower": 0}}
			}
			if err := pg.ImportTables(context.Background(), uid, tables); err != nil {
				t.Fatal(err)
			}
			out := post(t, s, "/api/item/lottery/draw", xuid, map[string]any{"masterItemLotteryId": 400, "drawCount": 1})
			if asInt(out["code"]) == 0 {
				t.Fatal("incomplete item lottery settled", out)
			}
			snapshot, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil || asInt(snapshot.Tables["user_item"][0]["_num"]) != 10 || len(snapshot.Tables["user_item_lottery"])+len(snapshot.Tables["user_item_lottery_result"]) != 0 {
				t.Fatal("failed lottery changed progress", snapshot, err)
			}
		})
	}
}

func TestSelectTicketRejectsMissingPaymentAndUnknownReward(t *testing.T) {
	for _, kind := range []string{"missing_uid", "unknown_reward", "mismatched_card", "unknown_card"} {
		t.Run(kind, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				defs["MasterSelectTicketLottery"] = []map[string]any{{"id": 970, "termStockItemCost": 1}}
				defs["MasterSelectTicketLotteryReward"] = []map[string]any{{"id": 971, "rewardCardLabel": "card"}}
				defs["MasterCard"] = []map[string]any{{"id": 1001, "label": "card"}}
			})
			if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_term_stock_item": {{"_uid": "ticket", "_value": 1}}}); err != nil {
				t.Fatal(err)
			}
			req := map[string]any{"masterSelectTicketLotteryId": 970, "selectRewardId": 971, "termStockItemUids": []string{"ticket"}}
			switch kind {
			case "missing_uid":
				delete(req, "termStockItemUids")
			case "unknown_reward":
				req["selectRewardId"], req["selectCardId"] = 999, 1001
			case "mismatched_card":
				req["selectCardId"] = 1002
			case "unknown_card":
				delete(req, "selectRewardId")
				req["selectCardId"] = 1002
			}
			out := post(t, s, "/api/lottery/card/exchange", xuid, req)
			if asInt(out["code"]) == 0 {
				t.Fatal("invalid exchange accepted", out)
			}
			snapshot, err := pg.ExportSnapshot(context.Background(), uid)
			if err != nil || asInt(snapshot.Tables["user_term_stock_item"][0]["_value"]) != 1 || len(snapshot.Tables["user_card"])+len(snapshot.Tables["user_select_ticket_lottery"]) != 0 {
				t.Fatal("failed exchange changed inventory", snapshot, err)
			}
		})
	}
}
