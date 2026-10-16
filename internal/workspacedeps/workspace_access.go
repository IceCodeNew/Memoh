package workspacedeps

import (
	"context"
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// WorkspaceState describes a managed computer without starting it.
type WorkspaceState string

const (
	WorkspaceRunning    WorkspaceState = "running"
	WorkspaceNotRunning WorkspaceState = "not_running"
	WorkspaceMissing    WorkspaceState = "missing"
)

// WorkspaceAccess reaches managed computers. Dependency management is keyed
// by the computer: a bot is resolved to its managed computer once, at the
// service boundary (ManagedWorkspaceID), and everything else takes the
// workspace id. A user's own computer never qualifies — it lacks
// workspace.CapabilityDependencies — and device selection belongs to
// interactive workspace operations, never software management.
type WorkspaceAccess interface {
	// ManagedWorkspaceID resolves the bot to its managed computer.
	// ErrWorkspaceMissing when the bot has none.
	ManagedWorkspaceID(ctx context.Context, botID string) (string, error)
	Client(ctx context.Context, workspaceID string) (*bridge.Client, error)
	DataRoot(ctx context.Context, workspaceID string) (string, error)
	State(ctx context.Context, workspaceID string) (WorkspaceState, error)
	EnsureRunning(ctx context.Context, workspaceID string) error
	// OnBridgeReset registers fn to run with the workspace id of a managed
	// computer whose bridge connection was reset.
	OnBridgeReset(fn func(workspaceID string))
}

type managerWorkspaceAccess struct{ manager *workspace.Manager }

func NewManagerWorkspaceAccess(m *workspace.Manager) WorkspaceAccess {
	if m == nil {
		panic("workspacedeps: workspace manager is nil")
	}
	return &managerWorkspaceAccess{manager: m}
}

// ManagedWorkspaceID resolves the bot through its bindings; the workspace id
// of a bot created after 0167 is not the bot id.
func (a *managerWorkspaceAccess) ManagedWorkspaceID(ctx context.Context, botID string) (string, error) {
	workspaceID, err := a.manager.ManagedWorkspaceID(ctx, botID)
	if errors.Is(err, workspace.ErrContainerNotFound) {
		return "", ErrWorkspaceMissing
	}
	if err != nil {
		return "", fmt.Errorf("workspacedeps: resolve managed computer: %w", err)
	}
	return workspaceID, nil
}

// Client connects to the computer for dependency management: a computer
// whose kind cannot host installations (a user's own computer) is
// workspace.ErrCapabilityUnsupported before any dial.
func (a *managerWorkspaceAccess) Client(ctx context.Context, workspaceID string) (*bridge.Client, error) {
	return a.manager.ConnectFor(ctx, workspaceID, workspace.CapabilityDependencies)
}

func (*managerWorkspaceAccess) DataRoot(_ context.Context, _ string) (string, error) {
	return config.DefaultDataMount, nil
}

// State inspects the computer's container by instance key: no container is
// WorkspaceMissing, a container without a running task WorkspaceNotRunning.
func (a *managerWorkspaceAccess) State(ctx context.Context, workspaceID string) (WorkspaceState, error) {
	inspection, err := a.manager.Inspect(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("workspacedeps: inspect managed computer: %w", err)
	}
	switch {
	case !inspection.Exists:
		return WorkspaceMissing, nil
	case inspection.Running:
		return WorkspaceRunning, nil
	}
	return WorkspaceNotRunning, nil
}

func (a *managerWorkspaceAccess) EnsureRunning(ctx context.Context, workspaceID string) error {
	return a.manager.EnsureManagedRunning(ctx, workspaceID)
}

func (a *managerWorkspaceAccess) OnBridgeReset(fn func(workspaceID string)) {
	a.manager.OnManagedBridgeReset(fn)
}
