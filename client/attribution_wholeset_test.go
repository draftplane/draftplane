package client_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/client/localfs"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/reanchor"
)

// TestEveryWriteMethodRequiresAttribution calls every attribution-bearing
// write method of localfs.Store with an undecorated context.Background() and
// proves each fails closed with client.ErrNoAttribution rather than stamping a
// zero-value attribution.
//
// The table is enumerated by name rather than derived from
// client.PlanService's method set, so a write method added later and
// forgotten here fails this list instead of being silently skipped.
//
// localfs.Store carries five attribution-bearing writes (CreatePlan,
// RegisterVersion, CreateThread, Reply, Approve). Add a method here only if it
// reads client.AttributionFrom.
func TestEveryWriteMethodRequiresAttribution(t *testing.T) {
	anchor := reanchor.Anchor{HeadingPath: []string{"Intro"}, Span: "hello world"}

	store, err := localfs.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	attrCtx := client.WithAttribution(context.Background(), domain.Attribution{ActorLogin: "alice-codes", ActorDisplay: "alice"})
	plan, err := store.CreatePlan(attrCtx, "Plan", "/plan.md", "", []byte("v1"))
	if err != nil {
		t.Fatalf("seeding a plan: %v", err)
	}
	thread, err := store.CreateThread(attrCtx, plan.ID, "v1", anchor, "seed comment")
	if err != nil {
		t.Fatalf("seeding a thread: %v", err)
	}

	tests := []struct {
		name string
		call func(ctx context.Context) error
	}{
		// An empty sourceHint never matches an existing plan (CreatePlan's
		// never-equal rule), so this reaches the stamp rather than
		// short-circuiting on return-existing semantics.
		{"localfs CreatePlan", func(ctx context.Context) error {
			_, err := store.CreatePlan(ctx, "Another Plan", "", "", []byte("v-other"))
			return err
		}},
		// "v2" against the seeded plan's real tip is neither the no-op arm nor
		// the conflict arm, so this reaches the stamp rather than returning
		// before consulting ctx.
		{"localfs RegisterVersion", func(ctx context.Context) error {
			_, err := store.RegisterVersion(ctx, plan.ID, client.VersionRegistration{
				Content: []byte("v2"), Base: domain.HashContent([]byte("v1")),
			})
			return err
		}},
		{"localfs CreateThread", func(ctx context.Context) error {
			_, err := store.CreateThread(ctx, plan.ID, "v1", anchor, "body")
			return err
		}},
		{"localfs Reply", func(ctx context.Context) error {
			_, err := store.Reply(ctx, plan.ID, thread.ID, "body")
			return err
		}},
		{"localfs Approve", func(ctx context.Context) error {
			return store.Approve(ctx, plan.ID, "v1")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(context.Background()); !errors.Is(err, client.ErrNoAttribution) {
				t.Fatalf("%s(context.Background()) err = %v, want errors.Is(err, client.ErrNoAttribution)", tc.name, err)
			}
		})
	}
}
