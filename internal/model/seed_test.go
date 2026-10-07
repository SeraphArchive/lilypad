package model

import (
	"encoding/json"
	"testing"
)

func TestNewPlayerSeedLoads(t *testing.T) {
	seed := NewPlayerSeed()
	if len(seed) != 231 {
		t.Fatalf("expected full table set (231), got %d", len(seed))
	}
	for _, name := range []string{
		"user_current_endless_expedition",
		"user_endless_expedition_enchant_card",
		"user_endless_expedition_fork_event",
		"user_endless_expedition_group",
		"user_home_banner",
		"user_latent_ability",
		"user_lottery_encore",
		"user_score_attack_ex",
	} {
		if _, ok := seed[name]; !ok {
			t.Fatalf("seed missing 6.9.0 table %s", name)
		}
	}
	if len(seed["user_card"]) != 6 {
		t.Fatalf("expected 6 starter cards, got %d", len(seed["user_card"]))
	}
	if len(seed["user_profile"]) != 1 {
		t.Fatalf("expected 1 profile row, got %d", len(seed["user_profile"]))
	}
	// Numbers must decode as json.Number so seed hashes match read-back hashes.
	if _, ok := seed["user_card"][0]["_masterCardId"].(json.Number); !ok {
		t.Fatalf("_masterCardId not json.Number: %T", seed["user_card"][0]["_masterCardId"])
	}
}

func TestSeedExcludesWriteOnlyTable(t *testing.T) {
	if _, ok := NewPlayerSeed()["user_reward_group_log"]; ok {
		t.Fatal("write-only user_reward_group_log must not be in the seed")
	}
}

func TestSeedIsAnonymized(t *testing.T) {
	p := NewPlayerSeed()["user_profile"][0]
	for _, k := range []string{"_userName", "_comment", "_country", "_language"} {
		if v, _ := p[k].(string); v != "" {
			t.Fatalf("profile field %s must be blank, got %q", k, v)
		}
	}
}
