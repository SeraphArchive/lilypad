package config

import "testing"

func TestGemValidateCostDefault(t *testing.T) {
	c := &Config{} // unset
	if !c.GemValidateCost() {
		t.Fatal("unset gem.validate_cost should default to true")
	}
	f := false
	c.Gem.ValidateCost = &f
	if c.GemValidateCost() {
		t.Fatal("explicit false not honored")
	}
	tr := true
	c.Gem.ValidateCost = &tr
	if !c.GemValidateCost() {
		t.Fatal("explicit true not honored")
	}
}
