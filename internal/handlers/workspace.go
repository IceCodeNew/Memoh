package handlers

import (
	"context"
	"io"

	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// containerWorkspace captures the subset of workspace capabilities required by
// container-related HTTP handlers. Keeping this private prevents handlers from
// depending on the full concrete workspace manager surface.
type containerWorkspace interface {
	bridge.Provider
	NativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error)
	// ManagedClient connects to the bot's managed computer for an operation
	// that needs the capability; handlers say what they need, not which kind
	// of computer they expect.
	ManagedClient(ctx context.Context, botID string, capability workspace.Capability) (*bridge.Client, error)
	// ResolveWorkspaceTarget resolves a client-facing target (empty: the
	// request override, then the bot's primary) to the workspace behind it.
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error)
	// ConnectFor is the one connection entry, gated on the capability the
	// operation needs.
	ConnectFor(ctx context.Context, workspaceID string, capability workspace.Capability) (*bridge.Client, error)
	BotDisplayEnabled(ctx context.Context, botID string) bool
	DisplaySocketPath(ctx context.Context, botID string) string
	EnsureRunning(ctx context.Context, botID string) error
	EnsureNativeRunning(ctx context.Context, botID string) error
	ContainerID(ctx context.Context, botID string) (string, error)
	ResolveWorkspaceImage(ctx context.Context, botID string) (string, error)
	ResolveWorkspaceGPU(ctx context.Context, botID string) (workspace.WorkspaceGPUConfig, error)
	PrepareImageForCreate(ctx context.Context, image string, opts *ctr.PullImageOptions) (workspace.ImagePrepareResult, error)
	HasPreservedData(ctx context.Context, botID string) bool
	StartWithResolvedConfig(ctx context.Context, botID, image string, gpu workspace.WorkspaceGPUConfig) error
	WaitForWorkspaceReady(ctx context.Context, botID string) error
	InitializeNativeWorkspace(ctx context.Context, botID string) error
	RememberWorkspaceImage(ctx context.Context, botID, image string) error
	RememberWorkspaceGPU(ctx context.Context, botID string, gpu workspace.WorkspaceGPUConfig) error
	RestorePreservedData(ctx context.Context, botID string) error
	RecordContainerRunning(ctx context.Context, botID, containerID, image string)
	GetContainerInfo(ctx context.Context, botID string) (*workspace.ContainerStatus, error)
	GetContainerMetrics(ctx context.Context, botID string) (*workspace.ContainerMetricsResult, error)
	GetResourceLimits(ctx context.Context, botID string) (*workspace.ResourceLimitsResult, error)
	SetResourceLimits(ctx context.Context, botID string, limits ctr.ResourceLimits) (*workspace.ResourceLimitsResult, error)
	CleanupBotContainer(ctx context.Context, botID string, preserveData bool) error
	StopBot(ctx context.Context, botID string) error
	ResolveWorkspaceSkillDiscoveryRoots(ctx context.Context, botID string) ([]string, error)
	CreateSnapshot(ctx context.Context, botID, snapshotName, source string) (*workspace.SnapshotCreateInfo, error)
	ListBotSnapshotData(ctx context.Context, botID string) (*workspace.BotSnapshotData, error)
	RollbackVersion(ctx context.Context, botID string, version int) error
	ExportData(ctx context.Context, botID string) (io.ReadCloser, error)
	ImportData(ctx context.Context, botID string, r io.Reader) error
}
