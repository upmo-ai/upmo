//go:build integration

package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUPM387_JsonbTextSizeMatchesPostgres(t *testing.T) {
	ctx := context.Background()
	dbURL := os.Getenv("UPMO_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("UPMO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	maps := []map[string]string{
		{},
		{"a": "x"},
		{"quote\"slash\\": "quote\"slash\\"},
		{"unicode": "line\u2028separator"},
		{"literal": `line\u2028separator`},
		{"a": strings.Repeat("x", 65527)},
		{"a": strings.Repeat("x", 65528)},
	}
	for i, models := range maps {
		got, err := jsonbTextSize(models)
		if err != nil {
			t.Fatalf("map %d size: %v", i, err)
		}
		encoded, err := json.Marshal(models)
		if err != nil {
			t.Fatal(err)
		}
		var want int
		if err := pool.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, string(encoded)).Scan(&want); err != nil {
			t.Fatalf("map %d postgres size: %v", i, err)
		}
		if got != want {
			t.Errorf("map %d size = %d, Postgres = %d", i, got, want)
		}
		if i == 5 && want != 65536 || i == 6 && want != 65537 {
			t.Errorf("boundary map %d Postgres size = %d", i, want)
		}
	}
}
