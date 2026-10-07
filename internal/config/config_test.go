package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndEnv(t *testing.T) {
	p := filepath.Join("testdata", "min.yaml")
	t.Setenv("LILYPAD_DB_DSN", "postgres://env")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8443" {
		t.Fatalf("default listen wrong: %q", c.Listen)
	}
	if c.DB.DSN != "postgres://env" {
		t.Fatalf("env override failed: %q", c.DB.DSN)
	}
	if c.Signing.Mode != "noop" {
		t.Fatalf("default signing mode wrong: %q", c.Signing.Mode)
	}
	if c.Portal.RequireEmailVerification {
		t.Fatal("verification must default off")
	}
}

func TestLoadFromFileNoEnv(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "min.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DB.DSN != "postgres://file" {
		t.Fatalf("file value lost: %q", c.DB.DSN)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(os.TempDir(), "nope-lilypad-xyz.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestExampleConfigParses(t *testing.T) {
	// The committed example must always be valid.
	c, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml must parse: %v", err)
	}
	if c.Portal.RequireEmailVerification {
		t.Fatal("example must default verification off")
	}
}

func TestExplicitEmptyDataDirOverrideDisablesMasterData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("data_dir: configured-master\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LILYPAD_DATA_DIR", "")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "" {
		t.Fatalf("explicit empty master override retained config value %q", c.DataDir)
	}
}
