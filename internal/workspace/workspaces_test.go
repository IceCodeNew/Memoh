package workspace

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/felinics/memoh/internal/db"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
)

const (
	wsTestBotID       = "11111111-1111-4111-8111-111111111111"
	wsTestOwnerID     = "33333333-3333-4333-8333-333333333333"
	wsTestManagedID   = "55555555-5555-4555-8555-555555555555"
	wsTestRuntimeID   = "22222222-2222-4222-8222-222222222222"
	wsTestBindingID   = "44444444-4444-4444-8444-444444444444"
	wsTestBindingID2  = "44444444-4444-4444-8444-444444444445"
	wsTestUnknownUUID = "99999999-9999-4999-8999-999999999999"
)

// fakeWorkspaceStore keeps workspaces and bindings in memory with the same
// error contract as the postgres store (db.ErrNotFound, ErrWorkspaceBound).
type fakeWorkspaceStore struct {
	workspaces []dbstore.WorkspaceRecord
	bindings   []dbstore.BotWorkspaceBindingRecord
	nextID     int
}

func (s *fakeWorkspaceStore) newID() string {
	s.nextID++
	return "aaaaaaaa-aaaa-4aaa-8aaa-" + leftPad(strconv.Itoa(s.nextID), 12)
}

func leftPad(value string, width int) string {
	for len(value) < width {
		value = "0" + value
	}
	return value
}

func (s *fakeWorkspaceStore) CreateWorkspace(_ context.Context, input dbstore.CreateWorkspaceInput) (dbstore.WorkspaceRecord, error) {
	id := input.ID
	if id == "" {
		id = s.newID()
	}
	record := dbstore.WorkspaceRecord{ID: id, OwnerUserID: input.OwnerUserID, Kind: input.Kind, Name: input.Name, Metadata: input.Metadata}
	s.workspaces = append(s.workspaces, record)
	return record, nil
}

func (s *fakeWorkspaceStore) GetWorkspace(_ context.Context, workspaceID string) (dbstore.WorkspaceRecord, error) {
	for _, record := range s.workspaces {
		if record.ID == workspaceID {
			return record, nil
		}
	}
	return dbstore.WorkspaceRecord{}, db.ErrNotFound
}

func (s *fakeWorkspaceStore) ListWorkspacesByOwner(_ context.Context, ownerUserID string) ([]dbstore.WorkspaceRecord, error) {
	var out []dbstore.WorkspaceRecord
	for _, record := range s.workspaces {
		if record.OwnerUserID == ownerUserID {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *fakeWorkspaceStore) ListWorkspacesByBot(_ context.Context, botID string) ([]dbstore.WorkspaceRecord, error) {
	var out []dbstore.WorkspaceRecord
	for _, binding := range s.bindings {
		if binding.BotID != botID {
			continue
		}
		for _, record := range s.workspaces {
			if record.ID == binding.WorkspaceID {
				out = append(out, record)
			}
		}
	}
	return out, nil
}

func (s *fakeWorkspaceStore) UpdateWorkspaceName(_ context.Context, workspaceID, name string) (dbstore.WorkspaceRecord, error) {
	for i := range s.workspaces {
		if s.workspaces[i].ID == workspaceID {
			s.workspaces[i].Name = name
			return s.workspaces[i], nil
		}
	}
	return dbstore.WorkspaceRecord{}, db.ErrNotFound
}

func (s *fakeWorkspaceStore) bound(workspaceID string) bool {
	for _, binding := range s.bindings {
		if binding.WorkspaceID == workspaceID {
			return true
		}
	}
	return false
}

func (s *fakeWorkspaceStore) DeleteWorkspace(_ context.Context, workspaceID string) error {
	for i := range s.workspaces {
		if s.workspaces[i].ID != workspaceID {
			continue
		}
		if s.bound(workspaceID) {
			return postgresstore.ErrWorkspaceBound
		}
		s.workspaces = append(s.workspaces[:i], s.workspaces[i+1:]...)
		return nil
	}
	return db.ErrNotFound
}

func (s *fakeWorkspaceStore) DeleteWorkspaceIfUnbound(_ context.Context, workspaceID string) (bool, error) {
	for i := range s.workspaces {
		if s.workspaces[i].ID != workspaceID || s.bound(workspaceID) {
			continue
		}
		s.workspaces = append(s.workspaces[:i], s.workspaces[i+1:]...)
		return true, nil
	}
	return false, nil
}

func (s *fakeWorkspaceStore) UpsertBotWorkspaceBinding(ctx context.Context, botID, workspaceID string, primary bool) (dbstore.BotWorkspaceBindingRecord, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	for _, binding := range s.bindings {
		if binding.BotID == botID && binding.WorkspaceID == workspaceID {
			return binding, nil
		}
	}
	record := dbstore.BotWorkspaceBindingRecord{ID: s.newID(), BotID: botID, WorkspaceID: workspaceID, IsPrimary: primary}
	s.bindings = append(s.bindings, record)
	return record, nil
}

func (s *fakeWorkspaceStore) GetBotWorkspaceBinding(_ context.Context, botID, bindingID string) (dbstore.BotWorkspaceBindingRecord, error) {
	for _, binding := range s.bindings {
		if binding.BotID == botID && binding.ID == bindingID {
			return binding, nil
		}
	}
	return dbstore.BotWorkspaceBindingRecord{}, db.ErrNotFound
}

func (s *fakeWorkspaceStore) ListBotWorkspaceBindingsByBot(_ context.Context, botID string) ([]dbstore.BotWorkspaceBindingRecord, error) {
	var out []dbstore.BotWorkspaceBindingRecord
	for _, binding := range s.bindings {
		if binding.BotID == botID {
			out = append(out, binding)
		}
	}
	return out, nil
}

func (s *fakeWorkspaceStore) ListBotWorkspaceBindingsByWorkspace(_ context.Context, workspaceID string) ([]dbstore.BotWorkspaceBindingRecord, error) {
	var out []dbstore.BotWorkspaceBindingRecord
	for _, binding := range s.bindings {
		if binding.WorkspaceID == workspaceID {
			out = append(out, binding)
		}
	}
	return out, nil
}

func (s *fakeWorkspaceStore) GetPrimaryBotWorkspaceBinding(_ context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, error) {
	for _, binding := range s.bindings {
		if binding.BotID == botID && binding.IsPrimary {
			return binding, nil
		}
	}
	return dbstore.BotWorkspaceBindingRecord{}, db.ErrNotFound
}

func (s *fakeWorkspaceStore) SetPrimaryBotWorkspaceBinding(_ context.Context, botID, bindingID string) error {
	found := false
	for i := range s.bindings {
		if s.bindings[i].BotID == botID && s.bindings[i].ID == bindingID {
			found = true
		}
	}
	if !found {
		return db.ErrNotFound
	}
	for i := range s.bindings {
		if s.bindings[i].BotID == botID {
			s.bindings[i].IsPrimary = s.bindings[i].ID == bindingID
		}
	}
	return nil
}

func (s *fakeWorkspaceStore) UpdateBotWorkspaceBindingToolApproval(_ context.Context, botID, bindingID string, config dbstore.JSON) (dbstore.BotWorkspaceBindingRecord, error) {
	for i := range s.bindings {
		if s.bindings[i].BotID == botID && s.bindings[i].ID == bindingID {
			s.bindings[i].ToolApproval = config
			return s.bindings[i], nil
		}
	}
	return dbstore.BotWorkspaceBindingRecord{}, db.ErrNotFound
}

func (s *fakeWorkspaceStore) DeleteBotWorkspaceBinding(_ context.Context, botID, bindingID string) error {
	for i := range s.bindings {
		if s.bindings[i].BotID == botID && s.bindings[i].ID == bindingID {
			s.bindings = append(s.bindings[:i], s.bindings[i+1:]...)
			return nil
		}
	}
	return db.ErrNotFound
}

func (s *fakeWorkspaceStore) DeleteBotWorkspaceBindingsByWorkspace(_ context.Context, workspaceID string) (int64, error) {
	var kept []dbstore.BotWorkspaceBindingRecord
	var removed int64
	for _, binding := range s.bindings {
		if binding.WorkspaceID == workspaceID {
			removed++
			continue
		}
		kept = append(kept, binding)
	}
	s.bindings = kept
	return removed, nil
}

func newBindingFixture(t *testing.T) (*fakeWorkspaceStore, *WorkspaceService, *BindingService) {
	t.Helper()
	store := &fakeWorkspaceStore{}
	return store, NewWorkspaceService(store), NewBindingService(store, store)
}

func TestResolvePrimaryReturnsManagedWorkspaceWhenNativeIsPrimary(t *testing.T) {
	store, _, bindings := newBindingFixture(t)
	store.workspaces = []dbstore.WorkspaceRecord{
		{ID: wsTestManagedID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "bot"},
		{ID: wsTestRuntimeID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindUser, Name: "Office Mac"},
	}
	store.bindings = []dbstore.BotWorkspaceBindingRecord{
		{ID: wsTestBindingID, BotID: wsTestBotID, WorkspaceID: wsTestManagedID, IsPrimary: true},
		{ID: wsTestBindingID2, BotID: wsTestBotID, WorkspaceID: wsTestRuntimeID},
	}

	binding, workspace, err := bindings.ResolvePrimary(context.Background(), wsTestBotID)
	if err != nil {
		t.Fatalf("ResolvePrimary() error = %v", err)
	}
	if binding.ID != wsTestBindingID || workspace.ID != wsTestManagedID || workspace.Kind != WorkspaceKindManaged {
		t.Fatalf("ResolvePrimary() = binding %s workspace %s/%s, want managed binding", binding.ID, workspace.ID, workspace.Kind)
	}
}

func TestResolvePrimaryReturnsUserWorkspaceWhenRemoteMountIsPrimary(t *testing.T) {
	store, _, bindings := newBindingFixture(t)
	store.workspaces = []dbstore.WorkspaceRecord{
		{ID: wsTestManagedID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "bot"},
		{ID: wsTestRuntimeID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindUser, Name: "Office Mac"},
	}
	store.bindings = []dbstore.BotWorkspaceBindingRecord{
		{ID: wsTestBindingID, BotID: wsTestBotID, WorkspaceID: wsTestManagedID},
		{ID: wsTestBindingID2, BotID: wsTestBotID, WorkspaceID: wsTestRuntimeID, IsPrimary: true},
	}

	binding, workspace, err := bindings.ResolvePrimary(context.Background(), wsTestBotID)
	if err != nil {
		t.Fatalf("ResolvePrimary() error = %v", err)
	}
	if binding.ID != wsTestBindingID2 || workspace.ID != wsTestRuntimeID || workspace.Kind != WorkspaceKindUser {
		t.Fatalf("ResolvePrimary() = binding %s workspace %s/%s, want the remote mount", binding.ID, workspace.ID, workspace.Kind)
	}
}

func TestResolvePrimaryReportsBotWithoutPrimary(t *testing.T) {
	store, _, bindings := newBindingFixture(t)
	store.workspaces = []dbstore.WorkspaceRecord{{ID: wsTestManagedID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "bot"}}
	store.bindings = []dbstore.BotWorkspaceBindingRecord{{ID: wsTestBindingID, BotID: wsTestBotID, WorkspaceID: wsTestManagedID}}

	if _, _, err := bindings.ResolvePrimary(context.Background(), wsTestBotID); !errors.Is(err, ErrNoPrimaryWorkspace) {
		t.Fatalf("ResolvePrimary() error = %v, want ErrNoPrimaryWorkspace", err)
	}
	if _, _, err := bindings.ResolvePrimary(context.Background(), "not-a-uuid"); !errors.Is(err, ErrNoPrimaryWorkspace) {
		t.Fatalf("ResolvePrimary(invalid) error = %v, want ErrNoPrimaryWorkspace", err)
	}
}

func TestBindPrimaryReplacesPreviousPrimary(t *testing.T) {
	store, workspaces, bindings := newBindingFixture(t)
	managed, err := workspaces.CreateManaged(context.Background(), wsTestOwnerID, "bot")
	if err != nil {
		t.Fatalf("CreateManaged() error = %v", err)
	}
	user, err := workspaces.CreateUser(context.Background(), wsTestRuntimeID, wsTestOwnerID, "Office Mac")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if user.ID != wsTestRuntimeID || user.Kind != WorkspaceKindUser {
		t.Fatalf("CreateUser() = %+v, want the runtime id and kind user", user)
	}

	first, err := bindings.Bind(context.Background(), wsTestBotID, managed.ID, true)
	if err != nil || !first.IsPrimary {
		t.Fatalf("Bind(managed, primary) = %+v, %v", first, err)
	}
	second, err := bindings.Bind(context.Background(), wsTestBotID, user.ID, true)
	if err != nil || !second.IsPrimary {
		t.Fatalf("Bind(user, primary) = %+v, %v", second, err)
	}
	primary, err := bindings.Primary(context.Background(), wsTestBotID)
	if err != nil || primary.ID != second.ID {
		t.Fatalf("Primary() = %+v, %v; want the user binding", primary, err)
	}
	// Binding again is idempotent and keeps the existing primary flag.
	again, err := bindings.Bind(context.Background(), wsTestBotID, managed.ID, false)
	if err != nil || again.ID != first.ID || again.IsPrimary {
		t.Fatalf("Bind(managed again) = %+v, %v; want the same non-primary binding", again, err)
	}
	if len(store.bindings) != 2 {
		t.Fatalf("bindings = %d, want 2", len(store.bindings))
	}
	if err := bindings.SetPrimary(context.Background(), wsTestBotID, first.ID); err != nil {
		t.Fatalf("SetPrimary() error = %v", err)
	}
	if _, workspace, err := bindings.ResolvePrimary(context.Background(), wsTestBotID); err != nil || workspace.ID != managed.ID {
		t.Fatalf("ResolvePrimary() after SetPrimary = %s, %v; want %s", workspace.ID, err, managed.ID)
	}
}

func TestBindUnknownWorkspaceAndBindingErrors(t *testing.T) {
	_, _, bindings := newBindingFixture(t)
	if _, err := bindings.Bind(context.Background(), wsTestBotID, wsTestUnknownUUID, false); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Bind(unknown workspace) error = %v, want ErrWorkspaceNotFound", err)
	}
	if err := bindings.Unbind(context.Background(), wsTestBotID, wsTestUnknownUUID); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("Unbind(unknown) error = %v, want ErrBindingNotFound", err)
	}
	if err := bindings.SetPrimary(context.Background(), wsTestBotID, wsTestUnknownUUID); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("SetPrimary(unknown) error = %v, want ErrBindingNotFound", err)
	}
	if _, err := bindings.UpdateToolApproval(context.Background(), wsTestBotID, wsTestUnknownUUID, settings.ToolApprovalConfig{}); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("UpdateToolApproval(unknown) error = %v, want ErrBindingNotFound", err)
	}
}

func TestUpdateToolApprovalPersistsNormalizedConfig(t *testing.T) {
	store, _, bindings := newBindingFixture(t)
	store.workspaces = []dbstore.WorkspaceRecord{{ID: wsTestRuntimeID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindUser, Name: "Office Mac"}}
	store.bindings = []dbstore.BotWorkspaceBindingRecord{{ID: wsTestBindingID, BotID: wsTestBotID, WorkspaceID: wsTestRuntimeID}}

	config := DefaultRemoteToolApprovalConfig()
	config.Exec.Mode = settings.ToolApprovalAsk
	record, err := bindings.UpdateToolApproval(context.Background(), wsTestBotID, wsTestBindingID, config)
	if err != nil {
		t.Fatalf("UpdateToolApproval() error = %v", err)
	}
	if got := toolApprovalConfig(record.ToolApproval); got.Exec.Mode != settings.ToolApprovalAsk || got.Read.Mode != settings.ToolApprovalAllow {
		t.Fatalf("stored approval = %+v, want exec ask / read allow", got)
	}
}

func TestDeleteRefusesBoundWorkspaceAndDeleteIfUnboundReleasesIt(t *testing.T) {
	store, workspaces, bindings := newBindingFixture(t)
	managed, err := workspaces.CreateManaged(context.Background(), wsTestOwnerID, "bot")
	if err != nil {
		t.Fatalf("CreateManaged() error = %v", err)
	}
	binding, err := bindings.Bind(context.Background(), wsTestBotID, managed.ID, true)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	if err := workspaces.Delete(context.Background(), managed.ID); !errors.Is(err, ErrWorkspaceBound) {
		t.Fatalf("Delete(bound) error = %v, want ErrWorkspaceBound", err)
	}
	if deleted, err := workspaces.DeleteIfUnbound(context.Background(), managed.ID); err != nil || deleted {
		t.Fatalf("DeleteIfUnbound(bound) = %v, %v; want false", deleted, err)
	}
	if err := bindings.Unbind(context.Background(), wsTestBotID, binding.ID); err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if deleted, err := workspaces.DeleteIfUnbound(context.Background(), managed.ID); err != nil || !deleted {
		t.Fatalf("DeleteIfUnbound(unbound) = %v, %v; want true", deleted, err)
	}
	if _, err := workspaces.Get(context.Background(), managed.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrWorkspaceNotFound", err)
	}
	if err := workspaces.Delete(context.Background(), managed.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrWorkspaceNotFound", err)
	}
	if len(store.workspaces) != 0 {
		t.Fatalf("workspaces left = %d, want 0", len(store.workspaces))
	}
}

func TestWorkspaceServiceValidatesInput(t *testing.T) {
	_, workspaces, _ := newBindingFixture(t)
	if _, err := workspaces.CreateManaged(context.Background(), wsTestOwnerID, "   "); !errors.Is(err, ErrInvalidWorkspaceName) {
		t.Fatalf("CreateManaged(blank name) error = %v, want ErrInvalidWorkspaceName", err)
	}
	if _, err := workspaces.CreateUser(context.Background(), "runtime", wsTestOwnerID, "pc"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("CreateUser(bad runtime id) error = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := workspaces.Get(context.Background(), wsTestUnknownUUID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Get(unknown) error = %v, want ErrWorkspaceNotFound", err)
	}
	owned, err := workspaces.ListByOwner(context.Background(), wsTestOwnerID)
	if err != nil || len(owned) != 0 {
		t.Fatalf("ListByOwner() = %v, %v; want empty", owned, err)
	}
}
