package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// stdioTargetWorkspace fakes the two manager calls a stdio MCP start makes
// and records what it was asked for. It answers like the manager does: an
// empty target means the request override, then the bot's primary.
type stdioTargetWorkspace struct {
	containerWorkspace
	primaryWorkspaceID string
	managedWorkspaceID string
	clients            map[string]*bridge.Client
	resolveErr         error

	resolvedTarget  string
	connectedID     string
	connectedCapabi workspace.Capability
}

func (w *stdioTargetWorkspace) ResolveWorkspaceTarget(ctx context.Context, _ string, targetID string) (workspace.ResolvedWorkspaceTarget, error) {
	w.resolvedTarget = targetID
	if w.resolveErr != nil {
		return workspace.ResolvedWorkspaceTarget{}, w.resolveErr
	}
	if override := workspace.WorkspaceTargetFromContext(ctx); override == workspace.WorkspaceTargetNative {
		return workspace.ResolvedWorkspaceTarget{
			TargetID: workspace.WorkspaceTargetNative, Kind: workspace.WorkspaceTargetNative,
			WorkspaceID: w.managedWorkspaceID, Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindManaged),
		}, nil
	}
	return workspace.ResolvedWorkspaceTarget{
		TargetID: "mount-1", Kind: workspace.WorkspaceTargetRemote, Primary: true,
		WorkspaceID: w.primaryWorkspaceID, Capabilities: workspace.CapabilitiesFor(workspace.WorkspaceKindUser),
	}, nil
}

func (w *stdioTargetWorkspace) ConnectFor(_ context.Context, workspaceID string, capability workspace.Capability) (*bridge.Client, error) {
	w.connectedID = workspaceID
	w.connectedCapabi = capability
	client, ok := w.clients[workspaceID]
	if !ok {
		return nil, workspace.ErrWorkspaceNotFound
	}
	return client, nil
}

// A stdio MCP process runs on the bot's current computer, as it did before
// the connection went through Connect: with a user's computer as primary it
// starts there, with a native override it starts on the managed computer, and
// either way the connection is asked for with the files capability only.
func TestStdioWorkspaceClientFollowsThePrimaryTarget(t *testing.T) {
	t.Parallel()

	userClient := &bridge.Client{}
	managedClient := &bridge.Client{}
	fake := &stdioTargetWorkspace{
		primaryWorkspaceID: "ws-user",
		managedWorkspaceID: "ws-managed",
		clients:            map[string]*bridge.Client{"ws-user": userClient, "ws-managed": managedClient},
	}
	h := &ContainerdHandler{manager: fake}

	workspaceID, client, err := h.stdioWorkspaceClient(t.Context(), "bot-1")
	if err != nil {
		t.Fatalf("stdioWorkspaceClient(primary) error = %v", err)
	}
	if fake.resolvedTarget != "" {
		t.Fatalf("resolved target = %q, want the empty id that means override-then-primary", fake.resolvedTarget)
	}
	if workspaceID != "ws-user" || client != userClient || fake.connectedID != "ws-user" {
		t.Fatalf("primary start = (%q, %p), connected %q; want the user computer", workspaceID, client, fake.connectedID)
	}
	if fake.connectedCapabi != workspace.CapabilityFiles {
		t.Fatalf("capability = %q, want files", fake.connectedCapabi)
	}

	workspaceID, client, err = h.stdioWorkspaceClient(workspace.WithWorkspaceTarget(t.Context(), workspace.WorkspaceTargetNative), "bot-1")
	if err != nil {
		t.Fatalf("stdioWorkspaceClient(native override) error = %v", err)
	}
	if workspaceID != "ws-managed" || client != managedClient {
		t.Fatalf("native override start = (%q, %p); want the managed computer", workspaceID, client)
	}
}

func TestStdioWorkspaceClientPropagatesResolutionErrors(t *testing.T) {
	t.Parallel()

	fake := &stdioTargetWorkspace{resolveErr: workspace.ErrRemoteRuntimeOffline}
	h := &ContainerdHandler{manager: fake}
	if _, _, err := h.stdioWorkspaceClient(t.Context(), "bot-1"); !errors.Is(err, workspace.ErrRemoteRuntimeOffline) {
		t.Fatalf("offline primary error = %v, want ErrRemoteRuntimeOffline", err)
	}
	if fake.connectedID != "" {
		t.Fatalf("connected %q although resolution failed", fake.connectedID)
	}

	fake = &stdioTargetWorkspace{primaryWorkspaceID: "ws-user", clients: map[string]*bridge.Client{}}
	h = &ContainerdHandler{manager: fake}
	_, _, err := h.stdioWorkspaceClient(t.Context(), "bot-1")
	if !errors.Is(err, workspace.ErrWorkspaceNotFound) || !strings.Contains(err.Error(), "workspace not found") {
		t.Fatalf("connect failure error = %v, want ErrWorkspaceNotFound", err)
	}
}
