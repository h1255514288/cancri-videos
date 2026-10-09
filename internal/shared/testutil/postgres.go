//go:build integration

package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

func Postgres(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("VDS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("integration test requires VDS_TEST_DATABASE_URL pointing to a dedicated local test database")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("VDS_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatal("integration tests accept local loopback databases only")
	}
	if u.Path == "" || u.Path == "/" {
		t.Fatal("an explicit test database name is required")
	}
	for key := range u.Query() {
		if key != "sslmode" && key != "connect_timeout" {
			t.Fatal("test database URL supports only sslmode and connect_timeout query parameters")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := sqlx.ConnectContext(ctx, "postgres", dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	var random [12]byte
	rand.Read(random[:])
	schema := "vds_test_" + hex.EncodeToString(random[:])
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE"); err != nil {
			t.Errorf("cleanup test schema %s: %v", schema, err)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sqlx.ConnectContext(ctx, "postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(24)
	db.SetMaxIdleConns(24)
	t.Cleanup(func() { db.Close() })
	var currentSchema string
	if err := db.GetContext(ctx, &currentSchema, `SELECT current_schema()`); err != nil || currentSchema != schema {
		t.Fatal("test connection is not confined to the expected isolated schema")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate initialization schema")
	}
	sqlBytes, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "..", "scripts", "init-db.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("initialize isolated test schema: %v", err)
	}
	return db
}
