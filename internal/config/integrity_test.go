package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectsMisspelledAndInvalidConfiguration(t *testing.T) {
	for _, body := range []string{"listen: ':8443'\nsigning:\n  mode: rssa\n", "lissten: ':8443'\n", "tls:\n  enabled: true\n", "listen: ':8443'\n---\ntls:\n  enabled: true\n", "listen: ':8443'\n---\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
