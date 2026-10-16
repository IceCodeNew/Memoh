package workspace

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/config"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const connectTestOfflineRuntimeID = "22222222-2222-4222-8222-222222222224"

// newUserWorkspaceBindings builds the binding service Connect resolves kinds
// through: one user workspace per runtime id, sharing the runtime's id as the
// workspace id the way CreateUser does.
func newUserWorkspaceBindings(t *testing.T, runtimeIDs ...string) (*BindingService, *fakeWorkspaceStore) {
	t.Helper()
	store := &fakeWorkspaceStore{}
	for _, runtimeID := range runtimeIDs {
		if _, err := store.CreateWorkspace(context.Background(), dbstore.CreateWorkspaceInput{
			ID: runtimeID, OwnerUserID: remoteTestOwnerID, Kind: WorkspaceKindUser, Name: "Computer " + runtimeID[len(runtimeID)-1:],
		}); err != nil {
			t.Fatalf("create user workspace %s: %v", runtimeID, err)
		}
	}
	return NewBindingService(store, store), store
}

// connectTestEnv is a bot with a managed computer and a mounted user computer
// that is its primary target, plus two more user workspaces: one whose
// runtime is online but too old to serve a workspace, one that is offline.
type connectTestEnv struct {
	manager    *Manager
	bindings   *BindingService
	remote     *RemoteWorkspaceService
	rootClient *bridge.Client
	managedID  string
}

func newConnectTestEnv(t *testing.T) *connectTestEnv {
	t.Helper()
	ctx := context.Background()
	rootClient, _ := newRemoteScopeTestClient(t)
	legacyClient, _ := newRemoteScopeTestClient(t)
	bindings, store := newUserWorkspaceBindings(t, remoteTestRuntimeID, remoteTestRuntimeID2, connectTestOfflineRuntimeID)
	managed, err := store.CreateWorkspace(ctx, dbstore.CreateWorkspaceInput{ID: wsTestManagedID, OwnerUserID: remoteTestOwnerID, Kind: WorkspaceKindManaged, Name: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.Bind(ctx, remoteTestBotID, managed.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.Bind(ctx, remoteTestBotID, remoteTestRuntimeID, true); err != nil {
		t.Fatal(err)
	}
	remote := &RemoteWorkspaceService{
		store: &fakeRemoteBindingStore{records: []dbstore.BotRemoteRuntimeBindingRecord{{
			ID: remoteTestTargetID, BotID: remoteTestBotID, RuntimeID: remoteTestRuntimeID,
			RuntimeName: "Office Mac", IsPrimary: true,
			RuntimeUserID: remoteTestOwnerID, BotOwnerUserID: remoteTestOwnerID,
		}}},
		runtimes: fakeRuntimeConnections{
			remoteTestRuntimeID: {
				RuntimeID: remoteTestRuntimeID,
				Client:    rootClient,
				Info: userruntime.RuntimeInfo{
					WorkspaceBase: "/Users/alice",
					OS:            "darwin",
					Capabilities:  []string{userruntime.CapabilityFS, userruntime.CapabilityExec, userruntime.CapabilityHostFS},
				},
			},
			remoteTestRuntimeID2: {
				RuntimeID: remoteTestRuntimeID2,
				Client:    legacyClient,
				Info: userruntime.RuntimeInfo{
					WorkspaceBase: "/Users/alice",
					OS:            "darwin",
					Capabilities:  []string{userruntime.CapabilityFS, userruntime.CapabilityExec},
				},
			},
		},
	}
	manager := NewManager(slog.Default(), nil, nil, config.WorkspaceConfig{DataRoot: t.TempDir()}, "", nil)
	manager.SetBindingService(bindings)
	manager.SetRemoteWorkspaceService(remote)
	return &connectTestEnv{manager: manager, bindings: bindings, remote: remote, rootClient: rootClient, managedID: managed.ID}
}

func TestConnectDispatchesOnWorkspaceKind(t *testing.T) {
	t.Parallel()
	env := newConnectTestEnv(t)
	ctx := context.Background()

	// The remote service takes its connections through the manager.
	if env.remote.connector != env.manager {
		t.Fatal("SetRemoteWorkspaceService did not wire the manager as connector")
	}

	t.Run("managed computer dials its container", func(t *testing.T) {
		client, err := env.manager.Connect(ctx, env.managedID)
		if err != nil || client == nil {
			t.Fatalf("Connect(managed) = %v, %v; want a client", client, err)
		}
	})
	t.Run("user computer is its live runtime connection", func(t *testing.T) {
		client, err := env.manager.Connect(ctx, remoteTestRuntimeID)
		if err != nil {
			t.Fatalf("Connect(user) error = %v", err)
		}
		if client != env.rootClient {
			t.Fatalf("Connect(user) returned a client other than the runtime connection's")
		}
	})
	t.Run("offline user computer", func(t *testing.T) {
		if _, err := env.manager.Connect(ctx, connectTestOfflineRuntimeID); !errors.Is(err, ErrRemoteRuntimeOffline) {
			t.Fatalf("Connect(offline) error = %v, want ErrRemoteRuntimeOffline", err)
		}
	})
	t.Run("user computer whose client must be updated", func(t *testing.T) {
		if _, err := env.manager.Connect(ctx, remoteTestRuntimeID2); !errors.Is(err, ErrRemoteRuntimeClientUpdateNeeded) {
			t.Fatalf("Connect(legacy client) error = %v, want ErrRemoteRuntimeClientUpdateNeeded", err)
		}
	})
	t.Run("unknown workspace", func(t *testing.T) {
		for _, id := range []string{wsTestUnknownUUID, "", "not-a-uuid"} {
			if _, err := env.manager.Connect(ctx, id); !errors.Is(err, ErrWorkspaceNotFound) {
				t.Fatalf("Connect(%q) error = %v, want ErrWorkspaceNotFound", id, err)
			}
		}
	})
}

func TestConnectForRequiresTheCapability(t *testing.T) {
	t.Parallel()
	env := newConnectTestEnv(t)
	ctx := context.Background()

	// A user's computer serves files; everything the server-owned container
	// offers on top of that is refused before any dial.
	if client, err := env.manager.ConnectFor(ctx, remoteTestRuntimeID, CapabilityFiles); err != nil || client != env.rootClient {
		t.Fatalf("ConnectFor(user, files) = %v, %v; want the runtime client", client, err)
	}
	for _, capability := range []Capability{CapabilityTerminal, CapabilityDisplay, CapabilitySnapshots, CapabilityDependencies, CapabilityExternalAgent} {
		_, err := env.manager.ConnectFor(ctx, remoteTestRuntimeID, capability)
		if !errors.Is(err, ErrCapabilityUnsupported) {
			t.Fatalf("ConnectFor(user, %s) error = %v, want ErrCapabilityUnsupported", capability, err)
		}
		var capErr *CapabilityError
		if !errors.As(err, &capErr) || capErr.Capability != capability {
			t.Fatalf("ConnectFor(user, %s) error = %#v, want CapabilityError naming it", capability, err)
		}
	}
	// The capability check comes first: an offline user computer still
	// answers "unsupported" for what it could never do, and "offline" for
	// what it could.
	if _, err := env.manager.ConnectFor(ctx, connectTestOfflineRuntimeID, CapabilityTerminal); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("ConnectFor(offline user, terminal) error = %v, want ErrCapabilityUnsupported", err)
	}
	if _, err := env.manager.ConnectFor(ctx, connectTestOfflineRuntimeID, CapabilityFiles); !errors.Is(err, ErrRemoteRuntimeOffline) {
		t.Fatalf("ConnectFor(offline user, files) error = %v, want ErrRemoteRuntimeOffline", err)
	}
	for _, capability := range allCapabilities {
		if client, err := env.manager.ConnectFor(ctx, env.managedID, capability); err != nil || client == nil {
			t.Fatalf("ConnectFor(managed, %s) = %v, %v; want a client", capability, client, err)
		}
	}
	if _, err := env.manager.ConnectFor(ctx, wsTestUnknownUUID, CapabilityFiles); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ConnectFor(unknown) error = %v, want ErrWorkspaceNotFound", err)
	}
}

// Terminal, display, snapshots and dependency management are asked of the
// bot's managed computer, whatever its primary target is: a bot whose primary
// is a user's computer keeps getting them from the computer that can serve
// them, and a bot without a managed computer gets the same not-found as
// before.
func TestManagedClientServesManagedCapabilitiesBesideAUserPrimary(t *testing.T) {
	t.Parallel()
	env := newConnectTestEnv(t)
	ctx := context.Background()

	if current, err := env.manager.CurrentWorkspaceTargetID(ctx, remoteTestBotID); err != nil || current != remoteTestTargetID {
		t.Fatalf("primary target = %q, %v; want the user computer mount", current, err)
	}
	for _, capability := range []Capability{CapabilityTerminal, CapabilityDisplay, CapabilitySnapshots, CapabilityDependencies, CapabilityExternalAgent} {
		client, err := env.manager.ManagedClient(ctx, remoteTestBotID, capability)
		if err != nil || client == nil {
			t.Fatalf("ManagedClient(%s) = %v, %v; want the managed computer's client", capability, client, err)
		}
		if client == env.rootClient {
			t.Fatalf("ManagedClient(%s) handed out the user computer's connection", capability)
		}
	}
	if _, err := env.manager.ManagedClient(ctx, remoteTestBotID2, CapabilityTerminal); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("ManagedClient(bot without managed computer) error = %v, want ErrContainerNotFound", err)
	}
	if _, err := env.manager.NativeMCPClient(ctx, remoteTestBotID2); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("NativeMCPClient(bot without managed computer) error = %v, want ErrContainerNotFound", err)
	}
}

func TestResolvedTargetsCarryCapabilities(t *testing.T) {
	t.Parallel()
	env := newConnectTestEnv(t)
	ctx := context.Background()

	native, err := env.manager.ResolveWorkspaceTarget(ctx, remoteTestBotID, WorkspaceTargetNative)
	if err != nil {
		t.Fatalf("ResolveWorkspaceTarget(native): %v", err)
	}
	if native.WorkspaceID != env.managedID || !native.Capabilities.Has(CapabilityTerminal) || !native.Capabilities.Has(CapabilityExternalAgent) {
		t.Fatalf("native target = %+v, want the managed computer with every capability", native)
	}
	if native.Primary {
		t.Fatal("native target reported primary while a user computer is primary")
	}

	mount, err := env.manager.ResolveWorkspaceTarget(ctx, remoteTestBotID, remoteTestTargetID)
	if err != nil {
		t.Fatalf("ResolveWorkspaceTarget(mount): %v", err)
	}
	if mount.WorkspaceID != remoteTestRuntimeID || mount.Client != env.rootClient || !mount.Primary {
		t.Fatalf("mount target = %+v, want the user workspace through Connect", mount)
	}
	if !mount.Capabilities.Has(CapabilityFiles) || mount.Capabilities.Has(CapabilityExternalAgent) {
		t.Fatalf("mount capabilities = %v, want files only", mount.Capabilities.List())
	}
	if mount.Info.Backend != bridge.WorkspaceBackendRemote || mount.Info.OS != "darwin" || mount.Info.DefaultWorkDir != "/Users/alice" {
		t.Fatalf("mount info = %+v", mount.Info)
	}

	primary, err := env.manager.MCPClient(ctx, remoteTestBotID)
	if err != nil || primary != env.rootClient {
		t.Fatalf("MCPClient(primary) = %v, %v; want the user computer's connection", primary, err)
	}
	listed, err := env.remote.ListMounts(ctx, remoteTestBotID)
	if err != nil || len(listed) != 1 || !listed[0].Capabilities.Has(CapabilityFiles) || listed[0].Capabilities.Has(CapabilityDisplay) {
		t.Fatalf("ListMounts = %+v, %v; want one files-only target", listed, err)
	}
	caps, err := env.manager.WorkspaceCapabilities(ctx, remoteTestRuntimeID)
	if err != nil || caps.Has(CapabilityDependencies) {
		t.Fatalf("WorkspaceCapabilities(user) = %v, %v", caps.List(), err)
	}
}
