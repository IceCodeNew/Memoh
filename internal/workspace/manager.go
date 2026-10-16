package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/identity"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/settings"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/workspace/bridge"
	workspacetemplates "github.com/felinics/memoh/templates"
)

const (
	// BotLabelKey names the bot a container was created for. It is kept on new
	// containers next to WorkspaceIDLabelKey so older lookups and Cloud-side
	// filters keep working.
	BotLabelKey = "memoh.bot_id"
	// WorkspaceIDLabelKey names the managed computer a container belongs to.
	WorkspaceIDLabelKey         = "memoh.workspace_id"
	WorkspaceLabelKey           = "memoh.workspace"
	WorkspaceLabelValue         = "v3"
	WorkspaceCDIDevicesLabelKey = "memoh.workspace.cdi_devices"
	// ContainerPrefix precedes the instance key in a container name. New
	// containers are named after the managed computer (workspace-<workspace
	// id>); those created earlier are named after the bot and keep their name.
	ContainerPrefix       = "workspace-"
	LegacyContainerPrefix = "mcp-"
	DisplayRFBSocketName  = "display.rfb.sock"
	ToolsProxyHTTPURL     = bridge.ToolsProxyHTTPURL

	// WorkspaceInitPath and WorkspaceBridgePath are the container start
	// parameters used by buildWorkspaceContainerSpec: the image's init and the
	// mount point of the Server-supplied bridge binary. A container missing
	// either never starts, so WaitForWorkspaceReady surfaces that on its own.
	WorkspaceInitPath   = "/usr/bin/tini"
	WorkspaceBridgePath = "/opt/memoh/bridge"

	legacyGRPCPort           = 9090
	bridgeReadyTimeout       = 45 * time.Second
	bridgeReadyRPCTimeout    = 3 * time.Second
	bridgeReadyRetryInterval = 500 * time.Millisecond
)

// ErrContainerNotFound is returned when a bot has no managed computer or its
// computer has no container. It wraps ctr.ErrNotFound: before the manager
// resolved containers through records, the same situations surfaced as the
// runtime's not-found on a guessed container name, and callers test for that.
var ErrContainerNotFound = fmt.Errorf("workspace not found for bot: %w", ctr.ErrNotFound)

// IsNotReady reports whether err says that the bot's workspace container does
// not exist in the runtime or that its bridge does not answer. Both clear once
// a workspace that is being created has started, so a caller that waits for a
// new workspace retries on them.
func IsNotReady(err error) bool {
	return errors.Is(err, ErrContainerNotFound) || ctr.IsNotFound(err) || errors.Is(err, bridge.ErrUnavailable)
}

// ContainerStatus combines DB records with live containerd state.
type ContainerStatus struct {
	ContainerID      string    `json:"container_id"`
	WorkspaceBackend string    `json:"workspace_backend"`
	RuntimeBackend   string    `json:"runtime_backend,omitempty"`
	Image            string    `json:"image"`
	Status           string    `json:"status"`
	Namespace        string    `json:"namespace"`
	ContainerPath    string    `json:"container_path"`
	CDIDevices       []string  `json:"cdi_devices,omitempty"`
	Snapshotter      string    `json:"snapshotter,omitempty"`
	TaskRunning      bool      `json:"task_running"`
	HasPreservedData bool      `json:"has_preserved_data"`
	Legacy           bool      `json:"legacy"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ContainerMetricsStatus struct {
	Exists      bool `json:"exists"`
	TaskRunning bool `json:"task_running"`
}

type ContainerStorageMetrics struct {
	Path        string `json:"path"`
	UsedBytes   uint64 `json:"used_bytes"`
	LimitBytes  uint64 `json:"limit_bytes,omitempty"`
	UsedUnknown bool   `json:"-"`
}

type ContainerMetricsResult struct {
	Supported         bool
	UnsupportedReason string
	Status            ContainerMetricsStatus
	SampledAt         time.Time
	CPU               *ctr.CPUMetrics
	Memory            *ctr.MemoryMetrics
	Storage           *ContainerStorageMetrics
}

// Manager operates the managed computers of a runtime. Callers address it by
// bot id; every exported bot-keyed method resolves the bot's managed computer
// once at the boundary and continues with the workspace id and the container
// behind it. Host-side resources — socket directory, display socket, bridge
// connection, legacy dial target, container lock — are keyed by the container
// (its instance key for paths), never derived from the bot.
type Manager struct {
	service           runtimeService
	networkController netctl.Controller
	cfg               config.WorkspaceConfig
	namespace         string
	db                *pgxpool.Pool
	queries           dbstore.Queries
	computers         dbstore.ManagedComputerStore
	bindings          *BindingService
	hookService       *hooks.Service
	logger            *slog.Logger
	containerLockMu   sync.Mutex
	containerLocks    map[string]*sync.Mutex
	grpcPool          *bridge.Pool
	bridgeTLS         *BridgeTLSRuntimeOptions
	remote            *RemoteWorkspaceService
	templateBootstrap *TemplateBootstrapper
	setupDiagnostics  WorkspaceSetupDiagnostics
	legacyMu          sync.RWMutex
	legacyIPs         map[string]string // containerID → IP for pre-bridge containers
	bridgeResetMu     sync.Mutex
	bridgeResetFns    []func(botID string) // see OnBridgeReset
}

func NewManager(log *slog.Logger, service runtimeService, networkController netctl.Controller, cfg config.WorkspaceConfig, namespace string, conn *pgxpool.Pool, queryOverride ...dbstore.Queries) *Manager {
	if namespace == "" {
		namespace = config.DefaultNamespace
	}
	var queries dbstore.Queries
	if len(queryOverride) > 0 {
		queries = queryOverride[0]
	} else if conn != nil {
		queries = postgresstore.NewQueries(dbsqlc.New(conn))
	}
	// Managed computer records live in the postgres store; a manager without
	// a pool (tests with fake queries) records nothing unless a store is set.
	var computers dbstore.ManagedComputerStore
	if conn != nil {
		computers = postgresstore.NewWithPool(conn, dbsqlc.New(conn))
	}
	m := &Manager{
		service:           service,
		networkController: networkController,
		cfg:               cfg,
		namespace:         namespace,
		db:                conn,
		queries:           queries,
		computers:         computers,
		logger:            log.With(slog.String("component", "workspace")),
		containerLocks:    make(map[string]*sync.Mutex),
		legacyIPs:         make(map[string]string),
		templateBootstrap: NewTemplateBootstrapper(workspacetemplates.WorkspaceFS()),
	}
	m.grpcPool = bridge.NewPool(m.dialTarget)
	return m
}

func (m *Manager) SetHookService(h *hooks.Service) {
	m.hookService = h
}

// SetRemoteWorkspaceService wires the remote mounts. The service takes live
// connections through this manager's Connect, so a mount is reached the same
// way every other workspace is.
func (m *Manager) SetRemoteWorkspaceService(service *RemoteWorkspaceService) {
	m.remote = service
	if service != nil {
		service.connector = m
	}
}

// WorkspaceSetupDiagnostics records sanitized setup failures for Bot health
// checks without coupling the workspace package to the bots package.
type WorkspaceSetupDiagnostics interface {
	RecordContainerSetupFailure(ctx context.Context, botID, phase string, setupErr error) error
	ClearContainerSetupFailure(ctx context.Context, botID string) error
}

func (m *Manager) SetSetupDiagnostics(diagnostics WorkspaceSetupDiagnostics) {
	m.setupDiagnostics = diagnostics
}

// SetBridgeTLS enables strict mTLS on TCP bridge dials and injects bridge-side
// TLS material into new workspace containers. UDS bridge targets keep using the
// local filesystem trust model.
func (m *Manager) SetBridgeTLS(opts *BridgeTLSRuntimeOptions) {
	if opts == nil {
		m.bridgeTLS = nil
		m.grpcPool.SetTLSOptions(nil)
		return
	}
	m.bridgeTLS = opts
	m.grpcPool.SetTLSOptions(opts.Client)
}

func (m *Manager) lockContainer(containerID string) func() {
	m.containerLockMu.Lock()
	lock, ok := m.containerLocks[containerID]
	if !ok {
		lock = &sync.Mutex{}
		m.containerLocks[containerID] = lock
	}
	m.containerLockMu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// socketDir returns the host-side directory that is bind-mounted into the
// container at /run/memoh, holding the UDS socket file. It is named after the
// container's instance key, so a container created before managed computers
// had their own ids keeps the directory it was started with.
func (m *Manager) socketDir(containerID string) string {
	return filepath.Join(m.dataRoot(), "run", instanceKey(containerID))
}

// socketPath returns the path to the UDS socket file of a container.
func (m *Manager) socketPath(containerID string) string {
	return filepath.Join(m.socketDir(containerID), "bridge.sock")
}

// DisplaySocketPath returns the host-side path to the workspace display RFB
// Unix socket of the bot's managed computer; empty when the bot has no
// managed computer. The directory is mounted into the container at
// /run/memoh.
func (m *Manager) DisplaySocketPath(ctx context.Context, botID string) string {
	_, containerID, err := m.botContainerIDOrNew(ctx, botID)
	if err != nil {
		return ""
	}
	return filepath.Join(m.socketDir(containerID), DisplayRFBSocketName)
}

// dialTarget returns the gRPC dial target for a container. Legacy containers
// (pre-bridge) are reached via TCP; bridge containers use UDS.
func (m *Manager) dialTarget(containerID string) string {
	if targeter, ok := m.service.(interface{ BridgeTarget(string) string }); ok {
		if target := strings.TrimSpace(targeter.BridgeTarget(instanceKey(containerID))); target != "" {
			return target
		}
	}
	m.legacyMu.RLock()
	ip, legacy := m.legacyIPs[containerID]
	m.legacyMu.RUnlock()
	if legacy {
		return fmt.Sprintf("passthrough:///%s:%d", ip, legacyGRPCPort)
	}
	return "unix://" + m.socketPath(containerID)
}

// setLegacyIP records the IP address of a legacy (pre-bridge) container
// so the gRPC pool can reach it via TCP.
func (m *Manager) setLegacyIP(containerID, ip string) {
	m.legacyMu.Lock()
	m.legacyIPs[containerID] = ip
	m.legacyMu.Unlock()
}

// clearLegacyIP removes a cached legacy IP (e.g. when the container is deleted).
func (m *Manager) clearLegacyIP(containerID string) {
	m.legacyMu.Lock()
	delete(m.legacyIPs, containerID)
	m.legacyMu.Unlock()
}

// clearLegacyRoute evicts any stale TCP fallback state for a container so
// future gRPC dials use the bridge container's Unix socket.
func (m *Manager) clearLegacyRoute(ctx context.Context, workspaceID, containerID string) {
	m.clearLegacyIP(containerID)
	m.resetBridge(ctx, workspaceID, containerID)
}

// instanceClient is the bridge client of a container: the runtime's own
// provider when it has one, the pooled connection otherwise.
func (m *Manager) instanceClient(ctx context.Context, containerID string) (*bridge.Client, error) {
	if provider, ok := m.service.(bridge.Provider); ok {
		client, err := provider.MCPClient(ctx, instanceKey(containerID))
		if err == nil {
			return client, nil
		}
		if !errors.Is(err, ctr.ErrNotSupported) && !ctr.IsNotFound(err) {
			return nil, err
		}
	}
	return m.grpcPool.Get(ctx, containerID)
}

// NativeMCPClient is the bridge client of the bot's managed computer: the
// workspace behind its native target, connected through Connect. A computer
// whose container does not exist yet is dialed by the name the container will
// have; the dial fails with bridge.ErrUnavailable until then, as it always
// did. A bot without a managed computer is ErrContainerNotFound.
func (m *Manager) NativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if noManagedComputer(err) {
			return nil, ErrContainerNotFound
		}
		return nil, err
	}
	return m.Connect(ctx, workspaceID)
}

// MCPClient implements bridge.Provider: the request-scoped target override
// first, then the Bot's persisted Primary target. Either way the target is
// resolved to its workspace and connected through Connect.
func (m *Manager) MCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		if targetID == WorkspaceTargetNative {
			return m.NativeMCPClient(ctx, botID)
		}
		target, err := m.ResolveWorkspaceTarget(ctx, botID, targetID)
		return target.Client, err
	}
	if m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target.Client, err
		}
	}
	return m.NativeMCPClient(ctx, botID)
}

// CurrentWorkspaceTargetID resolves only the request or persisted target ID.
// It intentionally avoids connecting to a runtime or loading target settings.
func (m *Manager) CurrentWorkspaceTargetID(ctx context.Context, botID string) (string, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		return targetID, nil
	}
	if m.remote == nil {
		return WorkspaceTargetNative, nil
	}
	record, err := m.remote.getPrimaryRecord(ctx, botID)
	if errors.Is(err, ErrRemoteWorkspaceNotBound) {
		return WorkspaceTargetNative, nil
	}
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func (m *Manager) ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (ResolvedWorkspaceTarget, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		targetID = WorkspaceTargetFromContext(ctx)
	}
	if targetID == "" && m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target, err
		}
	}
	if targetID == "" || targetID == WorkspaceTargetNative {
		primary := targetID == ""
		if targetID == WorkspaceTargetNative && m.remote != nil {
			if _, err := m.remote.GetPrimaryMount(ctx, botID); err == nil {
				primary = false
			} else if !errors.Is(err, ErrRemoteWorkspaceNotBound) {
				return ResolvedWorkspaceTarget{}, err
			} else {
				primary = true
			}
		}
		client, err := m.NativeMCPClient(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		info, err := m.nativeWorkspaceInfo(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		workspaceID, err := m.nativeWorkspaceID(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		approval, err := m.nativeToolApprovalConfig(ctx, botID)
		if err != nil {
			return ResolvedWorkspaceTarget{}, err
		}
		return ResolvedWorkspaceTarget{
			TargetID:     WorkspaceTargetNative,
			Kind:         WorkspaceTargetNative,
			WorkspaceID:  workspaceID,
			Capabilities: CapabilitiesFor(WorkspaceKindManaged),
			Name:         "Server Workspace",
			Primary:      primary,
			Client:       client,
			Info:         info,
			Approval:     approval,
		}, nil
	}
	if _, ok := canonicalWorkspaceUUID(targetID); !ok || m.remote == nil {
		return ResolvedWorkspaceTarget{}, ErrWorkspaceTargetNotFound
	}
	return m.remote.ResolveMount(ctx, botID, targetID)
}

// WaitForWorkspaceReady waits until the bridge of the bot's managed computer
// answers.
func (m *Manager) WaitForWorkspaceReady(ctx context.Context, botID string) error {
	workspaceID, containerID, err := m.botContainerIDOrNew(ctx, botID)
	if err != nil {
		return err
	}
	return m.waitForInstanceReady(ctx, workspaceID, containerID)
}

func (m *Manager) waitForInstanceReady(ctx context.Context, workspaceID, containerID string) error {
	deadline := time.Now().Add(bridgeReadyTimeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, bridgeReadyRPCTimeout)
		client, err := m.instanceClient(attemptCtx, containerID)
		if err == nil {
			_, err = client.Stat(attemptCtx, "/")
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		m.resetBridge(ctx, workspaceID, containerID)
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace bridge not ready for %s after %s: %w", containerID, bridgeReadyTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for workspace bridge: %w", ctx.Err())
		case <-time.After(bridgeReadyRetryInterval):
		}
	}
}

// InitializeNativeWorkspace applies server-owned bootstrap content after the
// native workspace bridge is reachable. Remote Runtime targets intentionally do
// not pass through this method.
func (m *Manager) InitializeNativeWorkspace(ctx context.Context, botID string) error {
	_, containerID, err := m.botContainerIDOrNew(ctx, botID)
	if err != nil {
		return fmt.Errorf("%w: resolve native workspace filesystem: %w", ErrWorkspaceTemplateBootstrapFailed, err)
	}
	return m.initializeInstance(ctx, containerID)
}

func (m *Manager) initializeInstance(ctx context.Context, containerID string) error {
	client, err := m.instanceClient(ctx, containerID)
	if err != nil {
		return fmt.Errorf("%w: resolve native workspace filesystem: %w", ErrWorkspaceTemplateBootstrapFailed, err)
	}
	if m.templateBootstrap == nil {
		return fmt.Errorf("%w: template bootstrapper is not configured", ErrWorkspaceTemplateBootstrapFailed)
	}
	if err := m.templateBootstrap.Bootstrap(
		ctx,
		bridgeWorkspaceFileSystem{client: client},
		defaultWorkspaceBootstrapRoot,
	); err != nil {
		return fmt.Errorf("%w: %w", ErrWorkspaceTemplateBootstrapFailed, err)
	}
	return nil
}

func (m *Manager) WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error) {
	if targetID := WorkspaceTargetFromContext(ctx); targetID != "" {
		target, err := m.ResolveWorkspaceTarget(ctx, botID, targetID)
		return target.Info, err
	}
	if m.remote != nil {
		if target, primary, err := m.remote.ResolvePrimary(ctx, botID); err != nil || primary {
			return target.Info, err
		}
	}
	return m.nativeWorkspaceInfo(ctx, botID)
}

func (m *Manager) nativeWorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error) {
	if provider, ok := m.service.(bridge.WorkspaceInfoProvider); ok {
		_, containerID, err := m.botContainerIDOrNew(ctx, botID)
		switch {
		case err == nil:
			info, err := provider.WorkspaceInfo(ctx, instanceKey(containerID))
			if err == nil {
				return withToolsEndpoint(info), nil
			}
			if !errors.Is(err, ctr.ErrNotSupported) && !ctr.IsNotFound(err) {
				return bridge.WorkspaceInfo{}, err
			}
		case errors.Is(err, ErrContainerNotFound):
			// No managed computer: the defaults below describe what it
			// will be.
		default:
			return bridge.WorkspaceInfo{}, err
		}
	}
	info := bridge.WorkspaceInfo{
		Backend:        bridge.WorkspaceBackendContainer,
		DefaultWorkDir: config.DefaultDataMount,
	}
	return withToolsEndpoint(info), nil
}

// nativeWorkspaceID is the workspace behind the bot's native target: its
// managed computer. A bot without one has no workspace to record.
func (m *Manager) nativeWorkspaceID(ctx context.Context, botID string) (string, error) {
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
		return "", nil
	}
	return workspaceID, err
}

// nativeToolApprovalConfig is the native target's policy. It lives on the
// bot's managed workspace binding; bots.tool_approval_config is written
// alongside for one more version and is what a bot without a managed
// computer (or a manager without bindings) still reads.
func (m *Manager) nativeToolApprovalConfig(ctx context.Context, botID string) (settings.ToolApprovalConfig, error) {
	if m.bindings != nil {
		binding, err := m.bindings.ManagedBinding(ctx, botID)
		switch {
		case err == nil:
			return parseToolApprovalConfig(binding.ToolApproval, settings.DefaultToolApprovalConfig), nil
		case !errors.Is(err, db.ErrNoManagedWorkspaceBinding):
			return settings.ToolApprovalConfig{}, err
		}
	}
	config := settings.DefaultToolApprovalConfig()
	if m.queries == nil {
		return config, nil
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return settings.ToolApprovalConfig{}, err
	}
	row, err := m.queries.GetSettingsByBotID(ctx, id)
	if err != nil {
		return settings.ToolApprovalConfig{}, err
	}
	if len(row.ToolApprovalConfig) > 0 {
		if err := json.Unmarshal(row.ToolApprovalConfig, &config); err != nil {
			return settings.ToolApprovalConfig{}, err
		}
	}
	return settings.NormalizeToolApprovalConfig(config), nil
}

// WorkspaceIDForTarget maps a client-facing target id to the workspace it
// names, from stored state only (no runtime is dialed): "native" is the
// bot's managed computer, a mount id is the user workspace it binds, and an
// empty id is the bot's primary target. ErrWorkspaceTargetNotFound when the
// id names nothing of the bot's. A manager without bindings predates
// workspace records and reports an empty id.
func (m *Manager) WorkspaceIDForTarget(ctx context.Context, botID, targetID string) (string, error) {
	if m.bindings == nil {
		return "", nil
	}
	targetID = strings.TrimSpace(targetID)
	switch targetID {
	case "":
		binding, err := m.bindings.Primary(ctx, botID)
		if errors.Is(err, ErrNoPrimaryWorkspace) {
			return m.nativeWorkspaceID(ctx, botID)
		}
		if err != nil {
			return "", err
		}
		return binding.WorkspaceID, nil
	case WorkspaceTargetNative:
		return m.nativeWorkspaceID(ctx, botID)
	}
	bindingID, ok := canonicalWorkspaceUUID(targetID)
	if !ok {
		return "", ErrWorkspaceTargetNotFound
	}
	binding, err := m.bindings.Get(ctx, botID, bindingID)
	if errors.Is(err, ErrBindingNotFound) {
		return "", ErrWorkspaceTargetNotFound
	}
	if err != nil {
		return "", err
	}
	return binding.WorkspaceID, nil
}

// TargetForWorkspace maps a stored workspace id back to the client-facing
// target address and kind: a managed workspace is the native target, a user
// workspace is addressed by the bot's binding to it. ErrWorkspaceTargetNotFound
// when the workspace is gone or the bot is not bound to it.
func (m *Manager) TargetForWorkspace(ctx context.Context, botID, workspaceID string) (targetID, kind string, err error) {
	if m.bindings == nil {
		return "", "", ErrWorkspaceTargetNotFound
	}
	record, err := m.bindings.Workspace(ctx, workspaceID)
	if errors.Is(err, ErrWorkspaceNotFound) {
		return "", "", ErrWorkspaceTargetNotFound
	}
	if err != nil {
		return "", "", err
	}
	if record.Kind == WorkspaceKindManaged {
		return WorkspaceTargetNative, WorkspaceTargetNative, nil
	}
	binding, err := m.bindings.BindingByWorkspace(ctx, botID, workspaceID)
	if errors.Is(err, ErrBindingNotFound) {
		return "", "", ErrWorkspaceTargetNotFound
	}
	if err != nil {
		return "", "", err
	}
	return binding.ID, WorkspaceTargetRemote, nil
}

func (m *Manager) ListWorkspaceTargets(ctx context.Context, botID string) ([]WorkspaceTarget, error) {
	approval, err := m.nativeToolApprovalConfig(ctx, botID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := m.nativeWorkspaceID(ctx, botID)
	if err != nil {
		return nil, err
	}
	native := WorkspaceTarget{
		TargetID:           WorkspaceTargetNative,
		Kind:               WorkspaceTargetNative,
		WorkspaceID:        workspaceID,
		Capabilities:       CapabilitiesFor(WorkspaceKindManaged),
		Name:               "Server Workspace",
		Primary:            true,
		Status:             WorkspaceTargetStatusOffline,
		ToolApproval:       WorkspaceToolApprovalModes(approval),
		ToolApprovalConfig: approval,
	}
	if status, statusErr := m.GetContainerInfo(ctx, botID); statusErr == nil {
		native.Online = status.TaskRunning
		if native.Online {
			native.Status = WorkspaceTargetStatusOnline
		} else if strings.TrimSpace(status.Status) != "" {
			native.Status = status.Status
		}
	} else if !errors.Is(statusErr, ErrContainerNotFound) && !ctr.IsNotFound(statusErr) {
		return nil, statusErr
	}
	targets := []WorkspaceTarget{native}
	if m.remote == nil {
		return targets, nil
	}
	remoteTargets, err := m.remote.ListMounts(ctx, botID)
	if err != nil {
		return nil, err
	}
	for _, target := range remoteTargets {
		if target.Primary {
			targets[0].Primary = false
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func withToolsEndpoint(info bridge.WorkspaceInfo) bridge.WorkspaceInfo {
	if strings.TrimSpace(info.Backend) != bridge.WorkspaceBackendContainer {
		return info
	}
	if strings.TrimSpace(info.ToolsHTTPURL) != "" {
		return info
	}
	info.ToolsHTTPURL = ToolsProxyHTTPURL
	return info
}

func (m *Manager) Init(ctx context.Context) error {
	image := m.imageRef()
	result, err := m.PrepareImageForCreate(ctx, image, &ctr.PullImageOptions{
		Unpack:        true,
		StorageDriver: m.cfg.Snapshotter,
	})
	if err != nil {
		return errs.Wrap(err, "prepare base image", slog.String("image", image))
	}
	if result.Mode == ImagePrepareDelegated {
		m.logger.InfoContext(ctx, "base image pull delegated to container backend", slog.String("image", image))
	}
	return nil
}

// EnsureBot creates the workspace container of the bot's managed computer if
// it does not exist. Bot data lives in the container's writable layer
// (snapshot), not bind mounts. Only the bridge binary is injected as a
// read-only file mount; the workspace image owns its toolkit and runtime
// scripts. If imageOverride is non-empty, it is used instead of the configured
// default.
func (m *Manager) EnsureBot(ctx context.Context, botID, imageOverride string) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	image := m.imageRef()
	if imageOverride != "" {
		image = config.NormalizeImageRef(imageOverride)
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		return err
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	containerID := m.workspaceContainerIDOrNew(ctx, workspaceID)
	_, err = m.ensureWorkspaceContainer(ctx, workspaceID, botID, containerID, image, gpu)
	return err
}

func workspaceCDIDevicesLabelValue(devices []string) string {
	devices = normalizeWorkspaceGPUDevices(devices)
	return strings.Join(devices, ",")
}

func workspaceCDIDevicesFromLabels(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	value := strings.TrimSpace(labels[WorkspaceCDIDevicesLabelKey])
	if value == "" {
		return nil
	}
	return normalizeWorkspaceGPUDevices(strings.Split(value, ","))
}

// buildWorkspaceContainerSpec builds the start parameters of containerID. The
// socket directory follows the container's instance key; the bot, when there
// is one, contributes its skill roots and display preference.
func (m *Manager) buildWorkspaceContainerSpec(ctx context.Context, containerID, botID string, gpu WorkspaceGPUConfig) (ctr.ContainerSpec, error) {
	resolvPath, err := ctr.ResolveConfSource(m.dataRoot())
	if err != nil {
		return ctr.ContainerSpec{}, err
	}

	bridgePath := m.cfg.BridgeBinaryPath()
	sockDir := m.socketDir(containerID)
	if err := os.MkdirAll(sockDir, 0o750); err != nil {
		return ctr.ContainerSpec{}, fmt.Errorf("create socket dir: %w", err)
	}

	mounts := []ctr.MountSpec{
		{
			Destination: "/etc/resolv.conf",
			Type:        "bind",
			Source:      resolvPath,
			Options:     []string{"rbind", "ro"},
		},
		{
			Destination: WorkspaceBridgePath,
			Type:        "bind",
			Source:      bridgePath,
			Options:     []string{"bind", "ro"},
		},
		{
			Destination: "/run/memoh",
			Type:        "bind",
			Source:      sockDir,
			Options:     []string{"rbind", "rw"},
		},
	}
	tzMounts, tzEnv := ctr.TimezoneSpec()
	mounts = append(mounts, tzMounts...)
	if m.bridgeTLS != nil {
		bridgeDir := strings.TrimSpace(m.bridgeTLS.BridgeMaterialDir)
		if bridgeDir == "" || strings.TrimSpace(m.bridgeTLS.ExpectedClientURI) == "" {
			return ctr.ContainerSpec{}, fmt.Errorf("%w: bridge TLS strict mode requires bridge material dir and expected client URI", ctr.ErrInvalidArgument)
		}
		mounts = append(mounts, ctr.MountSpec{
			Destination: bridgeMTLSMountPath,
			Type:        "bind",
			Source:      bridgeDir,
			Options:     []string{"rbind", "ro"},
		})
	}

	var skillRoots []string
	displayEnabled := false
	if botID != "" {
		skillRoots, err = m.ResolveWorkspaceSkillDiscoveryRoots(ctx, botID)
		if err != nil {
			return ctr.ContainerSpec{}, err
		}
		displayEnabled = m.botDisplayEnabled(ctx, botID)
	}
	skillEnv := skillset.ContainerEnv(skillRoots)
	env := make([]string, 0, len(tzEnv)+1+len(skillEnv))
	env = append(env, tzEnv...)
	env = append(env, "BRIDGE_SOCKET_PATH=/run/memoh/bridge.sock")
	env = m.appendBridgeTLSEnv(env)
	if displayEnabled {
		env = append(env,
			"MEMOH_DISPLAY_ENABLED=true",
			"MEMOH_DISPLAY_RFB_TCP_ADDR=127.0.0.1:5999",
			"DISPLAY=:99",
		)
	}
	env = append(env, skillEnv...)

	return ctr.ContainerSpec{
		Cmd:        []string{WorkspaceInitPath, "-g", "--", WorkspaceBridgePath},
		Mounts:     mounts,
		Env:        env,
		CDIDevices: normalizeWorkspaceGPUDevices(gpu.Devices),
	}, nil
}

func (m *Manager) appendBridgeTLSEnv(env []string) []string {
	if m.bridgeTLS == nil {
		return env
	}
	return append(env,
		"BRIDGE_TLS_MODE="+config.BridgeTLSModeStrict,
		"BRIDGE_TLS_CERT_FILE="+bridgeMTLSMountPath+"/"+bridgeServerCertFile,
		"BRIDGE_TLS_KEY_FILE="+bridgeMTLSMountPath+"/"+bridgeServerKeyFile,
		"BRIDGE_TLS_CLIENT_CA_FILE="+bridgeMTLSMountPath+"/"+serverClientCACertFile,
		"BRIDGE_TLS_EXPECTED_CLIENT_URI="+m.bridgeTLS.ExpectedClientURI,
	)
}

func (m *Manager) botDisplayEnabled(ctx context.Context, botID string) bool {
	if m.queries == nil {
		return false
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return false
	}
	row, err := m.queries.GetSettingsByBotID(ctx, id)
	if err != nil {
		return false
	}
	return row.DisplayEnabled
}

func (m *Manager) BotDisplayEnabled(ctx context.Context, botID string) bool {
	return m.botDisplayEnabled(ctx, botID)
}

func (m *Manager) DisplayDialContext(ctx context.Context, botID, network, address string) (net.Conn, error) {
	client, err := m.NativeMCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	return client.DialContext(ctx, network, address)
}

// ensureWorkspaceContainer creates containerID for the managed computer when
// the runtime does not have it. The container is labelled with the workspace
// id and, when the computer is operated for a bot, the bot id as well.
func (m *Manager) ensureWorkspaceContainer(ctx context.Context, workspaceID, botID, containerID, image string, gpu WorkspaceGPUConfig) (string, error) {
	if _, err := m.service.GetContainer(ctx, containerID); err == nil {
		return containerID, nil
	} else if !ctr.IsNotFound(err) {
		return "", err
	}
	spec, err := m.buildWorkspaceContainerSpec(ctx, containerID, botID, gpu)
	if err != nil {
		return "", err
	}
	limits, err := m.resourceLimitsForCreate(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	labels := map[string]string{
		WorkspaceIDLabelKey: workspaceID,
		WorkspaceLabelKey:   WorkspaceLabelValue,
	}
	if botID != "" {
		labels[BotLabelKey] = botID
	}
	for k, v := range resourceLimitLabels(limits) {
		labels[k] = v
	}
	if value := workspaceCDIDevicesLabelValue(gpu.Devices); value != "" {
		labels[WorkspaceCDIDevicesLabelKey] = value
	}

	_, err = m.service.CreateContainer(ctx, ctr.CreateContainerRequest{
		ID:              containerID,
		ImageRef:        image,
		ImagePullPolicy: m.cfg.EffectiveImagePullPolicy(),
		StorageRef:      ctr.StorageRef{Driver: m.cfg.Snapshotter, Kind: "active"},
		ResourceLimits:  limits,
		Labels:          labels,
		Spec:            spec,
	})
	if err != nil && !ctr.IsAlreadyExists(err) {
		return "", err
	}
	return containerID, nil
}

// ListBots returns the ids of the bots whose managed computer has a workspace
// container: each container is resolved to its computer and the computer to
// the bots bound to it. A container no computer claims still names the bot it
// was created for.
func (m *Manager) ListBots(ctx context.Context) ([]string, error) {
	containers, err := m.service.ListContainers(ctx)
	if err != nil {
		return nil, err
	}

	botIDs := make([]string, 0, len(containers))
	seen := make(map[string]struct{}, len(containers))
	add := func(botID string) {
		if _, dup := seen[botID]; dup {
			return
		}
		seen[botID] = struct{}{}
		botIDs = append(botIDs, botID)
	}
	for _, info := range containers {
		if workspaceID, ok := m.containerWorkspaceID(ctx, info); ok {
			for _, botID := range m.boundBots(ctx, workspaceID) {
				add(botID)
			}
			continue
		}
		if botID, ok := BotIDFromContainerInfo(info); ok {
			add(botID)
		}
	}
	return botIDs, nil
}

func (m *Manager) Start(ctx context.Context, botID string) error {
	image, err := m.resolveWorkspaceImage(ctx, botID)
	if err != nil {
		return err
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

// StartWithImage creates and starts the MCP container for a bot.
// If imageOverride is non-empty, it is used as the base image instead of the
// configured default. The override only applies when creating a new container.
func (m *Manager) StartWithImage(ctx context.Context, botID, imageOverride string) error {
	image := strings.TrimSpace(imageOverride)
	if image == "" {
		return m.Start(ctx, botID)
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, config.NormalizeImageRef(image), gpu)
}

// StartWithResolvedImage creates and starts the workspace container for a bot
// using an explicit image reference.
func (m *Manager) StartWithResolvedImage(ctx context.Context, botID, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image is required")
	}
	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return err
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

func (m *Manager) StartWithResolvedConfig(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image is required")
	}
	return m.startWithResolvedConfig(ctx, botID, image, gpu)
}

func (m *Manager) startWithResolvedConfig(ctx context.Context, botID, image string, gpu WorkspaceGPUConfig) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		return err
	}
	_, err = m.startWorkspace(ctx, workspaceID, botID, image, gpu)
	return err
}

// startWorkspace creates the managed computer's container when missing and
// starts its task. botID is the bot the computer is operated for; it decides
// the skill roots, display, network overlay and hooks of the container.
func (m *Manager) startWorkspace(ctx context.Context, workspaceID, botID, image string, gpu WorkspaceGPUConfig) (string, error) {
	containerID := m.workspaceContainerIDOrNew(ctx, workspaceID)
	unlock := m.lockContainer(containerID)
	defer unlock()

	// Before creating a new container, check for an orphaned snapshot
	// (container deleted but snapshot with /data survived). Export /data
	// to a backup so it can be restored after the fresh container exists.
	// This covers dev image rebuilds, containerd metadata loss, and manual
	// container deletion.
	if _, err := m.service.GetContainer(ctx, containerID); ctr.IsNotFound(err) {
		m.recoverOrphanedSnapshot(ctx, workspaceID, containerID)
	}

	if _, err := m.ensureWorkspaceContainer(ctx, workspaceID, botID, containerID, image, gpu); err != nil {
		return "", err
	}

	// Restore preserved data (from orphaned snapshot recovery or a previous
	// teardown with preserveData) into the fresh snapshot before starting the
	// task when the backend exposes snapshot mounts. Backends without mount
	// support restore through the bridge after the task starts.
	restoreFrom := ""
	if backupPath := m.preservedBackupPath(workspaceID, botID); backupPath != "" {
		info, err := m.service.GetContainer(ctx, containerID)
		if err != nil {
			return "", fmt.Errorf("load workspace for preserved data restore: %w", err)
		}
		if err := m.restorePreservedIntoSnapshot(ctx, backupPath, info); err != nil {
			if errors.Is(err, errMountNotSupported) {
				restoreFrom = backupPath
			} else {
				return "", fmt.Errorf("restore preserved data: %w", err)
			}
		}
	}

	// Start the task and restore the container network so workspace processes
	// regain outbound connectivity. Server communication still uses UDS.
	if err := m.startTaskAndEnsureNetwork(ctx, botID, containerID); err != nil {
		if stopErr := m.service.StopContainer(ctx, containerID, &ctr.StopTaskOptions{Force: true}); stopErr != nil {
			m.logger.WarnContext(ctx, "cleanup: stop task failed", slog.String("container_id", containerID), slog.Any("error", stopErr))
		}
		return "", err
	}
	if restoreFrom != "" {
		if err := m.restorePreservedDataViaGRPC(ctx, restoreFrom, containerID); err != nil {
			return "", fmt.Errorf("restore preserved data through bridge: %w", err)
		}
	}
	if !m.IsLegacyContainer(ctx, containerID) {
		m.clearLegacyRoute(ctx, workspaceID, containerID)
	}
	if botID != "" {
		if err := m.runWorkspaceHook(context.WithoutCancel(ctx), botID, hooks.EventWorkspaceStart, map[string]any{
			"backend": bridge.WorkspaceBackendContainer,
			"image":   image,
		}); err != nil {
			m.logWorkspaceHookError(hooks.EventWorkspaceStart, botID, err)
		}
	}
	return containerID, nil
}

func (m *Manager) Stop(ctx context.Context, botID string, timeout time.Duration) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	_, containerID, err := m.resolveBotContainer(ctx, botID)
	if err != nil {
		return err
	}
	if err := m.runWorkspaceHook(ctx, botID, hooks.EventWorkspaceStop, map[string]any{
		"timeout_seconds": int(timeout.Seconds()),
	}); err != nil {
		m.logWorkspaceHookError(hooks.EventWorkspaceStop, botID, err)
	}
	return m.service.StopContainer(ctx, containerID, &ctr.StopTaskOptions{
		Timeout: timeout,
		Force:   true,
	})
}

// errPreserveData marks a removal stopped by a failed export of /data.
var errPreserveData = errors.New("preserve data")

// Delete removes the container of the bot's managed computer. The record is
// left as it is; CleanupBotContainer clears it as well.
func (m *Manager) Delete(ctx context.Context, botID string, preserveData bool) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	workspaceID, containerID, err := m.resolveBotContainer(ctx, botID)
	if err != nil {
		return err
	}
	return m.deleteWorkspaceContainer(ctx, workspaceID, botID, containerID, preserveData)
}

// deleteWorkspaceContainer exports /data when asked, then removes the
// container, its task and its network. botID only matters for the network
// overlay and may be empty.
func (m *Manager) deleteWorkspaceContainer(ctx context.Context, workspaceID, botID, containerID string, preserveData bool) error {
	if preserveData {
		if err := m.preserveDataBeforeDelete(ctx, workspaceID, botID, containerID); err != nil {
			return fmt.Errorf("%w: %w", errPreserveData, err)
		}
	}

	m.clearLegacyRoute(ctx, workspaceID, containerID)

	if err := m.removeContainerNetwork(ctx, botID, containerID); err != nil {
		m.logger.WarnContext(ctx, "delete: remove network failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}
	if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil {
		m.logger.WarnContext(ctx, "delete: delete task failed",
			slog.String("container_id", containerID), slog.Any("error", err))
	}
	return m.service.DeleteContainer(ctx, containerID, &ctr.DeleteContainerOptions{
		CleanupSnapshot: true,
	})
}

func (m *Manager) dataRoot() string {
	return m.cfg.DataRootPath()
}

func (m *Manager) imageRef() string {
	return m.cfg.ImageRef()
}

// IsLegacyContainer returns true if the container was created before the
// bridge process architecture (uses the legacy "mcp-" prefix).
// Legacy containers are functional but unreachable from the server (they
// use TCP gRPC instead of UDS). Users should delete and recreate them.
func (*Manager) IsLegacyContainer(_ context.Context, containerID string) bool {
	return strings.HasPrefix(containerID, LegacyContainerPrefix)
}

func validateBotID(botID string) error {
	return identity.ValidateChannelIdentityID(botID)
}
