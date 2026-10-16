package workdir

import (
	"context"
	"errors"
	"strings"
	"testing"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Branch switching serves the external-agent composer, so it needs a computer
// that can host those agents. A user's own computer cannot: the switch is
// refused as unavailable before any Git command runs, as it was when the
// check was "not the native target".
func TestSwitchGitBranchRequiresExternalAgentCapability(t *testing.T) {
	svc := &statTestContainerService{}
	client := newStatTestClient(t, svc)
	store := &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p2", BotID: "b1", WorkspaceID: "ws-user", WorkspaceKind: dbstore.WorkspaceKindUser, BindingID: "bind-1", Path: "/Users/alice/code",
	}}
	service := &Service{
		store: store,
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID:     "bind-1",
			Kind:         TargetKindRemote,
			WorkspaceID:  "ws-user",
			Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindUser),
			Client:       client,
			Info:         bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, OS: "darwin"},
		}},
	}
	_, err := service.SwitchGitBranch(context.Background(), "b1", "p2", "main")
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("SwitchGitBranch on a user computer error = %v, want ErrGitUnavailable", err)
	}
	if strings.Contains(err.Error(), "activity store") {
		t.Fatalf("capability guard did not fire first: %v", err)
	}
	if len(svc.seen) != 0 || len(svc.listed) != 0 {
		t.Fatalf("bridge was contacted before the capability guard: stat %v, list %v", svc.seen, svc.listed)
	}
}

// A managed computer passes the capability guard; what fails next here is the
// test store, which has no activity tracking, proving the guard let it
// through.
func TestSwitchGitBranchPassesCapabilityGuardOnManagedComputer(t *testing.T) {
	client := newStatTestClient(t, &statTestContainerService{})
	store := &fakeWorkdirStore{get: dbstore.BotWorkdirRecord{
		ID: "p1", BotID: "b1", WorkspaceID: "ws-managed", WorkspaceKind: dbstore.WorkspaceKindManaged, Path: "/data/site",
	}}
	service := &Service{
		store: store,
		targets: &fakeTargetResolver{target: workspace.ResolvedWorkspaceTarget{
			TargetID:     workspace.WorkspaceTargetNative,
			Kind:         TargetKindNative,
			WorkspaceID:  "ws-managed",
			Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindManaged),
			Client:       client,
		}},
	}
	_, err := service.SwitchGitBranch(context.Background(), "b1", "p1", "main")
	if !errors.Is(err, ErrGitUnavailable) || !strings.Contains(err.Error(), "activity store") {
		t.Fatalf("SwitchGitBranch on a managed computer error = %v, want the activity store gap after the guard", err)
	}
}
