package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// web/shop/product/list serves the configured web-shop product rows and never
// reports a content hash for the table (empty-string hash; the table is absent
// from confirm).
func TestWebShopProductList(t *testing.T) {
	s := newSeededEconomyServer(t)
	s.webShopProducts = []map[string]any{{
		"_masterGamelibProductId": 115000000, "_campaignType": 0, "_campaignMode": 0,
		"_priority": 1, "_consumable": 0, "_endDatetime": "", "_limitedDatetime": "",
		"_limitedCount": 1,
	}}
	xuid := fmt.Sprintf("webshop-%d", time.Now().UnixNano())
	out := post(t, s, "/api/web/shop/product/list", xuid, map[string]any{"hdr": "", "dummy": 0})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("product list failed: %v", out)
	}
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows, ok := put["user_web_shop_product"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 product row, got %v", put)
	}
	if rows[0].(map[string]any)["_masterGamelibProductId"].(json.Number).String() != "115000000" {
		t.Fatalf("wrong product row: %v", rows[0])
	}
	if out["hashes"].(map[string]any)["user_web_shop_product"] != "" {
		t.Fatalf("expected empty-string hash, got %v", out["hashes"])
	}

	// Default (unconfigured) server serves an empty row list.
	s2 := newSeededEconomyServer(t)
	out = post(t, s2, "/api/web/shop/product/list", fmt.Sprintf("webshop2-%d", time.Now().UnixNano()), map[string]any{"hdr": ""})
	put = out["tables"].(map[string]any)["putItems"].(map[string]any)
	if rows, ok := put["user_web_shop_product"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("expected empty product list, got %v", put)
	}
}
