// Package testdb gives each integration-test process its own disposable schema.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"strings"
	"time"
)

func Run(run func() int) int {
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	if dsn == "" {
		return run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test database connection failed:", err)
		return 1
	}
	defer conn.Close(context.Background())
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	schema := "lilypad_test_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		fmt.Fprintln(os.Stderr, "test schema creation failed:", err)
		return 1
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	}()
	isolated := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return 1
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		isolated = u.String()
	}
	_ = os.Setenv("LILYPAD_TEST_DSN", isolated)
	return run()
}
