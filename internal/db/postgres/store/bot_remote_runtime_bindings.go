package postgresstore

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CreateOrUpdateMount(ctx context.Context, botID, runtimeID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	parsedBotID, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	parsedRuntimeID, err := db.ParseUUID(runtimeID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	targetID, err := s.queries.CreateOrUpdateBotRemoteRuntimeMount(ctx, dbsqlc.CreateOrUpdateBotRemoteRuntimeMountParams{
		BotID: parsedBotID, RuntimeID: parsedRuntimeID,
	})
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return s.GetMount(ctx, botID, targetID.String())
}

func (s *Store) ListMounts(ctx context.Context, botID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotRemoteRuntimeMounts(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.BotRemoteRuntimeBindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, remoteMountRecord(
			row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
			row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
			row.CreatedAt, row.UpdatedAt,
		))
	}
	return records, nil
}

func (s *Store) ListGrantsByRuntimeOwner(ctx context.Context, ownerUserID string) ([]dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(ownerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotRemoteRuntimeGrantsByRuntimeOwner(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.BotRemoteRuntimeBindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, dbstore.BotRemoteRuntimeBindingRecord{
			ID:        row.ID.String(),
			BotID:     row.BotID.String(),
			RuntimeID: row.RuntimeID.String(),
			IsPrimary: row.IsPrimary,
		})
	}
	return records, nil
}

func (s *Store) GetMount(ctx context.Context, botID, targetID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	botUUID, targetUUID, err := parseRemoteMountIDs(botID, targetID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	row, err := s.queries.GetBotRemoteRuntimeMount(ctx, dbsqlc.GetBotRemoteRuntimeMountParams{BotID: botUUID, TargetID: targetUUID})
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

func (s *Store) GetPrimaryMount(ctx context.Context, botID string) (dbstore.BotRemoteRuntimeBindingRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, err
	}
	row, err := s.queries.GetPrimaryBotRemoteRuntimeMount(ctx, id)
	if err != nil {
		return dbstore.BotRemoteRuntimeBindingRecord{}, mapQueryErr(err)
	}
	return remoteMountRecord(
		row.ID, row.BotID, row.RuntimeID, row.IsPrimary, row.ToolApprovalConfig,
		row.RuntimeName, row.RuntimeUserID, row.RuntimeUnavailable, row.BotOwnerUserID,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

// SetPrimary makes targetID the bot's primary binding. "native" (or an empty
// target) means the bot's managed workspace binding: the remote primaries are
// cleared and the managed binding is marked primary in the same transaction,
// so a bot without a managed binding keeps its current primary and gets
// db.ErrNoManagedWorkspaceBinding instead of a silent no-op.
func (s *Store) SetPrimary(ctx context.Context, botID, targetID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" || strings.EqualFold(targetID, "native") {
		return s.inTx(ctx, func(queries *dbsqlc.Queries) error {
			if err := queries.ClearBotWorkspaceBindingPrimary(ctx, botUUID); err != nil {
				return mapQueryErr(err)
			}
			rows, err := queries.SetBotManagedWorkspaceBindingPrimary(ctx, botUUID)
			if err != nil {
				return mapQueryErr(err)
			}
			if rows == 0 {
				return db.ErrNoManagedWorkspaceBinding
			}
			return nil
		})
	}
	// Only a remote mount is addressable here; the managed binding is reached
	// through the "native" target above.
	if _, err := s.GetMount(ctx, botID, targetID); err != nil {
		return err
	}
	return s.SetPrimaryBotWorkspaceBinding(ctx, botID, targetID)
}

func (s *Store) UpdateToolApproval(ctx context.Context, botID, targetID string, config dbstore.JSON) error {
	_, err := s.UpdateBotWorkspaceBindingToolApproval(ctx, botID, targetID, config)
	return err
}

func (s *Store) DeleteMount(ctx context.Context, botID, targetID string) error {
	return s.DeleteBotWorkspaceBinding(ctx, botID, targetID)
}

func parseRemoteMountIDs(botID, targetID string) (pgtype.UUID, pgtype.UUID, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	targetUUID, err := db.ParseUUID(targetID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return botUUID, targetUUID, nil
}

func remoteMountRecord(
	id, botID, runtimeID pgtype.UUID,
	isPrimary bool,
	toolApproval []byte,
	runtimeName string,
	runtimeUserID pgtype.UUID,
	runtimeUnavailable pgtype.Bool,
	botOwnerUserID pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) dbstore.BotRemoteRuntimeBindingRecord {
	return dbstore.BotRemoteRuntimeBindingRecord{
		ID:             id.String(),
		BotID:          botID.String(),
		RuntimeID:      runtimeID.String(),
		IsPrimary:      isPrimary,
		ToolApproval:   append(dbstore.JSON(nil), toolApproval...),
		RuntimeName:    runtimeName,
		RuntimeUserID:  runtimeUserID.String(),
		BotOwnerUserID: botOwnerUserID.String(),
		RuntimeRevoked: runtimeUnavailable.Bool,
		CreatedAt:      db.TimeFromPg(createdAt),
		UpdatedAt:      db.TimeFromPg(updatedAt),
	}
}
