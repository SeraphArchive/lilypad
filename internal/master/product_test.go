package master

import "testing"

func TestProductListDefaultLadder(t *testing.T) {
	products := (*Data)(nil).ProductList(0)
	if len(products) != len(DefaultSteamGemSKUs) {
		t.Fatalf("default ladder %d want %d", len(products), len(DefaultSteamGemSKUs))
	}
	first := products[0]
	if first["product_id"] != "115093001" || first["charge_gem"] != "130" || first["formatted_price"] != "¥160" {
		t.Fatalf("first sku = %v", first)
	}
	if products[2]["formatted_price"] != "¥1,000" {
		t.Fatalf("comma yen = %v", products[2]["formatted_price"])
	}
}

func TestFormatYen(t *testing.T) {
	if got := formatYen(160); got != "¥160" {
		t.Fatal(got)
	}
	if got := formatYen(10000); got != "¥10,000" {
		t.Fatal(got)
	}
}
