package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"lilypad/internal/store"
)

func TestWeeklyMissionBatchSettlementPreservesProgressAndRetries(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterReward"][0] = map[string]any{"id": 600, "groupLabel": "reward", "category": 22, "num": 30}
		defs["MasterMission"] = append(defs["MasterMission"], map[string]any{"id": 501, "masterRewardGroupLabel": "batch"})
	})
	expires := time.Now().Unix() + 86400
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{
		"user_profile": {{"_limitBreakPower": 0}},
		"user_mission": {{"_masterMissionId": 500, "_state": 2, "_progress": 1, "_expireAt": expires, "_isDisplayed": false, "_future": 99}},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/mission/receive", xuid, map[string]any{"masterMissionIds": []int{500, 501, 500}}))
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	missions := indexByInt(snapshot.Tables["user_mission"], "_masterMissionId")
	if len(missions) != 2 || asInt(missions[500]["_state"]) != 3 || asInt(missions[500]["_expireAt"]) != expires || asInt(missions[500]["_progress"]) != 1 || asInt(missions[500]["_future"]) != 99 || asBool(missions[500]["_isDisplayed"]) {
		t.Fatal("weekly settlement lost the received mission or its period", missions)
	}
	if asInt(snapshot.Tables["user_profile"][0]["_limitBreakPower"]) != 3 || itemCount(snapshot.Tables["user_item"], 700) != 1 {
		t.Fatal("batch rewards missing or repeated", snapshot.Tables)
	}
}

func TestTermStockBatchDebitAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []int64
		cost   int64
		left   []int64
	}{
		{"one_per_instance", []int64{1, 1, 1, 1, 1}, 5, []int64{0, 0, 0, 0, 0}},
		{"partial_last_instance", []int64{3, 3}, 4, []int64{0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pg, xuid, uid := newIntegrityServer(t)
			rows := []store.Row{{"_uid": "unselected", "_value": 9}}
			uids := []string{}
			for i, value := range tc.values {
				id := string(rune('a' + i))
				uids = append(uids, id)
				rows = append(rows, store.Row{"_uid": id, "_value": value, "_future": 99})
			}
			if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_term_stock_item": rows}); err != nil {
				t.Fatal(err)
			}
			token := testSessionToken(xuid + "term")
			if err := pg.BeginSession(context.Background(), uid, token, 1); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "termStockItemCost": tc.cost, "termStockItemUids": uids}
			_, first := postWithSession(t, s, "/api/lottery/draw", xuid, token, 1001, body)
			assertRPCSuccess(t, first)
			_, retry := postWithSession(t, s, "/api/lottery/draw", xuid, token, 1001, body)
			assertRPCSuccess(t, retry)
			a, _ := json.Marshal(first)
			b, _ := json.Marshal(retry)
			if string(a) != string(b) {
				t.Fatal("retry changed its receipt")
			}
			got, err := pg.GetTables(context.Background(), uid, []string{"user_term_stock_item", "user_lottery"})
			if err != nil {
				t.Fatal(err)
			}
			if len(got["user_term_stock_item"]) != len(rows) || asInt(got["user_lottery"][0]["_drawCount"]) != 1 {
				t.Fatal("retry debited or drew twice", got)
			}
			for _, row := range got["user_term_stock_item"] {
				if row["_uid"] == "unselected" {
					if asInt(row["_value"]) != 9 {
						t.Fatal("unselected balance changed")
					}
					continue
				}
				for i, id := range uids {
					if row["_uid"] == id && (asInt(row["_value"]) != tc.left[i] || asInt(row["_future"]) != 99) {
						t.Fatal("wrong aggregate debit", got)
					}
				}
			}
		})
	}
}

func TestTermStockInvalidBatchRollsBack(t *testing.T) {
	for _, uids := range [][]string{{"a", "a"}, {"a", "missing"}, {"a"}, nil} {
		s, pg, xuid, uid := newIntegrityServer(t)
		if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_term_stock_item": {{"_uid": "a", "_value": 1}}}); err != nil {
			t.Fatal(err)
		}
		before, err := pg.ExportSnapshot(context.Background(), uid)
		if err != nil {
			t.Fatal(err)
		}
		out := post(t, s, "/api/lottery/draw", xuid, map[string]any{"masterLotteryId": 100, "rewardCount": 1, "termStockItemCost": 2, "termStockItemUids": uids})
		if asInt(out["code"]) == 0 {
			t.Fatal("invalid payment accepted", uids)
		}
		after, err := pg.ExportSnapshot(context.Background(), uid)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Tables, after.Tables) || !reflect.DeepEqual(before.Hashes, after.Hashes) || before.Balance != after.Balance {
			t.Fatal("failed payment changed state")
		}
	}
}

func TestConcurrentTermStockDrawCannotOverspend(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_term_stock_item": {{"_uid": "a", "_value": 3}, {"_uid": "b", "_value": 3}}}); err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "concurrent")
	if err := pg.BeginSession(context.Background(), uid, token, 1); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "termStockItemCost": 4, "termStockItemUids": []string{"a", "b"}}
	type result struct {
		msgid  int64
		status int
		body   map[string]any
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, msgid := range []int64{1001, 1002} {
		go func() {
			r := result{msgid: msgid}
			defer func() { results <- r }()
			<-start
			r.status, r.body = postWithSession(t, s, "/api/lottery/draw", xuid, token, msgid, body)
		}()
	}
	close(start)
	succeeded := 0
	var winner result
	for i := 0; i < 2; i++ {
		r := <-results
		if r.status != 200 || r.body == nil {
			t.Fatal("concurrent request failed at the transport", r)
		}
		if asInt(r.body["code"]) == 0 {
			succeeded++
			winner = r
		}
	}
	if succeeded != 1 {
		t.Fatal("concurrent draws did not serialize payment", succeeded)
	}
	_, retry := postWithSession(t, s, "/api/lottery/draw", xuid, token, winner.msgid, body)
	if !reflect.DeepEqual(winner.body, retry) {
		t.Fatal("concurrent winner lost its receipt")
	}
	got, err := pg.GetTables(context.Background(), uid, []string{"user_term_stock_item", "user_lottery"})
	if err != nil {
		t.Fatal(err)
	}
	var remaining int64
	for _, row := range got["user_term_stock_item"] {
		remaining += asInt(row["_value"])
	}
	if remaining != 2 || len(got["user_lottery"]) != 1 || asInt(got["user_lottery"][0]["_drawCount"]) != 1 {
		t.Fatal("concurrent draw changed inventory twice", got)
	}
}
