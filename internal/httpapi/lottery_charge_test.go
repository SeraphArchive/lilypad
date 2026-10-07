package httpapi

import "testing"

func TestGemChargeCost(t *testing.T) {
	// validate=true with an authoritative gem-paid master row: master wins, even
	// if the client declares a different number (anti-cheat).
	if got := gemChargeCost(999999, 300, true, true); got != 300 {
		t.Fatalf("validate uses master cost: %d", got)
	}
	// validate=true, lottery is gem-paid in master with a positive cost, client
	// declares the same: 300.
	if got := gemChargeCost(300, 300, true, true); got != 300 {
		t.Fatalf("validate gem master: %d", got)
	}
	// validate=true but the lottery is unknown/not-gem-paid in the local master
	// snapshot (masterIsGemPaid=false) — e.g. a live banner. Trust the client's
	// declared gemCost so the paid draw still consumes. THIS is the captured case:
	// consumeType:0, gemCost:300, lottery absent from the 6.5 master snapshot.
	if got := gemChargeCost(300, 0, false, true); got != 300 {
		t.Fatalf("live banner trusts client cost: %d", got)
	}
	if got := gemChargeCost(3000, 0, false, true); got != 3000 {
		t.Fatalf("live banner 10-pull trusts client cost: %d", got)
	}
	// Free / ticket / term-stock draws send gemCost:0 -> no charge regardless of
	// validate or master.
	if got := gemChargeCost(0, 0, false, true); got != 0 {
		t.Fatalf("free draw: %d", got)
	}
	// A ticket draw on a lottery that master knows as gem-paid: the client sends
	// gemCost:0 (it spent a ticket), so we must NOT force-charge the master cost.
	if got := gemChargeCost(0, 300, true, true); got != 0 {
		t.Fatalf("ticket draw on a gem banner must charge 0: %d", got)
	}
	// validate=false: trust the client's declared value as-is.
	if got := gemChargeCost(250, 300, true, false); got != 250 {
		t.Fatalf("validate off trusts client: %d", got)
	}
}
