package model

import "testing"

func TestCollectionKey(t *testing.T) {
	s := SpecFor("user_card")
	if s.Kind != Collection || len(s.Key) != 1 || s.Key[0] != "_masterCardId" {
		t.Fatalf("user_card spec wrong: %+v", s)
	}
}

func TestCompositeKey(t *testing.T) {
	s := SpecFor("user_slot")
	if s.Kind != Collection || len(s.Key) != 2 || s.Key[0] != "_deckIndex" || s.Key[1] != "_slotIndex" {
		t.Fatalf("user_slot composite expected: %+v", s)
	}
}

func TestUnknownIsSingleton(t *testing.T) {
	if SpecFor("user_version").Kind != Singleton {
		t.Fatal("unknown/singleton expected")
	}
}

func TestCollectionCount(t *testing.T) {
	n := 0
	for _, s := range Keys() {
		if s.Kind == Collection {
			n++
		}
	}
	if n != 190 {
		t.Fatalf("expected 190 collection tables, got %d", n)
	}
}

func TestWorldStateIsCollectionOnIndexDepth(t *testing.T) {
	s := SpecFor("user_world_state")
	if s.Kind != Collection || len(s.Key) != 2 || s.Key[0] != "_index" || s.Key[1] != "_depth" {
		t.Fatalf("user_world_state spec wrong: %+v", s)
	}
}

func TestClientGiftIsCollection(t *testing.T) {
	s := SpecFor("user_client_gift")
	if s.Kind != Collection || len(s.Key) != 1 || s.Key[0] != "_giftId" {
		t.Fatalf("user_client_gift spec wrong: %+v", s)
	}
}

func TestSkillIsCollectionOnMasterSkillId(t *testing.T) {
	s := SpecFor("user_skill")
	if s.Kind != Collection || len(s.Key) != 1 || s.Key[0] != "_masterSkillId" {
		t.Fatalf("user_skill spec wrong: %+v", s)
	}
}

func TestFieldEnemyIsCollectionOnWorldStateAndEnemy(t *testing.T) {
	s := SpecFor("user_field_enemy")
	if s.Kind != Collection || len(s.Key) != 2 || s.Key[0] != "_worldStateKey" || s.Key[1] != "_masterFieldEnemyId" {
		t.Fatalf("user_field_enemy spec wrong: %+v", s)
	}
}

func TestLatentAbilityIsCollectionOnCharacterAndAbility(t *testing.T) {
	s := SpecFor("user_latent_ability")
	if s.Kind != Collection || len(s.Key) != 2 || s.Key[0] != "_masterCharacterId" || s.Key[1] != "_masterLatentAbilityId" {
		t.Fatalf("user_latent_ability spec wrong: %+v", s)
	}
}

func TestLiveIsCollectionOnMasterLiveId(t *testing.T) {
	s := SpecFor("user_live")
	if s.Kind != Collection || len(s.Key) != 1 || s.Key[0] != "_masterLiveId" {
		t.Fatalf("user_live spec wrong: %+v", s)
	}
}
