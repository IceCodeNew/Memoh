package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// WorkspaceLifecycle keeps workspace rows in step with the lifecycles that
// create computers: a bot gets a managed workspace when it is created and
// releases it when deleted; a paired runtime gets a user workspace sharing
// its id. Every method runs through the caller's transaction-scoped Queries
// so the rows commit with the bot or credential, or not at all. It satisfies
// the bots.WorkspaceBindings and userruntime.RuntimeWorkspaces ports without
// either package importing this one.
type WorkspaceLifecycle struct{}

func NewWorkspaceLifecycle() *WorkspaceLifecycle {
	return &WorkspaceLifecycle{}
}

// BindManaged creates the bot's managed workspace (a fresh id, never the bot
// id), binds it as the bot's primary target and returns the workspace id the
// caller records its intent under.
func (*WorkspaceLifecycle) BindManaged(ctx context.Context, q dbstore.Queries, botID, ownerUserID, name string) (string, error) {
	if q == nil {
		return "", errors.New("workspace lifecycle requires transaction queries")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrInvalidWorkspaceName
	}
	workspace, err := postgresstore.CreateWorkspaceWith(ctx, q, dbstore.CreateWorkspaceInput{
		OwnerUserID: ownerUserID, Kind: WorkspaceKindManaged, Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("create managed workspace: %w", err)
	}
	if _, err := q.UpsertBotWorkspaceBinding(ctx, dbsqlc.UpsertBotWorkspaceBindingParams{
		BotID: botUUID, WorkspaceID: workspace.ID, IsPrimary: true,
	}); err != nil {
		return "", fmt.Errorf("bind managed workspace: %w", err)
	}
	return workspace.ID.String(), nil
}

// ManagedWorkspaceID returns the bot's managed computer (the workspace behind
// its first managed binding) or db.ErrNoManagedWorkspaceBinding.
func (l *WorkspaceLifecycle) ManagedWorkspaceID(ctx context.Context, q dbstore.Queries, botID string) (string, error) {
	ids, err := l.ManagedWorkspaceIDs(ctx, q, botID)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", db.ErrNoManagedWorkspaceBinding
	}
	return ids[0], nil
}

// ManagedWorkspaceIDs lists the managed workspaces bound to the bot.
func (*WorkspaceLifecycle) ManagedWorkspaceIDs(ctx context.Context, q dbstore.Queries, botID string) ([]string, error) {
	if q == nil {
		return nil, errors.New("workspace lifecycle requires transaction queries")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListWorkspacesByBot(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Kind == WorkspaceKindManaged {
			ids = append(ids, row.ID.String())
		}
	}
	return ids, nil
}

// ReleaseUnbound deletes the workspace once nothing binds it any more; a
// workspace another bot still uses, or one already gone, is left alone.
func (*WorkspaceLifecycle) ReleaseUnbound(ctx context.Context, q dbstore.Queries, workspaceID string) error {
	if q == nil {
		return errors.New("workspace lifecycle requires transaction queries")
	}
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	_, err = q.DeleteWorkspaceIfUnbound(ctx, id)
	return err
}

// RuntimeCreated records the user workspace behind a freshly paired runtime.
// The workspace reuses the runtime id so remote mounts keep addressing it.
func (*WorkspaceLifecycle) RuntimeCreated(ctx context.Context, q dbstore.Queries, runtime dbstore.UserRuntimeRecord) error {
	if q == nil {
		return errors.New("workspace lifecycle requires transaction queries")
	}
	_, err := postgresstore.CreateWorkspaceWith(ctx, q, dbstore.CreateWorkspaceInput{
		ID: runtime.ID, OwnerUserID: runtime.UserID, Kind: WorkspaceKindUser, Name: runtime.Name,
	})
	if err != nil {
		return fmt.Errorf("create user workspace: %w", err)
	}
	return nil
}
