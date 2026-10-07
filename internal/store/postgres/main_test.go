package postgres

import (
	"lilypad/internal/testdb"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m.Run)) }
