package workspace

import (
	"context"
	"fmt"
	"strings"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Connector is the one entry point to a workspace's bridge. Every caller that
// needs to talk to a computer — tools, handlers, remote mounts, stdio MCP —
// resolves the workspace it means and connects here; nothing else dials.
type Connector interface {
	Connect(ctx context.Context, workspaceID string) (*bridge.Client, error)
}

// Connect returns the bridge client of a workspace. A managed computer is
// reached through its container: one that does not exist yet is dialed by the
// name the container will have, so the pooled connection is in place once it
// starts and the dial fails with bridge.ErrUnavailable until then. A user's
// computer is reached through the live Remote Runtime connection it holds:
// ErrRemoteRuntimeOffline when there is none, ErrRemoteRuntimeClientUpdateNeeded
// when the runtime's client cannot serve a workspace. ErrWorkspaceNotFound
// when the id names no workspace.
func (m *Manager) Connect(ctx context.Context, workspaceID string) (*bridge.Client, error) {
	record, err := m.workspaceRecord(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return m.connectRecord(ctx, record)
}

// ConnectFor is Connect for an operation that needs capability: a computer
// whose kind does not carry it is ErrCapabilityUnsupported before any dial.
func (m *Manager) ConnectFor(ctx context.Context, workspaceID string, capability Capability) (*bridge.Client, error) {
	record, err := m.workspaceRecord(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := RecordCapabilities(record).Require(capability); err != nil {
		return nil, err
	}
	return m.connectRecord(ctx, record)
}

// ManagedClient connects to the bot's managed computer for an operation that
// needs capability: terminals, the display, dependency management and stdio
// MCP servers all live on the computer the server provisioned for the bot,
// whatever its primary target is. ErrContainerNotFound when the bot has no
// managed computer.
func (m *Manager) ManagedClient(ctx context.Context, botID string, capability Capability) (*bridge.Client, error) {
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if noManagedComputer(err) {
			return nil, ErrContainerNotFound
		}
		return nil, err
	}
	return m.ConnectFor(ctx, workspaceID, capability)
}

// WorkspaceCapabilities is the capability set of a stored workspace, from
// its kind alone; no runtime is dialed.
func (m *Manager) WorkspaceCapabilities(ctx context.Context, workspaceID string) (Capabilities, error) {
	record, err := m.workspaceRecord(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return RecordCapabilities(record), nil
}

// workspaceRecord loads the workspace Connect was asked for. A manager
// without a binding service predates workspace records: every id is a managed
// computer, as managedWorkspaceID assumes for bots.
func (m *Manager) workspaceRecord(ctx context.Context, workspaceID string) (dbstore.WorkspaceRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	if m.bindings == nil {
		return dbstore.WorkspaceRecord{ID: workspaceID, Kind: WorkspaceKindManaged}, nil
	}
	return m.bindings.Workspace(ctx, workspaceID)
}

// connectRecord is the body of Connect once the workspace is known.
func (m *Manager) connectRecord(ctx context.Context, record dbstore.WorkspaceRecord) (*bridge.Client, error) {
	switch record.Kind {
	case WorkspaceKindManaged:
		return m.connectManaged(ctx, record.ID)
	case WorkspaceKindUser:
		connection, err := m.connectUser(record.ID)
		if err != nil {
			return nil, err
		}
		return connection.Client, nil
	}
	return nil, fmt.Errorf("%w: unknown workspace kind %q", ErrWorkspaceNotFound, record.Kind)
}

// connectManaged is the managed half of Connect: the bridge client of the
// computer's container, by instance key.
func (m *Manager) connectManaged(ctx context.Context, workspaceID string) (*bridge.Client, error) {
	return m.instanceClient(ctx, m.workspaceContainerIDOrNew(ctx, workspaceID))
}

// connectUser is the user half of Connect: the live Remote Runtime connection
// of the computer, which shares its id with the workspace. Without a remote
// workspace service no runtime can be connected, so every user computer is
// offline.
func (m *Manager) connectUser(runtimeID string) (*userruntime.Connection, error) {
	if m.remote == nil {
		return nil, ErrRemoteRuntimeOffline
	}
	return connectUserRuntime(m.remote.runtimes, runtimeID)
}

// connectUserRuntime resolves the live connection of a Remote Runtime and
// checks that its client can serve a workspace. It is the only place the
// offline and client-update sentinels for a user computer come from.
func connectUserRuntime(runtimes runtimeConnectionResolver, runtimeID string) (*userruntime.Connection, error) {
	if runtimes == nil {
		return nil, ErrRemoteRuntimeOffline
	}
	connection, ok := runtimes.Connection(runtimeID)
	if !ok || connection == nil || connection.Client == nil {
		return nil, ErrRemoteRuntimeOffline
	}
	if !supportsRemoteWorkspace(connection.Info.Capabilities) {
		return nil, ErrRemoteRuntimeClientUpdateNeeded
	}
	return connection, nil
}
