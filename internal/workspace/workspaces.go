package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/db"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/settings"
)

// Workspace kinds, re-exported for callers that only import this package.
const (
	WorkspaceKindManaged = dbstore.WorkspaceKindManaged
	WorkspaceKindUser    = dbstore.WorkspaceKindUser
)

var (
	ErrWorkspaceNotFound  = errors.New("workspace not found")
	ErrBindingNotFound    = errors.New("workspace binding not found")
	ErrNoPrimaryWorkspace = errors.New("bot has no primary workspace")
	// ErrWorkspaceBound: the workspace cannot be deleted while a bot is bound
	// to it.
	ErrWorkspaceBound = errors.New("workspace is still bound to a bot")
	// ErrInvalidWorkspaceName: a workspace needs a non-blank display name.
	ErrInvalidWorkspaceName = errors.New("workspace name must not be blank")
)

// WorkspaceService owns the computers a team knows about: the managed
// workspace provisioned for a bot and the user's own machines paired through
// Remote Runtime. Provisioning stays with the botworkspace reconciler and
// live runtime connections with userruntime.Service; this service only keeps
// the records.
type WorkspaceService struct {
	store dbstore.WorkspaceStore
}

func NewWorkspaceService(store dbstore.WorkspaceStore) *WorkspaceService {
	return &WorkspaceService{store: store}
}

// CreateManaged records a server-provisioned workspace with a fresh id.
func (s *WorkspaceService) CreateManaged(ctx context.Context, ownerUserID, name string) (dbstore.WorkspaceRecord, error) {
	return s.create(ctx, "", ownerUserID, WorkspaceKindManaged, name)
}

// CreateUser records a paired computer. runtimeID is the user_runtimes id,
// which the workspace shares so remote mounts keep addressing it.
func (s *WorkspaceService) CreateUser(ctx context.Context, runtimeID, ownerUserID, name string) (dbstore.WorkspaceRecord, error) {
	runtimeID, ok := canonicalWorkspaceUUID(runtimeID)
	if !ok {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	return s.create(ctx, runtimeID, ownerUserID, WorkspaceKindUser, name)
}

func (s *WorkspaceService) create(ctx context.Context, id, ownerUserID, kind, name string) (dbstore.WorkspaceRecord, error) {
	if s == nil || s.store == nil {
		return dbstore.WorkspaceRecord{}, errors.New("workspace service not configured")
	}
	ownerUserID, ok := canonicalWorkspaceUUID(ownerUserID)
	if !ok {
		return dbstore.WorkspaceRecord{}, errors.New("workspace owner user id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return dbstore.WorkspaceRecord{}, ErrInvalidWorkspaceName
	}
	return s.store.CreateWorkspace(ctx, dbstore.CreateWorkspaceInput{
		ID: id, OwnerUserID: ownerUserID, Kind: kind, Name: name,
	})
}

func (s *WorkspaceService) Get(ctx context.Context, workspaceID string) (dbstore.WorkspaceRecord, error) {
	if s == nil || s.store == nil {
		return dbstore.WorkspaceRecord{}, errors.New("workspace service not configured")
	}
	workspaceID, ok := canonicalWorkspaceUUID(workspaceID)
	if !ok {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	record, err := s.store.GetWorkspace(ctx, workspaceID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	return record, err
}

func (s *WorkspaceService) ListByOwner(ctx context.Context, ownerUserID string) ([]dbstore.WorkspaceRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("workspace service not configured")
	}
	ownerUserID, ok := canonicalWorkspaceUUID(ownerUserID)
	if !ok {
		return nil, errors.New("workspace owner user id is required")
	}
	return s.store.ListWorkspacesByOwner(ctx, ownerUserID)
}

// Delete removes a workspace nothing is bound to. A bound workspace is
// reported as ErrWorkspaceBound; unbind first.
func (s *WorkspaceService) Delete(ctx context.Context, workspaceID string) error {
	if s == nil || s.store == nil {
		return errors.New("workspace service not configured")
	}
	workspaceID, ok := canonicalWorkspaceUUID(workspaceID)
	if !ok {
		return ErrWorkspaceNotFound
	}
	err := s.store.DeleteWorkspace(ctx, workspaceID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return ErrWorkspaceNotFound
	case errors.Is(err, postgresstore.ErrWorkspaceBound):
		return ErrWorkspaceBound
	}
	return err
}

// DeleteIfUnbound removes the workspace when no binding references it and
// reports whether it did. A still-bound or already-missing workspace is not
// an error: callers use it as "release if this was the last user".
func (s *WorkspaceService) DeleteIfUnbound(ctx context.Context, workspaceID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("workspace service not configured")
	}
	workspaceID, ok := canonicalWorkspaceUUID(workspaceID)
	if !ok {
		return false, ErrWorkspaceNotFound
	}
	return s.store.DeleteWorkspaceIfUnbound(ctx, workspaceID)
}

// BindingService owns which bots may use which workspace and which binding is
// a bot's primary target.
type BindingService struct {
	bindings   dbstore.BotWorkspaceBindingStore
	workspaces dbstore.WorkspaceStore
}

func NewBindingService(bindings dbstore.BotWorkspaceBindingStore, workspaces dbstore.WorkspaceStore) *BindingService {
	return &BindingService{bindings: bindings, workspaces: workspaces}
}

func (s *BindingService) ready() error {
	if s == nil || s.bindings == nil || s.workspaces == nil {
		return errors.New("workspace binding service not configured")
	}
	return nil
}

// Bind grants the bot the workspace. With primary set, the binding also
// becomes the bot's primary target, replacing the previous one.
func (s *BindingService) Bind(ctx context.Context, botID, workspaceID string, primary bool) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	workspaceID, workspaceOK := canonicalWorkspaceUUID(workspaceID)
	if !botOK || !workspaceOK {
		return dbstore.BotWorkspaceBindingRecord{}, ErrWorkspaceNotFound
	}
	record, err := s.bindings.UpsertBotWorkspaceBinding(ctx, botID, workspaceID, false)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, ErrWorkspaceNotFound
	}
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	if !primary || record.IsPrimary {
		return record, nil
	}
	if err := s.bindings.SetPrimaryBotWorkspaceBinding(ctx, botID, record.ID); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	record.IsPrimary = true
	return record, nil
}

func (s *BindingService) Unbind(ctx context.Context, botID, bindingID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	bindingID, bindingOK := canonicalWorkspaceUUID(bindingID)
	if !botOK || !bindingOK {
		return ErrBindingNotFound
	}
	err := s.bindings.DeleteBotWorkspaceBinding(ctx, botID, bindingID)
	if errors.Is(err, db.ErrNotFound) {
		return ErrBindingNotFound
	}
	return err
}

func (s *BindingService) ListByBot(ctx context.Context, botID string) ([]dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return nil, ErrBindingNotFound
	}
	return s.bindings.ListBotWorkspaceBindingsByBot(ctx, botID)
}

// Primary returns the bot's primary binding or ErrNoPrimaryWorkspace.
func (s *BindingService) Primary(ctx context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return dbstore.BotWorkspaceBindingRecord{}, ErrNoPrimaryWorkspace
	}
	record, err := s.bindings.GetPrimaryBotWorkspaceBinding(ctx, botID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, ErrNoPrimaryWorkspace
	}
	return record, err
}

func (s *BindingService) SetPrimary(ctx context.Context, botID, bindingID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	bindingID, bindingOK := canonicalWorkspaceUUID(bindingID)
	if !botOK || !bindingOK {
		return ErrBindingNotFound
	}
	err := s.bindings.SetPrimaryBotWorkspaceBinding(ctx, botID, bindingID)
	if errors.Is(err, db.ErrNotFound) {
		return ErrBindingNotFound
	}
	return err
}

func (s *BindingService) UpdateToolApproval(ctx context.Context, botID, bindingID string, config settings.ToolApprovalConfig) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	bindingID, bindingOK := canonicalWorkspaceUUID(bindingID)
	if !botOK || !bindingOK {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	raw, err := json.Marshal(settings.NormalizeToolApprovalConfig(config))
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	record, err := s.bindings.UpdateBotWorkspaceBindingToolApproval(ctx, botID, bindingID, raw)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	return record, err
}

// ResolvePrimary returns the bot's primary binding together with the
// workspace it points at: the managed workspace when "native" is primary, a
// user workspace when a remote mount is. A bot without a primary binding
// yields ErrNoPrimaryWorkspace.
func (s *BindingService) ResolvePrimary(ctx context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, dbstore.WorkspaceRecord, error) {
	binding, err := s.Primary(ctx, botID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, dbstore.WorkspaceRecord{}, err
	}
	workspace, err := s.workspaces.GetWorkspace(ctx, binding.WorkspaceID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, dbstore.WorkspaceRecord{}, err
	}
	return binding, workspace, nil
}
