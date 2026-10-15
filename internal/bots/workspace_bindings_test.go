package bots

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// fakeWorkspaceBindings records the lifecycle calls in order so tests can
// check that the managed workspace is looked up before the bot row goes and
// released after.
type fakeWorkspaceBindings struct {
	managed    []string
	listErr    error
	releaseErr error
	calls      *[]string
}

func (f *fakeWorkspaceBindings) BindManaged(_ context.Context, _ dbstore.Queries, botID, ownerUserID, name string) error {
	*f.calls = append(*f.calls, "bind:"+botID+":"+ownerUserID+":"+name)
	return nil
}

func (f *fakeWorkspaceBindings) ManagedWorkspaceIDs(_ context.Context, _ dbstore.Queries, botID string) ([]string, error) {
	*f.calls = append(*f.calls, "list:"+botID)
	return f.managed, f.listErr
}

func (f *fakeWorkspaceBindings) ReleaseUnbound(_ context.Context, _ dbstore.Queries, workspaceID string) error {
	*f.calls = append(*f.calls, "release:"+workspaceID)
	return f.releaseErr
}

func newDeleteLifecycleService(calls *[]string, bindings *fakeWorkspaceBindings) *Service {
	db := &fakeDBTX{
		execFunc: func(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "SET status = $2"):
				*calls = append(*calls, "status:"+args[1].(string))
			case strings.Contains(query, "DELETE FROM bots"):
				*calls = append(*calls, "delete")
			}
			return pgconn.CommandTag{}, nil
		},
	}
	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	svc.SetWorkspaceIntents(&fakeWorkspaceIntents{outcome: WorkspaceOutcome{Desired: "absent", Observed: WorkspaceObservedAbsent}})
	svc.SetWorkspaceBindings(bindings)
	return svc
}

func TestRunDeleteLifecycleReleasesManagedWorkspaceAfterBotRow(t *testing.T) {
	botID := mustParseUUID("00000000-0000-0000-0000-000000000002").String()
	const workspaceID = "00000000-0000-0000-0000-000000000077"
	var calls []string
	svc := newDeleteLifecycleService(&calls, &fakeWorkspaceBindings{managed: []string{workspaceID}, calls: &calls})

	if err := svc.runDeleteLifecycle(context.Background(), botID, BotStatusReady); err != nil {
		t.Fatalf("runDeleteLifecycle() error = %v", err)
	}
	want := []string{"list:" + botID, "delete", "release:" + workspaceID}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("lifecycle calls = %v, want %v", calls, want)
	}
}

func TestRunDeleteLifecycleRevertsWhenManagedWorkspaceCannotBeListed(t *testing.T) {
	botID := mustParseUUID("00000000-0000-0000-0000-000000000002").String()
	var calls []string
	svc := newDeleteLifecycleService(&calls, &fakeWorkspaceBindings{listErr: errors.New("store went away"), calls: &calls})

	err := svc.runDeleteLifecycle(context.Background(), botID, BotStatusReady)
	if err == nil || !strings.Contains(err.Error(), "list managed workspaces") {
		t.Fatalf("runDeleteLifecycle() error = %v, want the listing failure", err)
	}
	for _, call := range calls {
		if call == "delete" {
			t.Fatalf("bot row must not be deleted when its workspaces cannot be listed; calls=%v", calls)
		}
	}
	if calls[len(calls)-1] != "status:"+BotStatusReady {
		t.Fatalf("a failed delete must revert the status; calls=%v", calls)
	}
}

func TestRunDeleteLifecycleWithoutBindingsStillDeletesBotRow(t *testing.T) {
	botID := mustParseUUID("00000000-0000-0000-0000-000000000002").String()
	var calls []string
	svc := newDeleteLifecycleService(&calls, nil)
	svc.SetWorkspaceBindings(nil)

	if err := svc.runDeleteLifecycle(context.Background(), botID, BotStatusReady); err != nil {
		t.Fatalf("runDeleteLifecycle() error = %v", err)
	}
	if len(calls) != 1 || calls[0] != "delete" {
		t.Fatalf("calls = %v, want only the bot row delete", calls)
	}
}
