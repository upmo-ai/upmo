//go:build integration

package postgres_test

import (
	"context"
	"testing"
)

func TestTaskBudgetColumnsAreBigintMicroUSD(t *testing.T) {
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
		if typ != "bigint" || def != "0" || nullable != "NO" {
			t.Fatalf("%s = %s default %s nullable %s, want bigint default 0 NO", col, typ, def, nullable)
		}
	}
	var micro int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'tasks' AND table_name = 'tasks'
		  AND column_name IN ('budget_micro_usd', 'held_micro_usd', 'spent_micro_usd')`).Scan(&micro); err != nil {
		t.Fatal(err)
	}
	if micro != 0 {
		t.Fatalf("duplicate micro-USD columns = %d", micro)
	}
	want := map[string]string{
		"tasks_budget_check": "CHECK (((budget >= 0) AND (budget <= 1000000000)))",
		"tasks_held_check":   "CHECK (((held >= 0) AND (held <= budget)))",
		"tasks_spent_check":  "CHECK ((spent >= 0))",
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
	var typ, nullable string
	if err := owner.QueryRow(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'tasks' AND table_name = 'templates' AND column_name = 'budget'`).Scan(&typ, &nullable); err != nil {
		t.Fatal(err)
	}
	if typ != "bigint" || nullable != "NO" {
		t.Fatalf("templates.budget = %s nullable %s", typ, nullable)
	}
	var tpl string
	if err := owner.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'tasks.templates'::regclass AND conname = 'templates_budget_check'`).Scan(&tpl); err != nil {
		t.Fatal(err)
	}
	const wantTpl = "CHECK (((budget >= 1) AND (budget <= 1000000000)))"
	if tpl != wantTpl {
		t.Fatalf("templates_budget_check = %q, want %q", tpl, wantTpl)
	}
}
