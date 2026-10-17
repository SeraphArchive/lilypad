package httpapi

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"lilypad/internal/gem"
	"lilypad/internal/store"
	"lilypad/internal/store/postgres"
)

func assertQuartzHistory(t *testing.T, pg *postgres.Store, uid int64, want []gem.Transaction) {
	t.Helper()
	got, more, err := pg.History(context.Background(), uid, 0, 100)
	if err != nil || more || len(got) != len(want) {
		t.Fatalf("history: %v more=%v err=%v; want %v", got, more, err, want)
	}
	for i := range got {
		if got[i].Date.IsZero() {
			t.Fatal("transaction has no settlement timestamp")
		}
		got[i].Date = time.Time{}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("transaction %d: %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestQuartzHistoryMissionBatchAndLoop(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterMission"] = append(defs["MasterMission"],
			map[string]any{"id": 501, "masterRewardGroupLabel": "quartz"},
			map[string]any{"id": 502, "masterRewardGroupLabel": "batch"})
		defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "groupLabel": "quartz", "category": 99, "num": 50})
	})
	ctx := context.Background()
	if err := pg.ImportTables(ctx, uid, map[string][]store.Row{"user_profile": {{"_limitBreakPower": 0}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/mission/receive", xuid, map[string]any{"masterMissionIds": []int{500, 501, 500, 502}}))
	}
	want := []gem.Transaction{
		{Type: gem.TransactionIssue, Free: 50, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 501}},
		{Type: gem.TransactionIssue, Free: 20, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 500}},
	}
	assertQuartzHistory(t, pg, uid, want)
	batch := map[string]any{"receiveMissions": []map[string]any{
		{"masterMissionId": 500, "receivedCount": 2, "clearCount": 2},
		{"masterMissionId": 500, "receivedCount": 3, "clearCount": 3},
		{"masterMissionId": 500, "receivedCount": 2, "clearCount": 2},
	}}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/mission/loop/receive", xuid, batch))
	}
	want = append([]gem.Transaction{{Type: gem.TransactionIssue, Free: 60, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 500}}}, want...)
	assertQuartzHistory(t, pg, uid, want)
	balance, err := pg.Get(ctx, uid)
	if err != nil || balance.Free != 130 || balance.Paid != 0 {
		t.Fatal("history did not match mission settlement", balance, err)
	}
}

func TestQuartzHistoryGiftProvenanceAndRepeatedClaims(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterReward"][0]["masterReasonLabel"] = "different-reward-reason"
		defs["MasterReason"] = []map[string]any{{"id": 903, "label": "different-reward-reason"}}
		defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "groupLabel": "quartz", "category": 99, "num": 50})
	})
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
		"user_server_gift": {
			{"_giftId": 1, "_rewardCategory": 99, "_itemNum": 100, "_masterReasonId": 901, "_isReceived": false},
			{"_giftId": 2, "_rewardCategory": 99, "_itemNum": 20, "_masterReasonId": 0, "_isReceived": false},
			{"_giftId": 5, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 1, "_isReceived": false},
		},
		"user_client_gift": {
			{"_giftId": 3, "_masterRewardId": 600, "_masterReasonId": 902, "_isReceived": false},
			{"_giftId": 4, "_masterRewardId": 603, "_masterReasonId": 0, "_isReceived": false},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/gift/receive", xuid, map[string]any{
			"serverGiftIds": []int{1, 2, 1, 5}, "clientGiftIds": []int{3, 4, 3},
		}))
	}
	assertQuartzHistory(t, pg, uid, []gem.Transaction{
		{Type: gem.TransactionIssue, Free: 50, Reason: gem.Reason{Type: gem.ReasonClientGift}},
		{Type: gem.TransactionIssue, Free: 20, Reason: gem.Reason{Type: gem.ReasonClientGift, MasterReasonID: 902}},
		{Type: gem.TransactionIssue, Free: 20, Reason: gem.Reason{Type: gem.ReasonServerGift}},
		{Type: gem.TransactionIssue, Free: 100, Reason: gem.Reason{Type: gem.ReasonServerGift, MasterReasonID: 901}},
	})
}

func TestQuartzHistoryDailyBonusRecordsOnlyReceipt(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterReason"] = []map[string]any{{"id": 900, "label": "reason.synthetic.login"}}
		defs["MasterLoginBonus"] = []map[string]any{{"id": 1000, "loginBonusContentGroupLabel": "login", "canLoop": true}}
		defs["MasterLoginBonusContent"] = []map[string]any{{"id": 1001, "groupLabel": "login", "day": 1, "rewardGroupLabel": "reward"}}
		defs["MasterReward"][0]["masterReasonLabel"] = "reason.synthetic.login"
	})
	ctx := context.Background()
	if err := pg.ImportTables(ctx, uid, map[string][]store.Row{
		"user_profile": {{"_registeredAt": time.Now().Unix(), "_lastDailyUpdatedAt": 0}},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/daily/update", xuid, map[string]any{}))
	}
	assertQuartzHistory(t, pg, uid, nil)
	balance, err := pg.Get(ctx, uid)
	if err != nil || balance.Total() != 0 {
		t.Fatal("unreceived daily gift credited wallet", balance, err)
	}
	tables, err := pg.GetTables(ctx, uid, []string{"user_server_gift"})
	if err != nil || len(tables["user_server_gift"]) != 1 {
		t.Fatal("daily gift missing or duplicated", tables, err)
	}
	gift := tables["user_server_gift"][0]
	if asInt(gift["_masterReasonId"]) != 900 || asInt(gift["_masterRewardId"]) != 600 {
		t.Fatal("login reason did not resolve from MasterReward.masterReasonLabel", gift)
	}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int64{asInt(gift["_giftId"])}}))
	}
	assertQuartzHistory(t, pg, uid, []gem.Transaction{{Type: gem.TransactionIssue, Free: 20, Reason: gem.Reason{Type: gem.ReasonServerGift, MasterReasonID: 900}}})
}

func TestQuartzHistoryLotterySplitRetryAndTickets(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterLottery"][0]["consumeItemId"] = 700
		defs["MasterLottery"][0]["ticketCost"] = 1
		defs["MasterLotteryReward"][0]["rewardCategory"] = 1
		defs["MasterLotteryReward"][0]["rewardId"] = 700
	})
	ctx := context.Background()
	if err := pg.ImportSnapshot(ctx, uid, map[string][]store.Row{"user_item": {{"_id": 700, "_num": 1, "_totalNum": 1}}}, &gem.Balance{Free: 200, Paid: 400}); err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "quartz")
	if err := pg.BeginSession(ctx, uid, token, 1); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "gemCost": 300}
	_, first := postWithSession(t, s, "/api/lottery/draw", xuid, token, 1001, body)
	assertRPCSuccess(t, first)
	_, retry := postWithSession(t, s, "/api/lottery/draw", xuid, token, 1001, body)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("lottery retry changed response")
	}
	_, ticket := postWithSession(t, s, "/api/lottery/draw", xuid, token, 1002, map[string]any{"masterLotteryId": 100, "rewardCount": 1, "consumeType": 2, "ticketCost": 1})
	assertRPCSuccess(t, ticket)
	assertQuartzHistory(t, pg, uid, []gem.Transaction{{Type: gem.TransactionConsume, Free: -200, Paid: -100, Reason: gem.Reason{Type: gem.ReasonLottery, MasterLotteryID: 100}}})
	balance, err := pg.Get(ctx, uid)
	if err != nil || balance.Free != 0 || balance.Paid != 300 {
		t.Fatal(balance, err)
	}
}

func TestQuartzHistoryConcurrentDrawLogsOnlyWinner(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	if _, err := pg.Set(ctx, uid, gem.Balance{Free: 300}); err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "concurrent-history")
	if err := pg.BeginSession(ctx, uid, token, 1); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan map[string]any, 2)
	for _, msgid := range []int64{1001, 1002} {
		go func() {
			var out map[string]any
			defer func() { results <- out }()
			<-start
			_, out = postWithSession(t, s, "/api/lottery/draw", xuid, token, msgid, map[string]any{"masterLotteryId": 100, "rewardCount": 1, "gemCost": 300})
		}()
	}
	close(start)
	succeeded := 0
	for i := 0; i < 2; i++ {
		out := <-results
		if out == nil {
			t.Fatal("concurrent draw returned no response")
		}
		if asInt(out["code"]) == 0 {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatal("concurrent draws did not serialize payment", succeeded)
	}
	assertQuartzHistory(t, pg, uid, []gem.Transaction{{Type: gem.TransactionConsume, Free: -300, Reason: gem.Reason{Type: gem.ReasonLottery, MasterLotteryID: 100}}})
}

func TestQuartzHistoryRecoveryAndFailure(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	if err := pg.ImportSnapshot(ctx, uid, map[string][]store.Row{"user_life": {{"_num": 0}}}, &gem.Balance{Free: 5, Paid: 20}); err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "recover-history")
	if err := pg.BeginSession(ctx, uid, token, 1); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"recoveryNum": 2, "consumedHc": 10}
	_, first := postWithSession(t, s, "/api/life/recover", xuid, token, 1001, body)
	assertRPCSuccess(t, first)
	_, retry := postWithSession(t, s, "/api/life/recover", xuid, token, 1001, body)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("recovery retry changed response")
	}
	want := []gem.Transaction{{Type: gem.TransactionConsume, Free: -5, Paid: -5, Reason: gem.Reason{Type: gem.ReasonLifeRecovery, RecoveryNum: 2}}}
	assertQuartzHistory(t, pg, uid, want)
	_, insufficient := postWithSession(t, s, "/api/life/recover", xuid, token, 1002, map[string]any{"recoveryNum": 1, "consumedHc": 100})
	if asInt(insufficient["code"]) == 0 {
		t.Fatal("insufficient recovery succeeded")
	}
	assertQuartzHistory(t, pg, uid, want)
	if err := pg.ImportTables(ctx, uid, map[string][]store.Row{"user_life": {{"_num": int64(math.MaxInt64)}}}); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/life/recover", xuid, map[string]any{"recoveryNum": 1, "consumedHc": 1})
	if asInt(out["code"]) == 0 {
		t.Fatal("overflowing recovery succeeded")
	}
	assertQuartzHistory(t, pg, uid, want)
}

func TestQuartzHistorySpiritRecoveryDoesNotUseLifeReason(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if _, err := pg.Set(context.Background(), uid, gem.Balance{Free: 100}); err != nil {
		t.Fatal(err)
	}
	assertRPCSuccess(t, post(t, s, "/api/spirit/recover", xuid, map[string]any{"recoveryNum": 1, "consumedHc": 10}))
	assertQuartzHistory(t, pg, uid, []gem.Transaction{{Type: gem.TransactionConsume, Free: -10, Reason: gem.Reason{Type: gem.ReasonNone}}})
}

func TestQuartzHistoryFailedGiftBatchHasNoPartialIncome(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	if err := pg.ImportTables(ctx, uid, map[string][]store.Row{
		"user_server_gift": {
			{"_giftId": 1, "_rewardCategory": 99, "_itemNum": 20, "_masterReasonId": 900, "_isReceived": false},
			{"_giftId": 2, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 1, "_isReceived": false},
		},
		"user_item": {{"_id": 700, "_num": int64(math.MaxInt64), "_totalNum": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := pg.ExportSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int{1, 2}})
	if asInt(out["code"]) == 0 {
		t.Fatal("partial gift batch succeeded")
	}
	assertQuartzHistory(t, pg, uid, nil)
	after, err := pg.ExportSnapshot(ctx, uid)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed gift changed wallet or receipt state", after, err)
	}
}

func TestQuartzHistoryInvalidGiftReasonRollsBackReceipt(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	if err := pg.ImportTables(ctx, uid, map[string][]store.Row{
		"user_server_gift": {
			{"_giftId": 1, "_rewardCategory": 99, "_itemNum": 20, "_masterReasonId": 900, "_isReceived": false},
			{"_giftId": 2, "_rewardCategory": 99, "_itemNum": 20, "_masterReasonId": int64(math.MaxInt32) + 1, "_isReceived": false},
		},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := pg.ExportSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int{1, 2}})
	if asInt(out["code"]) == 0 {
		t.Fatal("reason outside native int32 range was accepted")
	}
	assertQuartzHistory(t, pg, uid, nil)
	after, err := pg.ExportSnapshot(ctx, uid)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("invalid reason committed a receipt or balance", after, err)
	}
}

func TestQuartzHistoryUnmappedFaucetsDoNotInventReasons(t *testing.T) {
	for _, source := range []string{"item_lottery", "stockable"} {
		t.Run(source, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
				defs["MasterReward"] = append(defs["MasterReward"], map[string]any{"id": 603, "label": "stockable-quartz", "groupLabel": "batch", "category": 99, "num": 50})
				defs["MasterStockableRegularReward"] = []map[string]any{{"id": 800, "category": 1, "rewardLabel": "stockable-quartz", "stockPeriod": "weekly", "maxRewardNumPerTerm": 1}}
				defs["MasterRelease"] = []map[string]any{{"id": 801, "label": "weekly", "openAt": "2020/01/01 00:00:00"}}
			})
			if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
				"user_item": {{"_id": 700, "_num": 1, "_totalNum": 1}}, "user_profile": {{"_limitBreakPower": 0}},
			}); err != nil {
				t.Fatal(err)
			}
			route, body := "/api/item/lottery/draw", map[string]any{"masterItemLotteryId": 400, "drawCount": 1}
			if source == "stockable" {
				route, body = "/api/stockable_regular_reward/receive", map[string]any{"category": 1}
			}
			for i := 0; i < 2; i++ {
				out := post(t, s, route, xuid, body)
				if i == 0 || source == "stockable" {
					assertRPCSuccess(t, out)
				}
			}
			assertQuartzHistory(t, pg, uid, []gem.Transaction{{Type: gem.TransactionIssue, Free: 50, Reason: gem.Reason{Type: gem.ReasonNone}}})
		})
	}
}
