//go:build integration

package postgres_test

import (
	"context"
	"testing"
)

// UPM-403 leaves the whole-credit budget columns to UPM-402: their type,
// default and checks must match the pre-reset schema exactly.
func TestTaskBudgetColumnsAreUnchangedByTheSchemaReset(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	for _, col := range []string{"budget", "held", "spent"} {
		var typ, def, nullable string
		if err := owner.QueryRow(ctx, `
			SELECT data_type, column_default, is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'tasks' AND table_name = 'tasks' AND column_name = $1`, col).Scan(&typ, &def, &nullable); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if typ != "integer" || def != "0" || nullable != "NO" {
			t.Fatalf("%s = %s default %s nullable %s, want integer default 0 NO", col, typ, def, nullable)
		}
	}
	want := map[string]string{
		"tasks_budget_check": "CHECK (((budget >= 0) AND (budget <= 1000000)))",
		"tasks_held_check":   "CHECK (((held >= 0) AND (held <= budget)))",
		"tasks_spent_check":  "CHECK (((spent >= 0) AND (spent <= budget)))",
	}
	for name, def := range want {
		var got string
		if err := owner.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = 'tasks.tasks'::regclass AND conname = $1`, name).Scan(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != def {
			t.Fatalf("%s = %q, want %q", name, got, def)
		}
	}
}
