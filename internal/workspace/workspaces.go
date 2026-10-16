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

// CreateUser records a paired computer. runtimeID is the user_computers id,
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

// Get returns one of the bot's bindings or ErrBindingNotFound.
func (s *BindingService) Get(ctx context.Context, botID, bindingID string) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	bindingID, bindingOK := canonicalWorkspaceUUID(bindingID)
	if !botOK || !bindingOK {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	record, err := s.bindings.GetBotWorkspaceBinding(ctx, botID, bindingID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	return record, err
}

// BindingByWorkspace returns the bot's binding to the workspace or
// ErrBindingNotFound: it is how a stored workspace id maps back to the
// client-facing target address.
func (s *BindingService) BindingByWorkspace(ctx context.Context, botID, workspaceID string) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	workspaceID, workspaceOK := canonicalWorkspaceUUID(workspaceID)
	if !botOK || !workspaceOK {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	record, err := s.bindings.GetBotWorkspaceBindingByWorkspace(ctx, botID, workspaceID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, ErrBindingNotFound
	}
	return record, err
}

// Workspace returns a workspace record or ErrWorkspaceNotFound.
func (s *BindingService) Workspace(ctx context.Context, workspaceID string) (dbstore.WorkspaceRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.WorkspaceRecord{}, err
	}
	workspaceID, ok := canonicalWorkspaceUUID(workspaceID)
	if !ok {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	record, err := s.workspaces.GetWorkspace(ctx, workspaceID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.WorkspaceRecord{}, ErrWorkspaceNotFound
	}
	return record, err
}

// ManagedBinding returns the binding behind the bot's managed computer (its
// first managed binding), which carries the native target's tool approval
// policy. db.ErrNoManagedWorkspaceBinding when the bot has none.
func (s *BindingService) ManagedBinding(ctx context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, error) {
	if err := s.ready(); err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return dbstore.BotWorkspaceBindingRecord{}, db.ErrNoManagedWorkspaceBinding
	}
	record, err := s.bindings.GetBotManagedWorkspaceBinding(ctx, botID)
	if errors.Is(err, db.ErrNotFound) {
		return dbstore.BotWorkspaceBindingRecord{}, db.ErrNoManagedWorkspaceBinding
	}
	return record, err
}

// UpdateManagedToolApproval stores the native target's tool approval policy
// on the bot's managed binding. It is the settings.ManagedWorkspaceApprovalWriter
// port: the Bot settings and the native workspace-target endpoint both write
// the policy here and to bots.tool_approval_config.
func (s *BindingService) UpdateManagedToolApproval(ctx context.Context, botID string, config settings.ToolApprovalConfig) error {
	binding, err := s.ManagedBinding(ctx, botID)
	if err != nil {
		return err
	}
	_, err = s.UpdateToolApproval(ctx, botID, binding.ID, config)
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

// ManagedWorkspaceID returns the bot's managed computer: the workspace behind
// its first managed binding. Every bot -> computer mapping goes through the
// bindings; a bot created after 0167 has a workspace id that is not the bot
// id. db.ErrNoManagedWorkspaceBinding when the bot has none.
func (s *BindingService) ManagedWorkspaceID(ctx context.Context, botID string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	botID, ok := canonicalWorkspaceUUID(botID)
	if !ok {
		return "", db.ErrNoManagedWorkspaceBinding
	}
	records, err := s.workspaces.ListWorkspacesByBot(ctx, botID)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Kind == WorkspaceKindManaged {
			return record.ID, nil
		}
	}
	return "", db.ErrNoManagedWorkspaceBinding
}

// BoundBots lists the bots bound to the workspace, in binding order. It is
// the botworkspace.BoundBots port: the reconciler derives bots.status for
// every bot bound to a managed computer.
func (s *BindingService) BoundBots(ctx context.Context, workspaceID string) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	workspaceID, ok := canonicalWorkspaceUUID(workspaceID)
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	bindings, err := s.bindings.ListBotWorkspaceBindingsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	bots := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		bots = append(bots, binding.BotID)
	}
	return bots, nil
}

// EnsureManaged returns the bot's managed computer, creating and binding one
// when the bot has none: bots that predate workspace records and never had a
// workspace slot get theirs on the first workspace create. The new binding is
// primary only when the bot has no primary target yet.
func (s *BindingService) EnsureManaged(ctx context.Context, botID, ownerUserID, name string) (string, error) {
	workspaceID, err := s.ManagedWorkspaceID(ctx, botID)
	if err == nil {
		return workspaceID, nil
	}
	if !errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
		return "", err
	}
	botID, botOK := canonicalWorkspaceUUID(botID)
	ownerUserID, ownerOK := canonicalWorkspaceUUID(ownerUserID)
	if !botOK || !ownerOK {
		return "", ErrWorkspaceNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrInvalidWorkspaceName
	}
	workspace, err := s.workspaces.CreateWorkspace(ctx, dbstore.CreateWorkspaceInput{
		OwnerUserID: ownerUserID, Kind: WorkspaceKindManaged, Name: name,
	})
	if err != nil {
		return "", err
	}
	_, primaryErr := s.Primary(ctx, botID)
	primary := errors.Is(primaryErr, ErrNoPrimaryWorkspace)
	if primaryErr != nil && !primary {
		return "", primaryErr
	}
	if _, err := s.Bind(ctx, botID, workspace.ID, primary); err != nil {
		return "", err
	}
	return workspace.ID, nil
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
