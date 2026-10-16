package postgresstore

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// SQLSTATEs raised when a referenced row is deleted: 23503 for NO ACTION
// (checked at the end of the transaction for the binding -> workspace key)
// and 23001 for RESTRICT.
const (
	pgForeignKeyViolation = "23503"
	pgRestrictViolation   = "23001"
)

// ErrWorkspaceBound is returned by DeleteWorkspace while a binding still
// references the workspace.
var ErrWorkspaceBound = errors.New("workspace is still bound to a bot")

func (s *Store) CreateWorkspace(ctx context.Context, input dbstore.CreateWorkspaceInput) (dbstore.WorkspaceRecord, error) {
	row, err := CreateWorkspaceWith(ctx, NewQueries(s.queries), input)
	if err != nil {
		return dbstore.WorkspaceRecord{}, err
	}
	return WorkspaceRecord(row), nil
}

// CreateWorkspaceWith inserts a workspace through q, which may be a
// transaction's Queries so the row commits with its owner's other writes.
func CreateWorkspaceWith(ctx context.Context, q dbstore.Queries, input dbstore.CreateWorkspaceInput) (dbsqlc.Workspace, error) {
	ownerID, err := db.ParseUUID(input.OwnerUserID)
	if err != nil {
		return dbsqlc.Workspace{}, err
	}
	var id pgtype.UUID
	if strings.TrimSpace(input.ID) != "" {
		if id, err = db.ParseUUID(input.ID); err != nil {
			return dbsqlc.Workspace{}, err
		}
	}
	metadata := input.Metadata
	if len(metadata) == 0 {
		metadata = dbstore.JSON(`{}`)
	}
	row, err := q.CreateWorkspace(ctx, dbsqlc.CreateWorkspaceParams{
		ID: id, OwnerUserID: ownerID, Kind: input.Kind, Name: input.Name, Metadata: metadata,
	})
	if err != nil {
		return dbsqlc.Workspace{}, mapQueryErr(err)
	}
	return row, nil
}

func (s *Store) GetWorkspace(ctx context.Context, workspaceID string) (dbstore.WorkspaceRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return dbstore.WorkspaceRecord{}, err
	}
	row, err := s.queries.GetWorkspace(ctx, id)
	if err != nil {
		return dbstore.WorkspaceRecord{}, mapQueryErr(err)
	}
	return WorkspaceRecord(row), nil
}

func (s *Store) ListWorkspacesByOwner(ctx context.Context, ownerUserID string) ([]dbstore.WorkspaceRecord, error) {
	id, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListWorkspacesByOwner(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	return workspaceRecords(rows), nil
}

func (s *Store) ListWorkspacesByBot(ctx context.Context, botID string) ([]dbstore.WorkspaceRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListWorkspacesByBot(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	return workspaceRecords(rows), nil
}

func (s *Store) UpdateWorkspaceName(ctx context.Context, workspaceID, name string) (dbstore.WorkspaceRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return dbstore.WorkspaceRecord{}, err
	}
	row, err := s.queries.UpdateWorkspaceName(ctx, dbsqlc.UpdateWorkspaceNameParams{ID: id, Name: name})
	if err != nil {
		return dbstore.WorkspaceRecord{}, mapQueryErr(err)
	}
	return WorkspaceRecord(row), nil
}

func (s *Store) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	rows, err := s.queries.DeleteWorkspace(ctx, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == pgForeignKeyViolation || pgErr.Code == pgRestrictViolation) {
			return ErrWorkspaceBound
		}
		return mapQueryErr(err)
	}
	if rows == 0 {
		return db.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteWorkspaceIfUnbound(ctx context.Context, workspaceID string) (bool, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return false, err
	}
	rows, err := s.queries.DeleteWorkspaceIfUnbound(ctx, id)
	if err != nil {
		return false, mapQueryErr(err)
	}
	return rows > 0, nil
}

func (s *Store) UpsertBotWorkspaceBinding(ctx context.Context, botID, workspaceID string, primary bool) (dbstore.BotWorkspaceBindingRecord, error) {
	botUUID, workspaceUUID, err := parseRemoteMountIDs(botID, workspaceID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.UpsertBotWorkspaceBinding(ctx, dbsqlc.UpsertBotWorkspaceBindingParams{
		BotID: botUUID, WorkspaceID: workspaceUUID, IsPrimary: primary,
	})
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) GetBotWorkspaceBinding(ctx context.Context, botID, bindingID string) (dbstore.BotWorkspaceBindingRecord, error) {
	botUUID, bindingUUID, err := parseRemoteMountIDs(botID, bindingID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.GetBotWorkspaceBinding(ctx, dbsqlc.GetBotWorkspaceBindingParams{BotID: botUUID, BindingID: bindingUUID})
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) ListBotWorkspaceBindingsByBot(ctx context.Context, botID string) ([]dbstore.BotWorkspaceBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotWorkspaceBindingsByBot(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	return bindingRecords(rows), nil
}

func (s *Store) ListBotWorkspaceBindingsByWorkspace(ctx context.Context, workspaceID string) ([]dbstore.BotWorkspaceBindingRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotWorkspaceBindingsByWorkspace(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	return bindingRecords(rows), nil
}

func (s *Store) GetPrimaryBotWorkspaceBinding(ctx context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.GetPrimaryBotWorkspaceBinding(ctx, id)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) SetPrimaryBotWorkspaceBinding(ctx context.Context, botID, bindingID string) error {
	botUUID, bindingUUID, err := parseRemoteMountIDs(botID, bindingID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(queries *dbsqlc.Queries) error {
		if err := queries.ClearBotWorkspaceBindingPrimary(ctx, botUUID); err != nil {
			return mapQueryErr(err)
		}
		rows, err := queries.SetBotWorkspaceBindingPrimary(ctx, dbsqlc.SetBotWorkspaceBindingPrimaryParams{
			BotID: botUUID, BindingID: bindingUUID,
		})
		if err != nil {
			return mapQueryErr(err)
		}
		if rows == 0 {
			return db.ErrNotFound
		}
		return nil
	})
}

func (s *Store) UpdateBotWorkspaceBindingToolApproval(ctx context.Context, botID, bindingID string, config dbstore.JSON) (dbstore.BotWorkspaceBindingRecord, error) {
	botUUID, bindingUUID, err := parseRemoteMountIDs(botID, bindingID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.UpdateBotWorkspaceBindingToolApproval(ctx, dbsqlc.UpdateBotWorkspaceBindingToolApprovalParams{
		BotID: botUUID, BindingID: bindingUUID, ToolApprovalConfig: config,
	})
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) GetBotWorkspaceBindingByWorkspace(ctx context.Context, botID, workspaceID string) (dbstore.BotWorkspaceBindingRecord, error) {
	botUUID, workspaceUUID, err := parseRemoteMountIDs(botID, workspaceID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.GetBotWorkspaceBindingByWorkspace(ctx, dbsqlc.GetBotWorkspaceBindingByWorkspaceParams{BotID: botUUID, WorkspaceID: workspaceUUID})
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) GetBotManagedWorkspaceBinding(ctx context.Context, botID string) (dbstore.BotWorkspaceBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, err
	}
	row, err := s.queries.GetBotManagedWorkspaceBinding(ctx, id)
	if err != nil {
		return dbstore.BotWorkspaceBindingRecord{}, mapQueryErr(err)
	}
	return BotWorkspaceBindingRecord(row), nil
}

func (s *Store) DeleteBotWorkspaceBinding(ctx context.Context, botID, bindingID string) error {
	botUUID, bindingUUID, err := parseRemoteMountIDs(botID, bindingID)
	if err != nil {
		return err
	}
	rows, err := s.queries.DeleteBotWorkspaceBinding(ctx, dbsqlc.DeleteBotWorkspaceBindingParams{BotID: botUUID, BindingID: bindingUUID})
	if err != nil {
		return mapQueryErr(err)
	}
	if rows == 0 {
		return db.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteBotWorkspaceBindingsByWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return 0, err
	}
	rows, err := s.queries.DeleteBotWorkspaceBindingsByWorkspace(ctx, id)
	if err != nil {
		return 0, mapQueryErr(err)
	}
	return rows, nil
}

// inTx runs fn in one transaction. Stores built with NewWithQueries (focused
// store tests) have no pool and run fn directly, like the other stores here.
func (s *Store) inTx(ctx context.Context, fn func(queries *dbsqlc.Queries) error) error {
	if s.pool == nil {
		return fn(s.queries)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WorkspaceRecord converts a sqlc row; exported so code holding a
// transaction's Queries can map rows without duplicating the conversion.
func WorkspaceRecord(row dbsqlc.Workspace) dbstore.WorkspaceRecord {
	return dbstore.WorkspaceRecord{
		ID:          row.ID.String(),
		TeamID:      row.TeamID.String(),
		OwnerUserID: row.OwnerUserID.String(),
		Kind:        row.Kind,
		Name:        row.Name,
		Metadata:    append(dbstore.JSON(nil), row.Metadata...),
		CreatedAt:   db.TimeFromPg(row.CreatedAt),
		UpdatedAt:   db.TimeFromPg(row.UpdatedAt),
	}
}

func workspaceRecords(rows []dbsqlc.Workspace) []dbstore.WorkspaceRecord {
	records := make([]dbstore.WorkspaceRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, WorkspaceRecord(row))
	}
	return records
}

// BotWorkspaceBindingRecord converts a sqlc row; see WorkspaceRecord.
func BotWorkspaceBindingRecord(row dbsqlc.BotWorkspaceBinding) dbstore.BotWorkspaceBindingRecord {
	return dbstore.BotWorkspaceBindingRecord{
		ID:           row.ID.String(),
		TeamID:       row.TeamID.String(),
		BotID:        row.BotID.String(),
		WorkspaceID:  row.WorkspaceID.String(),
		IsPrimary:    row.IsPrimary,
		ToolApproval: append(dbstore.JSON(nil), row.ToolApprovalConfig...),
		CreatedAt:    db.TimeFromPg(row.CreatedAt),
		UpdatedAt:    db.TimeFromPg(row.UpdatedAt),
	}
}

func bindingRecords(rows []dbsqlc.BotWorkspaceBinding) []dbstore.BotWorkspaceBindingRecord {
	records := make([]dbstore.BotWorkspaceBindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, BotWorkspaceBindingRecord(row))
	}
	return records
}
