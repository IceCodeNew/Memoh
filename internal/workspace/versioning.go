package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	SnapshotSourceManual   = "manual"
	SnapshotSourcePreExec  = "pre_exec"
	SnapshotSourceRollback = "rollback"
)

type VersionInfo struct {
	ID                  string
	Version             int
	SnapshotName        string
	RuntimeSnapshotName string
	DisplayName         string
	CreatedAt           time.Time
}

type SnapshotCreateInfo struct {
	ContainerID         string
	SnapshotName        string
	RuntimeSnapshotName string
	DisplayName         string
	Snapshotter         string
	Version             int
	CreatedAt           time.Time
}

type ManagedSnapshotMeta struct {
	Source                    string
	Version                   *int
	DisplayName               string
	ParentRuntimeSnapshotName string
	Snapshotter               string
	CreatedAt                 time.Time
}

type BotSnapshotData struct {
	ContainerID      string
	Info             ctr.ContainerInfo
	Snapshotter      string
	RuntimeSnapshots []ctr.SnapshotInfo
	ManagedMeta      map[string]ManagedSnapshotMeta
}

func (m *Manager) CreateSnapshot(ctx context.Context, botID, snapshotName, source string) (*SnapshotCreateInfo, error) {
	if err := validateBotID(botID); err != nil {
		return nil, err
	}
	if m.computers == nil {
		return nil, errors.New("db is not configured")
	}

	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	if err := ref.EnsureDBRecords(ctx); err != nil {
		return nil, err
	}

	containerID := ref.containerID
	info := ref.info
	displayName := strings.TrimSpace(snapshotName)
	runtimeSnapshotName := fmt.Sprintf("%s-snapshot-%d", containerID, time.Now().UnixNano())
	snapshotter := info.StorageRef.Driver
	normalizedSource := normalizeSnapshotSource(source)

	// The sequence below (stop → commit → replace → start) is atomic from the
	// container's perspective: interrupting it mid-way leaves the container missing.
	// Use a detached context so a cancelled HTTP request cannot break it.
	dctx := context.WithoutCancel(ctx)

	if !m.nativeSnapshotsSupported(dctx) {
		runtimeSnapshotName = m.archiveSnapshotKey(ref.workspaceID)
		snapshotter = "archive"
		if archiveErr := m.createArchiveSnapshotFromRef(dctx, ref, runtimeSnapshotName); archiveErr != nil {
			return nil, archiveErr
		}
	} else if err := m.commitSnapshotAndReplaceContainer(dctx, ref, runtimeSnapshotName); err != nil {
		if !errors.Is(err, ctr.ErrNotSupported) {
			return nil, err
		}
		runtimeSnapshotName = m.archiveSnapshotKey(ref.workspaceID)
		snapshotter = "archive"
		if archiveErr := m.createArchiveSnapshotFromRef(dctx, ref, runtimeSnapshotName); archiveErr != nil {
			return nil, archiveErr
		}
	}

	_, versionNumber, createdAt, err := m.recordSnapshotVersion(
		dctx,
		ref.workspaceID,
		containerID,
		runtimeSnapshotName,
		displayName,
		info.StorageRef.Key,
		snapshotter,
		normalizedSource,
	)
	if err != nil {
		return nil, err
	}
	if err := m.insertEvent(dctx, ref.workspaceID, containerID, "snapshot_create", map[string]any{
		"snapshot_name":         coalesceSnapshotName(displayName, versionNumber),
		"display_name":          displayName,
		"runtime_snapshot_name": runtimeSnapshotName,
		"snapshotter":           snapshotter,
		"source":                normalizedSource,
		"version":               versionNumber,
	}); err != nil {
		return nil, err
	}

	return &SnapshotCreateInfo{
		ContainerID:         containerID,
		SnapshotName:        coalesceSnapshotName(displayName, versionNumber),
		RuntimeSnapshotName: runtimeSnapshotName,
		DisplayName:         displayName,
		Snapshotter:         snapshotter,
		Version:             versionNumber,
		CreatedAt:           createdAt,
	}, nil
}

func (m *Manager) CreateVersion(ctx context.Context, botID string) (*VersionInfo, error) {
	if m.computers == nil {
		return nil, errors.New("db is not configured")
	}
	if err := validateBotID(botID); err != nil {
		return nil, err
	}

	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	if err := ref.EnsureDBRecords(ctx); err != nil {
		return nil, err
	}

	containerID := ref.containerID
	info := ref.info
	dctx := context.WithoutCancel(ctx)

	versionSnapshotName := fmt.Sprintf("%s-v%d", containerID, time.Now().UnixNano())
	if !m.nativeSnapshotsSupported(dctx) {
		versionSnapshotName = m.archiveSnapshotKey(ref.workspaceID)
		if archiveErr := m.createArchiveSnapshotFromRef(dctx, ref, versionSnapshotName); archiveErr != nil {
			return nil, archiveErr
		}
	} else if err := m.commitSnapshotAndReplaceContainer(dctx, ref, versionSnapshotName); err != nil {
		if !errors.Is(err, ctr.ErrNotSupported) {
			return nil, err
		}
		versionSnapshotName = m.archiveSnapshotKey(ref.workspaceID)
		if archiveErr := m.createArchiveSnapshotFromRef(dctx, ref, versionSnapshotName); archiveErr != nil {
			return nil, archiveErr
		}
	}

	versionID, versionNumber, createdAt, err := m.recordSnapshotVersion(
		dctx,
		ref.workspaceID,
		containerID,
		versionSnapshotName,
		"",
		info.StorageRef.Key,
		archiveAwareSnapshotter(info.StorageRef.Driver, versionSnapshotName),
		SnapshotSourcePreExec,
	)
	if err != nil {
		return nil, err
	}

	if err := m.insertEvent(dctx, ref.workspaceID, containerID, "version_create", map[string]any{
		"snapshot_name": versionSnapshotName,
		"version":       versionNumber,
		"version_id":    versionID,
	}); err != nil {
		return nil, err
	}

	return &VersionInfo{
		ID:                  versionID,
		Version:             versionNumber,
		SnapshotName:        fmt.Sprintf("Version %d", versionNumber),
		RuntimeSnapshotName: versionSnapshotName,
		DisplayName:         "",
		CreatedAt:           createdAt,
	}, nil
}

// ListBotSnapshotData returns the raw snapshot data for a bot under the
// per-container lock, so callers never observe transient state during
// snapshot/version operations.
func (m *Manager) ListBotSnapshotData(ctx context.Context, botID string) (*BotSnapshotData, error) {
	if err := validateBotID(botID); err != nil {
		return nil, err
	}

	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer ref.Close()

	containerID := ref.containerID
	info := ref.info
	snapshotter := strings.TrimSpace(info.StorageRef.Driver)
	if snapshotter == "" {
		snapshotter = m.cfg.Snapshotter
	}
	if snapshotter == "" {
		snapshotter = "overlayfs"
	}

	runtimeSnapshots, err := m.service.ListSnapshots(ctx, ctr.ListSnapshotsRequest{Driver: snapshotter})
	if err != nil && !errors.Is(err, ctr.ErrNotSupported) {
		return nil, err
	}
	runtimeNames := make(map[string]struct{}, len(runtimeSnapshots))
	for _, info := range runtimeSnapshots {
		name := strings.TrimSpace(info.Name)
		if name != "" {
			runtimeNames[name] = struct{}{}
		}
	}

	managedMeta := make(map[string]ManagedSnapshotMeta)
	if rows, err := m.managedSnapshots(ctx, botID); err != nil {
		return nil, err
	} else {
		for _, row := range rows {
			name := strings.TrimSpace(row.RuntimeSnapshotName)
			if name == "" {
				continue
			}
			meta := ManagedSnapshotMeta{
				Source:                    strings.TrimSpace(row.Source),
				DisplayName:               strings.TrimSpace(row.DisplayName),
				ParentRuntimeSnapshotName: strings.TrimSpace(row.ParentRuntimeSnapshotName),
				Snapshotter:               strings.TrimSpace(row.Snapshotter),
				Version:                   row.Version,
				CreatedAt:                 row.CreatedAt,
			}
			managedMeta[name] = meta
			if strings.EqualFold(meta.Snapshotter, "archive") {
				if _, exists := runtimeNames[name]; !exists {
					runtimeSnapshots = append(runtimeSnapshots, ctr.SnapshotInfo{
						Name:    name,
						Parent:  meta.ParentRuntimeSnapshotName,
						Kind:    "archive",
						Created: meta.CreatedAt,
						Updated: meta.CreatedAt,
					})
					runtimeNames[name] = struct{}{}
				}
			}
		}
	}

	return &BotSnapshotData{
		ContainerID:      containerID,
		Info:             info,
		Snapshotter:      snapshotter,
		RuntimeSnapshots: runtimeSnapshots,
		ManagedMeta:      managedMeta,
	}, nil
}

func (m *Manager) ListVersions(ctx context.Context, botID string) ([]VersionInfo, error) {
	if m.computers == nil {
		return nil, errors.New("db is not configured")
	}
	if err := validateBotID(botID); err != nil {
		return nil, err
	}

	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
			return []VersionInfo{}, nil
		}
		return nil, err
	}
	versions, err := m.computers.ListManagedComputerVersions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	out := make([]VersionInfo, 0, len(versions))
	for _, row := range versions {
		out = append(out, VersionInfo{
			ID:                  row.ID,
			Version:             row.Version,
			SnapshotName:        coalesceSnapshotName(row.DisplayName, row.Version),
			RuntimeSnapshotName: row.RuntimeSnapshotName,
			DisplayName:         strings.TrimSpace(row.DisplayName),
			CreatedAt:           row.CreatedAt,
		})
	}
	return out, nil
}

func (m *Manager) RollbackVersion(ctx context.Context, botID string, version int) error {
	if err := validateBotID(botID); err != nil {
		return err
	}
	if m.computers == nil {
		return errors.New("db is not configured")
	}
	if version < 1 || version > math.MaxInt32 {
		return errors.New("version out of range")
	}

	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return err
	}
	defer ref.Close()
	workspaceID := ref.workspaceID

	snapshotName, err := m.computers.GetManagedComputerVersionSnapshotName(ctx, workspaceID, version)
	if err != nil {
		return err
	}

	dctx := context.WithoutCancel(ctx)

	if strings.HasPrefix(strings.TrimSpace(snapshotName), archivePrefix) {
		if err := m.restoreArchiveSnapshotFromRef(dctx, ref, snapshotName); err != nil {
			return err
		}
		return m.insertEvent(dctx, workspaceID, ref.containerID, "version_rollback", map[string]any{
			"snapshot_name": snapshotName,
			"version":       version,
			"source":        SnapshotSourceRollback,
		})
	}

	if err := m.safeStopTask(dctx, ref.containerID); err != nil {
		return err
	}

	if err := m.replaceLockedContainerFromSnapshot(dctx, ref, "rollback", snapshotName); err != nil {
		return err
	}

	return m.insertEvent(dctx, workspaceID, ref.containerID, "version_rollback", map[string]any{
		"snapshot_name": snapshotName,
		"version":       version,
		"source":        SnapshotSourceRollback,
	})
}

func (m *Manager) VersionSnapshotName(ctx context.Context, botID string, version int) (string, error) {
	if m.computers == nil {
		return "", errors.New("db is not configured")
	}
	if err := validateBotID(botID); err != nil {
		return "", err
	}
	if version < 1 || version > math.MaxInt32 {
		return "", errors.New("version out of range")
	}

	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		return "", err
	}
	return m.computers.GetManagedComputerVersionSnapshotName(ctx, workspaceID, version)
}

// replaceContainerSnapshot prepares a new active snapshot from parentSnapshot,
// deletes the old container, recreates it on the new snapshot, and restarts the task.
// The recreated container keeps its name and labels and gains the workspace
// label when it predates it.
// Caller must pass a detached context (context.WithoutCancel) to guarantee atomicity.
func (m *Manager) replaceContainerSnapshot(ctx context.Context, ref *lockedContainerRef, activeSnapshotName, parentSnapshot string) error {
	workspaceID, botID, containerID, info := ref.workspaceID, ref.botID, ref.containerID, ref.info
	if err := m.service.PrepareSnapshot(ctx, ctr.PrepareSnapshotRequest{
		Target: ctr.StorageRef{Driver: info.StorageRef.Driver, Key: activeSnapshotName, Kind: "active"},
		Parent: ctr.SnapshotRef{Driver: info.StorageRef.Driver, Key: parentSnapshot},
	}); err != nil {
		return err
	}
	if err := m.service.DeleteContainer(ctx, containerID, &ctr.DeleteContainerOptions{CleanupSnapshot: false}); err != nil {
		return err
	}
	spec, err := m.buildVersionSpec(ctx, containerID, botID, workspaceCDIDevicesFromLabels(info.Labels))
	if err != nil {
		return err
	}
	limits, err := m.resourceLimitsForCreate(ctx, workspaceID)
	if err != nil {
		return err
	}
	labels := cloneStringMap(info.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	if strings.TrimSpace(labels[WorkspaceIDLabelKey]) == "" {
		labels[WorkspaceIDLabelKey] = workspaceID
	}
	for k, v := range resourceLimitLabels(limits) {
		labels[k] = v
	}
	if _, err := m.service.RestoreContainer(ctx, ctr.CreateContainerRequest{
		ID:             containerID,
		ImageRef:       info.Image,
		StorageRef:     ctr.StorageRef{Driver: info.StorageRef.Driver, Key: activeSnapshotName, Kind: "active"},
		ResourceLimits: limits,
		Labels:         labels,
		Spec:           spec,
	}); err != nil {
		return err
	}
	// Container process was recreated — evict the stale gRPC connection
	// unconditionally so the next call dials fresh to the new process.
	m.resetBridge(ctx, workspaceID, containerID)

	// Recreate the task and restore the container network before the next
	// workspace operation.
	if err := m.startTaskAndEnsureNetwork(ctx, botID, containerID); err != nil {
		return fmt.Errorf("restart workspace runtime after snapshot replace: %w", err)
	}
	return nil
}

func (m *Manager) commitSnapshotAndReplaceContainer(ctx context.Context, ref *lockedContainerRef, runtimeSnapshotName string) error {
	if err := m.safeStopTask(ctx, ref.containerID); err != nil {
		return err
	}
	if err := m.service.CommitSnapshot(ctx, ctr.CommitSnapshotRequest{
		Source: ctr.StorageRef{Driver: ref.info.StorageRef.Driver, Key: ref.info.StorageRef.Key, Kind: "active"},
		Target: ctr.SnapshotRef{Driver: ref.info.StorageRef.Driver, Key: runtimeSnapshotName, Kind: "committed"},
	}); err != nil {
		if errors.Is(err, ctr.ErrNotSupported) {
			m.resetBridge(ctx, ref.workspaceID, ref.containerID)
			_ = m.startTaskAndEnsureNetwork(ctx, ref.botID, ref.containerID)
			m.resetBridge(ctx, ref.workspaceID, ref.containerID)
		}
		return err
	}
	return m.replaceLockedContainerFromSnapshot(ctx, ref, "active", runtimeSnapshotName)
}

func (m *Manager) replaceLockedContainerFromSnapshot(ctx context.Context, ref *lockedContainerRef, activeLabel, parentSnapshot string) error {
	activeSnapshotName := fmt.Sprintf("%s-%s-%d", ref.containerID, strings.TrimSpace(activeLabel), time.Now().UnixNano())
	return m.replaceContainerSnapshot(ctx, ref, activeSnapshotName, parentSnapshot)
}

func (m *Manager) nativeSnapshotsSupported(ctx context.Context) bool {
	checker, ok := m.service.(interface {
		SnapshotSupported(context.Context) bool
	})
	return !ok || checker.SnapshotSupported(ctx)
}

// buildVersionSpec rebuilds the start parameters of an existing container.
// botID may be empty for a computer nothing is bound to.
func (m *Manager) buildVersionSpec(ctx context.Context, containerID, botID string, cdiDevices []string) (ctr.ContainerSpec, error) {
	if len(cdiDevices) == 0 && botID != "" {
		gpu, err := m.resolveWorkspaceGPU(ctx, botID)
		if err != nil {
			return ctr.ContainerSpec{}, err
		}
		cdiDevices = gpu.Devices
	}
	return m.buildWorkspaceContainerSpec(ctx, containerID, botID, WorkspaceGPUConfig{Devices: cdiDevices})
}

func (m *Manager) safeStopTask(ctx context.Context, containerID string) error {
	err := m.service.StopContainer(ctx, containerID, &ctr.StopTaskOptions{
		Timeout: 10 * time.Second,
		Force:   true,
	})
	if err != nil && !ctr.IsNotFound(err) {
		return err
	}
	if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil && !ctr.IsNotFound(err) {
		return err
	}
	return nil
}

// ensureDBRecords records containerID as the computer's instance. The bot,
// when the computer is operated for one, has to exist.
func (m *Manager) ensureDBRecords(ctx context.Context, workspaceID, botID, containerID, imageRef string) error {
	if m.computers == nil {
		return errors.New("db is not configured")
	}
	if m.queries != nil && botID != "" {
		botUUID, err := db.ParseUUID(botID)
		if err != nil {
			return err
		}
		if _, err := m.queries.GetBotByID(ctx, botUUID); err != nil {
			return err
		}
	}
	return m.computers.UpsertManagedComputerInstance(ctx, dbstore.ManagedComputerInstanceInput{
		WorkspaceID:   workspaceID,
		InstanceID:    containerID,
		InstanceName:  containerID,
		Image:         imageRef,
		Status:        "created",
		Namespace:     "default",
		AutoStart:     true,
		ContainerPath: config.DefaultDataMount,
	})
}

// managedSnapshots lists the snapshot records of the bot's managed computer;
// a bot without a computer has none.
func (m *Manager) managedSnapshots(ctx context.Context, botID string) ([]dbstore.ManagedComputerSnapshotRecord, error) {
	if m.computers == nil {
		return nil, nil
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
			return nil, nil
		}
		return nil, err
	}
	return m.computers.ListManagedComputerSnapshots(ctx, workspaceID)
}

func (m *Manager) recordSnapshotVersion(ctx context.Context, workspaceID, containerID, runtimeSnapshotName, displayName, parentRuntimeSnapshotName, snapshotter, source string) (string, int, time.Time, error) {
	containerID = strings.TrimSpace(containerID)
	runtimeSnapshotName = strings.TrimSpace(runtimeSnapshotName)
	snapshotter = strings.TrimSpace(snapshotter)
	if strings.TrimSpace(workspaceID) == "" || containerID == "" || runtimeSnapshotName == "" || snapshotter == "" {
		return "", 0, time.Time{}, ctr.ErrInvalidArgument
	}
	record, err := m.computers.RecordManagedComputerVersion(ctx, dbstore.RecordManagedComputerVersionInput{
		WorkspaceID:               workspaceID,
		ContainerID:               containerID,
		RuntimeSnapshotName:       runtimeSnapshotName,
		DisplayName:               displayName,
		ParentRuntimeSnapshotName: parentRuntimeSnapshotName,
		Snapshotter:               snapshotter,
		Source:                    normalizeSnapshotSource(source),
	})
	if err != nil {
		return "", 0, time.Time{}, err
	}
	return record.ID, record.Version, record.CreatedAt, nil
}

func (m *Manager) insertEvent(ctx context.Context, workspaceID, containerID, eventType string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return m.computers.InsertManagedComputerEvent(ctx, dbstore.ManagedComputerEventInput{
		ID:          fmt.Sprintf("%s-%d", containerID, time.Now().UnixNano()),
		WorkspaceID: workspaceID,
		ContainerID: containerID,
		EventType:   eventType,
		Payload:     b,
	})
}

func normalizeSnapshotSource(source string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return SnapshotSourceManual
	}
	return s
}

func archiveAwareSnapshotter(snapshotter, runtimeSnapshotName string) string {
	if strings.HasPrefix(strings.TrimSpace(runtimeSnapshotName), archivePrefix) {
		return "archive"
	}
	return snapshotter
}

func coalesceSnapshotName(displayName string, version int) string {
	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		return displayName
	}
	if version > 0 {
		return fmt.Sprintf("Version %d", version)
	}
	return ""
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
