package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	lifecycleBotID      = "00000000-0000-0000-0000-000000000001"
	lifecycleBotID2     = "00000000-0000-0000-0000-000000000002"
	lifecycleComputerID = "00000000-0000-0000-0000-0000000000c1"
	lifecycleOrphanBot  = "00000000-0000-0000-0000-0000000000e1"
)

// fakeComputerStore keeps managed computer records in memory; only the
// instance columns and resource limits matter to the manager paths under
// test.
type fakeComputerStore struct {
	records map[string]dbstore.ManagedComputerRecord
	limits  map[string]dbstore.ManagedComputerResourceLimits
	cleared []string
}

func newFakeComputerStore() *fakeComputerStore {
	return &fakeComputerStore{
		records: make(map[string]dbstore.ManagedComputerRecord),
		limits:  make(map[string]dbstore.ManagedComputerResourceLimits),
	}
}

func (s *fakeComputerStore) GetManagedComputer(_ context.Context, workspaceID string) (dbstore.ManagedComputerRecord, error) {
	record, ok := s.records[workspaceID]
	if !ok {
		return dbstore.ManagedComputerRecord{}, db.ErrNotFound
	}
	return record, nil
}

func (s *fakeComputerStore) GetManagedComputerByInstanceID(_ context.Context, instanceID string) (dbstore.ManagedComputerRecord, error) {
	for _, record := range s.records {
		if record.InstanceID == instanceID && instanceID != "" {
			return record, nil
		}
	}
	return dbstore.ManagedComputerRecord{}, db.ErrNotFound
}

func (s *fakeComputerStore) ListAutoStartManagedComputers(context.Context) ([]dbstore.ManagedComputerRecord, error) {
	var out []dbstore.ManagedComputerRecord
	for _, record := range s.records {
		if record.AutoStart && record.InstanceID != "" {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *fakeComputerStore) UpsertManagedComputerInstance(_ context.Context, input dbstore.ManagedComputerInstanceInput) error {
	record := s.records[input.WorkspaceID]
	record.WorkspaceID = input.WorkspaceID
	record.InstanceID = input.InstanceID
	record.InstanceName = input.InstanceName
	record.Image = input.Image
	record.InstanceStatus = input.Status
	record.Namespace = input.Namespace
	record.AutoStart = input.AutoStart
	record.ContainerPath = input.ContainerPath
	s.records[input.WorkspaceID] = record
	return nil
}

func (s *fakeComputerStore) ClearManagedComputerInstance(_ context.Context, workspaceID string) error {
	s.cleared = append(s.cleared, workspaceID)
	record, ok := s.records[workspaceID]
	if !ok {
		return nil
	}
	record.InstanceID = ""
	record.InstanceName = ""
	record.InstanceStatus = ""
	s.records[workspaceID] = record
	return nil
}

func (s *fakeComputerStore) setStatus(workspaceID, status string) error {
	record, ok := s.records[workspaceID]
	if !ok {
		return db.ErrNotFound
	}
	record.InstanceStatus = status
	s.records[workspaceID] = record
	return nil
}

func (s *fakeComputerStore) MarkManagedComputerStarted(_ context.Context, workspaceID string) error {
	return s.setStatus(workspaceID, "running")
}

func (s *fakeComputerStore) MarkManagedComputerStopped(_ context.Context, workspaceID string) error {
	return s.setStatus(workspaceID, "stopped")
}

func (s *fakeComputerStore) UpdateManagedComputerInstanceStatus(_ context.Context, workspaceID, status string) error {
	return s.setStatus(workspaceID, status)
}

func (*fakeComputerStore) ListManagedComputerSnapshots(context.Context, string) ([]dbstore.ManagedComputerSnapshotRecord, error) {
	return nil, nil
}

func (*fakeComputerStore) ListManagedComputerVersions(context.Context, string) ([]dbstore.ManagedComputerVersionRecord, error) {
	return nil, nil
}

func (*fakeComputerStore) GetManagedComputerVersionSnapshotName(context.Context, string, int) (string, error) {
	return "", db.ErrNotFound
}

func (*fakeComputerStore) RecordManagedComputerVersion(context.Context, dbstore.RecordManagedComputerVersionInput) (dbstore.ManagedComputerVersionRecord, error) {
	return dbstore.ManagedComputerVersionRecord{}, errors.New("not implemented")
}

func (*fakeComputerStore) InsertManagedComputerEvent(context.Context, dbstore.ManagedComputerEventInput) error {
	return nil
}

func (s *fakeComputerStore) GetManagedComputerResourceLimits(_ context.Context, workspaceID string) (dbstore.ManagedComputerResourceLimits, error) {
	limits, ok := s.limits[workspaceID]
	if !ok {
		return dbstore.ManagedComputerResourceLimits{}, db.ErrNotFound
	}
	return limits, nil
}

func (s *fakeComputerStore) UpsertManagedComputerResourceLimits(_ context.Context, workspaceID string, limits dbstore.ManagedComputerResourceLimits) error {
	s.limits[workspaceID] = limits
	return nil
}

var _ dbstore.ManagedComputerStore = (*fakeComputerStore)(nil)

// ---------------------------------------------------------------------------
// Container resolution order: record → workspace label → bot label → scan →
// a new name after the computer.
// ---------------------------------------------------------------------------

func TestWorkspaceContainerIDPrefersRecordedInstance(t *testing.T) {
	t.Parallel()

	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{{
			ID:        "workspace-from-label",
			Labels:    map[string]string{WorkspaceIDLabelKey: lifecycleComputerID},
			UpdatedAt: time.Now(),
		}},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	store := newFakeComputerStore()
	store.records[lifecycleComputerID] = dbstore.ManagedComputerRecord{WorkspaceID: lifecycleComputerID, InstanceID: "workspace-recorded"}
	m.SetManagedComputerStore(store)

	got, err := m.ContainerID(context.Background(), lifecycleBotID)
	if err != nil || got != "workspace-recorded" {
		t.Fatalf("ContainerID() = %q, %v; want the recorded instance", got, err)
	}
}

func TestWorkspaceContainerIDUsesWorkspaceLabelBeforeBotLabel(t *testing.T) {
	t.Parallel()

	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{
			{ID: "workspace-from-bot-label", Labels: map[string]string{BotLabelKey: lifecycleBotID}, UpdatedAt: time.Now()},
			{ID: "workspace-from-ws-label", Labels: map[string]string{WorkspaceIDLabelKey: lifecycleComputerID}, UpdatedAt: time.Now().Add(-time.Hour)},
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	m.SetManagedComputerStore(newFakeComputerStore())

	got, err := m.ContainerID(context.Background(), lifecycleBotID)
	if err != nil || got != "workspace-from-ws-label" {
		t.Fatalf("ContainerID() = %q, %v; want the workspace-labelled container", got, err)
	}
}

func TestWorkspaceContainerIDFallsBackToBoundBotLabel(t *testing.T) {
	t.Parallel()

	// A container created before computers had their own ids: labelled and
	// named after the bot, while the bot's computer has another id.
	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{{
			ID:        ContainerPrefix + lifecycleBotID,
			Labels:    map[string]string{BotLabelKey: lifecycleBotID},
			UpdatedAt: time.Now(),
		}},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	m.SetManagedComputerStore(newFakeComputerStore())

	got, err := m.workspaceContainerID(context.Background(), lifecycleComputerID)
	if err != nil || got != ContainerPrefix+lifecycleBotID {
		t.Fatalf("workspaceContainerID() = %q, %v; want the bot-labelled container", got, err)
	}
	// Its host resources stay where the container was started with them.
	if want := filepath.Join(m.dataRoot(), "run", lifecycleBotID); m.socketDir(got) != want {
		t.Fatalf("socketDir() = %q, want %q", m.socketDir(got), want)
	}
}

func TestWorkspaceContainerIDScansByInstanceKeyAndBoundBotName(t *testing.T) {
	t.Parallel()

	for name, containerID := range map[string]string{
		"named after the computer": ContainerPrefix + lifecycleComputerID,
		"named after a bound bot":  ContainerPrefix + lifecycleBotID,
		"legacy mcp name":          LegacyContainerPrefix + lifecycleBotID,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := &legacyRouteTestService{created: true, container: ctr.ContainerInfo{ID: containerID, UpdatedAt: time.Now()}}
			m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
			bindTestComputer(m, lifecycleBotID, lifecycleComputerID)

			got, err := m.workspaceContainerID(context.Background(), lifecycleComputerID)
			if err != nil || got != containerID {
				t.Fatalf("workspaceContainerID() = %q, %v; want %q", got, err, containerID)
			}
		})
	}
}

func TestWorkspaceContainerIDIgnoresOtherComputersContainers(t *testing.T) {
	t.Parallel()

	const otherComputer = "00000000-0000-0000-0000-0000000000c2"
	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{
			// Named after the computer but labelled for another one: the
			// label wins.
			{ID: ContainerPrefix + lifecycleComputerID, Labels: map[string]string{WorkspaceIDLabelKey: otherComputer}, UpdatedAt: time.Now()},
			// Named after a bot that is not bound to the computer.
			{ID: ContainerPrefix + lifecycleBotID2, UpdatedAt: time.Now()},
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)

	if got, err := m.workspaceContainerID(context.Background(), lifecycleComputerID); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("workspaceContainerID() = %q, %v; want ErrContainerNotFound", got, err)
	}
}

func TestWorkspaceContainerIDNamesNewContainersAfterTheComputer(t *testing.T) {
	t.Parallel()

	m := newLegacyRouteTestManager(t, &legacyRouteTestService{}, config.WorkspaceConfig{})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	ctx := context.Background()

	_, err := m.ContainerID(ctx, lifecycleBotID)
	if !errors.Is(err, ErrContainerNotFound) || !ctr.IsNotFound(err) {
		t.Fatalf("ContainerID() error = %v; want ErrContainerNotFound that is also a runtime not-found", err)
	}
	if got := m.workspaceContainerIDOrNew(ctx, lifecycleComputerID); got != ContainerPrefix+lifecycleComputerID {
		t.Fatalf("workspaceContainerIDOrNew() = %q, want %q", got, ContainerPrefix+lifecycleComputerID)
	}
	// Dial-only paths address the container the computer will have.
	if got, want := m.DisplaySocketPath(ctx, lifecycleBotID), filepath.Join(m.dataRoot(), "run", lifecycleComputerID, DisplayRFBSocketName); got != want {
		t.Fatalf("DisplaySocketPath() without a container = %q, want %q", got, want)
	}
	if got, want := m.dialTarget(m.workspaceContainerIDOrNew(ctx, lifecycleComputerID)), "unix://"+filepath.Join(m.dataRoot(), "run", lifecycleComputerID, "bridge.sock"); got != want {
		t.Fatalf("dialTarget() without a container = %q, want %q", got, want)
	}
}

func TestBotWithoutManagedComputerHasNoContainer(t *testing.T) {
	t.Parallel()

	// The container is named after the bot, but the bindings know the bot
	// has no computer: the manager does not guess.
	svc := &legacyRouteTestService{created: true, container: ctr.ContainerInfo{ID: ContainerPrefix + lifecycleBotID}}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	m.SetBindingService(NewBindingService(&fakeWorkspaceStore{}, &fakeWorkspaceStore{}))
	ctx := context.Background()

	if _, err := m.ContainerID(ctx, lifecycleBotID); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("ContainerID() error = %v, want ErrContainerNotFound", err)
	}
	if _, err := m.GetContainerInfo(ctx, lifecycleBotID); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("GetContainerInfo() error = %v, want ErrContainerNotFound", err)
	}
	if err := m.CleanupBotContainer(ctx, lifecycleBotID, false); err != nil || svc.deleteCalls != 0 {
		t.Fatalf("CleanupBotContainer() = %v, deletes = %d; want nothing to do", err, svc.deleteCalls)
	}
	if err := m.StartWithImage(ctx, lifecycleBotID, "debian"); !errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
		t.Fatalf("StartWithImage() error = %v, want ErrNoManagedWorkspaceBinding", err)
	}
	if _, err := m.NativeMCPClient(ctx, lifecycleBotID); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("NativeMCPClient() error = %v, want ErrContainerNotFound", err)
	}
}

// Without a binding service every bot is its own computer, as before 0167:
// the bot-named container is found, created and dialed under the bot id.
func TestManagerWithoutBindingsTreatsBotAsItsOwnComputer(t *testing.T) {
	t.Parallel()

	dataRoot := t.TempDir()
	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		DataRoot:     dataRoot,
		Snapshotter:  "overlayfs",
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})
	ctx := context.Background()

	if got := m.DisplaySocketPath(ctx, lifecycleBotID); got != filepath.Join(dataRoot, "run", lifecycleBotID, DisplayRFBSocketName) {
		t.Fatalf("DisplaySocketPath() = %q, want the bot-named socket", got)
	}
	if err := m.StartWithImage(ctx, lifecycleBotID, "debian"); err != nil {
		t.Fatalf("StartWithImage() error = %v", err)
	}
	if svc.createdReq.ID != ContainerPrefix+lifecycleBotID {
		t.Fatalf("container id = %q, want %q", svc.createdReq.ID, ContainerPrefix+lifecycleBotID)
	}
	if got := svc.createdReq.Labels[WorkspaceIDLabelKey]; got != lifecycleBotID {
		t.Fatalf("%s label = %q, want the bot id", WorkspaceIDLabelKey, got)
	}
	if got, err := m.ContainerID(ctx, lifecycleBotID); err != nil || got != ContainerPrefix+lifecycleBotID {
		t.Fatalf("ContainerID() = %q, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// ListBots: container → computer → bound bots.
// ---------------------------------------------------------------------------

func TestListBotsResolvesContainersToBoundBots(t *testing.T) {
	t.Parallel()

	const sharedComputer = "00000000-0000-0000-0000-0000000000c3"
	const recordedComputer = "00000000-0000-0000-0000-0000000000c4"
	const recordedBot = "00000000-0000-0000-0000-000000000004"
	svc := &legacyRouteTestService{
		byLabel: []ctr.ContainerInfo{
			// Labelled with the computer; two bots share it.
			{ID: ContainerPrefix + sharedComputer, Labels: map[string]string{WorkspaceIDLabelKey: sharedComputer, BotLabelKey: lifecycleBotID}},
			// Older container: bot label only, bot bound to another id.
			{ID: ContainerPrefix + lifecycleBotID2, Labels: map[string]string{BotLabelKey: lifecycleBotID2}},
			// Known only through the computer record.
			{ID: "workspace-recorded"},
			// Nothing knows this one: the bot it names is reported as is.
			{ID: ContainerPrefix + lifecycleOrphanBot},
			// Not a workspace container at all.
			{ID: "sidecar-1"},
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})
	bindTestComputers(m, map[string][]string{
		sharedComputer:      {lifecycleBotID, lifecycleBotID2},
		lifecycleComputerID: {lifecycleBotID2},
		recordedComputer:    {recordedBot},
	})
	store := newFakeComputerStore()
	store.records[recordedComputer] = dbstore.ManagedComputerRecord{WorkspaceID: recordedComputer, InstanceID: "workspace-recorded"}
	m.SetManagedComputerStore(store)

	got, err := m.ListBots(context.Background())
	if err != nil {
		t.Fatalf("ListBots() error = %v", err)
	}
	want := []string{lifecycleBotID, lifecycleBotID2, recordedBot, lifecycleOrphanBot}
	if !slices.Equal(got, want) {
		t.Fatalf("ListBots() = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Preserved data: written under the computer, read back from either id.
// ---------------------------------------------------------------------------

func TestPreservedBackupPathFallsBackToBotArchive(t *testing.T) {
	t.Parallel()

	dataRoot := t.TempDir()
	m := newLegacyRouteTestManager(t, &legacyRouteTestService{}, config.WorkspaceConfig{DataRoot: dataRoot})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	ctx := context.Background()
	touch := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	botArchive := filepath.Join(dataRoot, backupsSubdir, lifecycleBotID+".tar.gz")
	computerArchive := filepath.Join(dataRoot, backupsSubdir, lifecycleComputerID+".tar.gz")

	if m.HasPreservedData(ctx, lifecycleBotID) {
		t.Fatal("HasPreservedData() with no archive")
	}

	// An archive exported before the computer had its own id sits under
	// the bot id and is still found for the bot.
	touch(botArchive)
	if got := m.preservedBackupPath(lifecycleComputerID, lifecycleBotID); got != botArchive {
		t.Fatalf("preservedBackupPath() = %q, want the bot archive %q", got, botArchive)
	}
	if !m.HasPreservedData(ctx, lifecycleBotID) {
		t.Fatal("HasPreservedData() missed the bot archive")
	}
	// Without a bot only the computer's own archive is known.
	if got := m.preservedBackupPath(lifecycleComputerID, ""); got != "" {
		t.Fatalf("preservedBackupPath() without a bot = %q, want empty", got)
	}

	// New exports go under the computer and take precedence.
	touch(computerArchive)
	if got := m.backupPath(lifecycleComputerID); got != computerArchive {
		t.Fatalf("backupPath() = %q, want %q", got, computerArchive)
	}
	if got := m.preservedBackupPath(lifecycleComputerID, lifecycleBotID); got != computerArchive {
		t.Fatalf("preservedBackupPath() = %q, want the computer archive %q", got, computerArchive)
	}
	if status, err := m.GetContainerInfo(ctx, lifecycleBotID); err == nil || status != nil {
		t.Fatalf("GetContainerInfo() without a container = %+v, %v", status, err)
	}
}

// ---------------------------------------------------------------------------
// Teardown by instance: a computer nothing is bound to still loses its
// container.
// ---------------------------------------------------------------------------

func TestTeardownUnboundComputerRemovesItsContainer(t *testing.T) {
	t.Parallel()

	containerID := ContainerPrefix + lifecycleComputerID
	svc := &legacyRouteTestService{created: true, container: ctr.ContainerInfo{ID: containerID}}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		DataRoot:     t.TempDir(),
		CNIBinaryDir: "/opt/cni/bin",
		CNIConfigDir: "/etc/cni/net.d",
	})
	m.SetBindingService(NewBindingService(&fakeWorkspaceStore{}, &fakeWorkspaceStore{}))
	store := newFakeComputerStore()
	store.records[lifecycleComputerID] = dbstore.ManagedComputerRecord{WorkspaceID: lifecycleComputerID, InstanceID: containerID, InstanceStatus: "running"}
	m.SetManagedComputerStore(store)
	ctx := context.Background()

	if insp, err := m.Inspect(ctx, lifecycleComputerID); err != nil || !insp.Exists {
		t.Fatalf("Inspect() = %+v, %v; want present", insp, err)
	}
	if err := m.Teardown(ctx, lifecycleComputerID, false); err != nil {
		t.Fatalf("Teardown() = %v", err)
	}
	if svc.deleteCalls != 1 || svc.deleteTask != 1 || svc.removeNet != 1 {
		t.Fatalf("teardown calls: delete=%d deleteTask=%d removeNet=%d; want one each", svc.deleteCalls, svc.deleteTask, svc.removeNet)
	}
	if !slices.Contains(store.cleared, lifecycleComputerID) {
		t.Fatal("instance record not cleared")
	}
	if insp, err := m.Inspect(ctx, lifecycleComputerID); err != nil || insp.Exists {
		t.Fatalf("Inspect() after teardown = %+v, %v; want absent", insp, err)
	}
}

// ---------------------------------------------------------------------------
// GetContainerInfo follows the same resolution and reports preserved data by
// computer.
// ---------------------------------------------------------------------------

func TestGetContainerInfoReportsRecordedInstance(t *testing.T) {
	t.Parallel()

	containerID := ContainerPrefix + lifecycleComputerID
	svc := &legacyRouteTestService{created: true, container: ctr.ContainerInfo{ID: containerID, Image: "debian"}}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})
	bindTestComputer(m, lifecycleBotID, lifecycleComputerID)
	store := newFakeComputerStore()
	store.records[lifecycleComputerID] = dbstore.ManagedComputerRecord{WorkspaceID: lifecycleComputerID, InstanceID: containerID, InstanceStatus: "stopped", Image: "debian", Namespace: "memoh"}
	m.SetManagedComputerStore(store)

	status, err := m.GetContainerInfo(context.Background(), lifecycleBotID)
	if err != nil {
		t.Fatalf("GetContainerInfo() error = %v", err)
	}
	if status.ContainerID != containerID || status.Status != "stopped" || status.Namespace != "memoh" || status.HasPreservedData {
		t.Fatalf("GetContainerInfo() = %+v", status)
	}

	// A record whose container the runtime lost reports as missing.
	svc.created = false
	if _, err := m.GetContainerInfo(context.Background(), lifecycleBotID); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("GetContainerInfo() with a stale record = %v, want ErrContainerNotFound", err)
	}
}
