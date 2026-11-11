//go:build integration

package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// noticesRig migrates audit, identity and notices on a fresh database and
// returns the owner pool and the request pool that runs as upmo_app.
func noticesRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

func TestNotificationDismissSealsItsEvent(t *testing.T) {
	owner, appPool := noticesRig(t)
	sealed := newSealer(t, owner)
	store := notices.Store{Pool: appPool}
	ctx := context.Background()

	user := uuid.Must(uuid.NewV7())
	notice, err := store.Insert(ctx, user, notices.KindProposal, "File the receipts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("an insert sealed %v, want nothing: proposals arrive on their own", actions(rows))
	}

	found, err := store.Dismiss(ctx, user, notice)
	if err != nil || !found {
		t.Fatalf("dismiss = %t %v", found, err)
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.NotificationDismiss {
		t.Fatalf("events = %v, want one notification.dismiss", actions(rows))
	}
	row := rows[0]
	if row.TargetID != notice.String() {
		t.Fatalf("target = %s, want the dismissed notice %s", row.TargetID, notice)
	}
	if row.ActorType != "system" || row.CredentialType != "none" {
		t.Fatalf("actor = %s %s, want the system fallback of a writer with no request", row.ActorType, row.CredentialType)
	}
}

func TestNoticeDismissNoOpsLeaveNoEvent(t *testing.T) {
	owner, appPool := noticesRig(t)
	sealed := newSealer(t, owner)
	store := notices.Store{Pool: appPool}
	ctx := context.Background()

	user := uuid.Must(uuid.NewV7())
	notice, err := store.Insert(ctx, user, notices.KindProposal, "File the receipts", nil)
	if err != nil {
		t.Fatal(err)
	}
	found, err := store.Dismiss(ctx, user, notice)
	if err != nil || !found {
		t.Fatalf("dismiss = %t %v", found, err)
	}
	if rows := sealed(); len(rows) != 1 || rows[0].Action != audit.NotificationDismiss {
		t.Fatalf("the dismiss itself sealed %v, want one notification.dismiss", actions(rows))
	}

	// A repeat dismiss keeps the first time and answers found, and changes
	// nothing, so it leaves no event behind.
	found, err = store.Dismiss(ctx, user, notice)
	if err != nil || !found {
		t.Fatalf("repeat dismiss = %t %v", found, err)
	}
	// Another person's notice and an unknown one answer not found, and leave
	// no event either.
	other := uuid.Must(uuid.NewV7())
	if found, err = store.Dismiss(ctx, other, notice); err != nil || found {
		t.Fatalf("another person's notice = %t %v", found, err)
	}
	if found, err = store.Dismiss(ctx, user, uuid.Must(uuid.NewV7())); err != nil || found {
		t.Fatalf("unknown notice = %t %v", found, err)
	}
	for _, row := range sealed() {
		if row.Action == audit.NotificationDismiss && row.Outcome == audit.Success {
			t.Fatalf("a no-op dismiss sealed %s success for %s, want none on audit.events",
				row.Action, row.TargetID)
		}
	}
}

func TestNoticeRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := noticesRig(t)
	sealed := newSealer(t, owner)
	store := notices.Store{Pool: appPool}
	ctx := context.Background()

	user := uuid.Must(uuid.NewV7())
	notice, err := store.Insert(ctx, user, notices.KindProposal, "File the receipts", nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed()

	mux := http.NewServeMux()
	notices.Handler{
		List:    store,
		Dismiss: store,
		User: func(r *http.Request) (uuid.UUID, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: user.String(), CredentialType: "session"})
			return user, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/notifications/"+notice.String(), nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.NotificationDismiss {
		t.Fatalf("events = %v, want one notification.dismiss", actions(rows))
	}
	row := rows[0]
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the handler's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != user.String() || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
}

func TestNoticeRouteRefusalRecordsDenied(t *testing.T) {
	owner, appPool := noticesRig(t)
	sealed := newSealer(t, owner)
	store := notices.Store{Pool: appPool}

	mux := http.NewServeMux()
	notices.Handler{
		List:    store,
		Dismiss: store,
		// No session, no person: the handler refuses.
		User: func(r *http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/notifications/"+uuid.Must(uuid.NewV7()).String(), nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a person with no session = %d %s, want 401", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.NotificationDismiss {
		t.Fatalf("events = %v, want one denied notification.dismiss", actions(rows))
	}
	if rows[0].Outcome != audit.Denied {
		t.Fatalf("outcome = %s, want denied", rows[0].Outcome)
	}
	if rows[0].ActorType != "anonymous" {
		t.Fatalf("actor = %s, want anonymous: no person was known", rows[0].ActorType)
	}
}
