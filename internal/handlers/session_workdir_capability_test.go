package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

type capabilityWorkdirService struct {
	workdir workdir.Workdir
}

func (s capabilityWorkdirService) RequireActive(context.Context, string, string) (workdir.Workdir, error) {
	return s.workdir, nil
}

// External Agent sessions bind a workdir only when the computer it lives on
// can host those agents. A user's own computer offers files only, so the
// binding is refused with the same client-fault code and text as before, for
// ACP and direct runtimes alike; the model runtime binds anywhere, and a
// managed computer binds everything.
func TestResolveSessionWorkdirBindingRequiresExternalAgentCapability(t *testing.T) {
	t.Parallel()

	userComputer := capabilityWorkdirService{workdir: workdir.Workdir{
		ID: "wd-user", TargetKind: workdir.TargetKindRemote, WorkspaceTargetID: "bind-1",
		Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindUser), Path: "/Users/alice/code",
	}}
	managedComputer := capabilityWorkdirService{workdir: workdir.Workdir{
		ID: "wd-managed", TargetKind: workdir.TargetKindNative, WorkspaceTargetID: workspace.WorkspaceTargetNative,
		Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindManaged), Path: "/data/site",
	}}
	externalRuntimes := []string{session.RuntimeACPAgent, session.RuntimeCodex, session.RuntimeClaudeCode}

	for _, runtimeType := range externalRuntimes {
		_, err := resolveSessionWorkdirBinding(t.Context(), userComputer, "bot-1", "wd-user", runtimeType)
		if err == nil {
			t.Fatalf("runtime %q bound a workdir on a user computer", runtimeType)
		}
		p := problemOf(t, err)
		if p.Code != string(apperror.CodeWorkdirRemoteUnsupportedForAgent) || p.Status != http.StatusBadRequest || p.Fault != apperror.FaultClient {
			t.Fatalf("runtime %q problem = %+v, want %s 400 client", runtimeType, p, apperror.CodeWorkdirRemoteUnsupportedForAgent)
		}
	}
	for _, runtimeType := range externalRuntimes {
		bound, err := resolveSessionWorkdirBinding(t.Context(), managedComputer, "bot-1", "wd-managed", runtimeType)
		if err != nil || bound == nil || bound.ID != "wd-managed" {
			t.Fatalf("runtime %q on a managed computer = %+v, %v; want the binding", runtimeType, bound, err)
		}
	}
	bound, err := resolveSessionWorkdirBinding(t.Context(), userComputer, "bot-1", "wd-user", session.RuntimeModel)
	if err != nil || bound == nil || bound.ID != "wd-user" {
		t.Fatalf("model runtime on a user computer = %+v, %v; want the binding", bound, err)
	}
}
