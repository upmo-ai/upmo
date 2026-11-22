//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// commitBetween runs step once, right after the second SELECT on its pool:
// the first is begin's set_config, the second the task row. step commits on
// another pool, so it lands between the task row and the events.
type commitBetween struct {
	mu      sync.Mutex
	selects int
	ran     bool
	step    func()
}

func (c *commitBetween) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (c *commitBetween) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if !strings.HasPrefix(data.CommandTag.String(), "SELECT") {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selects++
	if c.selects == 2 && c.step != nil {
		c.ran = true
		c.step()
	}
}

func tracedStore(t *testing.T, owner *pgxpool.Pool, step func()) (postgres.Store, *commitBetween) {
	t.Helper()
	cfg := pgtest.App(t, owner).Config()
	hook := &commitBetween{step: step}
	cfg.ConnConfig.Tracer = hook
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return postgres.Store{Pool: pool}, hook
}

// A task passes while a read is between the task row and its events. The
// read must see the task before the pass or after it, never the old status
// with the pass event.
func TestReadsSeeOneSnapshotWhenATaskPassesMidRead(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	for _, read := range []struct {
		name string
		read func(postgres.Store, string) (domain.Status, int, error)
	}{
		{"tail", func(s postgres.Store, id string) (domain.Status, int, error) {
			tail, err := s.Tail(ctx, ownerAccess, id, "", 100)
			return tail.State, len(tail.Events), err
		}},
		{"view", func(s postgres.Store, id string) (domain.Status, int, error) {
			view, err := s.View(ctx, ownerAccess, id)
			return view.State, len(view.Events), err
		}},
	} {
		t.Run(read.name, func(t *testing.T) {
			task := reach(t, store, "Pass mid read "+read.name, domain.Checking)
			if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}); err != nil {
				t.Fatal(err)
			}
			_, before, err := read.read(store, task.ID)
			if err != nil {
				t.Fatal(err)
			}

			traced, hook := tracedStore(t, owner, func() {
				if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Pass}); err != nil {
					t.Error(err)
				}
			})
			state, events, err := read.read(traced, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !hook.ran {
				t.Fatal("the pass never ran mid read")
			}
			switch {
			case state == domain.Checking && events == before:
			case state == domain.Done && events == before+1:
			default:
				t.Fatalf("read status %s with %d events, before the pass there were %d: one snapshot shows Checking with %d or Done with %d",
					state, events, before, before, before+1)
			}
		})
	}
}
