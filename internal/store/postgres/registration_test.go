package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"lilypad/internal/model"
)

func TestNewAccountRegistrationTimeIsStableAndSeedIsReusable(t *testing.T) {
	ctx := context.Background()
	seed := model.NewPlayerSeed()
	s := freshStore(t, seed)
	for i := 0; i < 2; i++ {
		a, err := s.GetOrCreate(ctx, fmt.Sprintf("registration-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		var expected int64
		if err = s.pool.QueryRow(ctx, `SELECT floor(extract(epoch FROM created_at))::bigint FROM accounts WHERE user_id=$1`, a.UserID).Scan(&expected); err != nil {
			t.Fatal(err)
		}
		rows, err := s.GetTables(ctx, a.UserID, []string{"user_profile"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := rows["user_profile"][0]["_registeredAt"].(json.Number).Int64()
		if err != nil || got != expected || got <= 0 {
			t.Fatal("new account has no real registration time", got, expected, err)
		}
		hashes, err := s.AllHashes(ctx, a.UserID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.GetOrCreate(ctx, fmt.Sprintf("registration-%d", i)); err != nil {
			t.Fatal(err)
		}
		next, err := s.AllHashes(ctx, a.UserID)
		if err != nil || next["user_profile"] != hashes["user_profile"] {
			t.Fatal("login reset registration time", err)
		}
	}
	if seed["user_profile"][0]["_registeredAt"] != json.Number("0") {
		t.Fatal("account creation mutated the shared seed")
	}
}

func TestRegistrationMigrationPreservesSaveAndArchivesRepair(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0009_registration_time'`); err != nil {
		t.Fatal(err)
	}
	for i, registered := range []int64{0, 1600000000} {
		a, err := s.GetOrCreate(ctx, fmt.Sprintf("repair-registration-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.pool.Exec(ctx, `UPDATE accounts SET created_at=to_timestamp(1700000000) WHERE user_id=$1`, a.UserID); err != nil {
			t.Fatal(err)
		}
		if err = s.SeedNewPlayer(ctx, a.UserID, map[string][]Row{"user_profile": {{"_registeredAt": registered, "_future": map[string]any{"preserve": 99}}}, "user_story": {{"_masterStoryId": 10, "_isClear": true}}}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Add(ctx, a.UserID, 123); err != nil {
			t.Fatal(err)
		}
		if err = s.BeginSession(ctx, a.UserID, "pre-registration-repair", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	for i, expected := range []int64{1700000000, 1600000000} {
		a, err := s.GetOrCreate(ctx, fmt.Sprintf("repair-registration-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := s.ExportSnapshot(ctx, a.UserID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := snap.Tables["user_profile"][0]["_registeredAt"].(json.Number).Int64()
		if err != nil || got != expected || snap.Balance.Free != 123 || len(snap.Tables["user_story"]) != 1 || snap.Tables["user_profile"][0]["_future"].(map[string]any)["preserve"] != json.Number("99") {
			t.Fatal("repair changed unrelated save data", snap, err)
		}
		if _, err = s.CheckSession(ctx, a.UserID, "pre-registration-repair", 10000); err == nil {
			t.Fatal("pre-repair process still admitted")
		}
		if i == 0 {
			var n int
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1 AND table_name='user_profile' AND source='server' AND old_data->0->>'_registeredAt'='0' AND new_data->0->>'_registeredAt'='1700000000'`, a.UserID).Scan(&n); err != nil || n != 1 {
				t.Fatal("repair not recoverable", n, err)
			}
		}
		hash := snap.Hashes["user_profile"]
		if err = Migrate(ctx, s.pool); err != nil {
			t.Fatal(err)
		}
		next, err := s.AllHashes(ctx, a.UserID)
		if err != nil || next["user_profile"] != hash {
			t.Fatal("repair repeated", err)
		}
	}
}
