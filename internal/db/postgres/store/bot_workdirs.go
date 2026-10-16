package postgresstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CreateWorkdir(ctx context.Context, input dbstore.CreateBotWorkdirInput) (dbstore.BotWorkdirRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	workspaceID, err := db.ParseUUID(input.WorkspaceID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	id, err := s.queries.CreateBotWorkdir(ctx, dbsqlc.CreateBotWorkdirParams{
		BotID:           botID,
		Name:            input.Name,
		WorkspaceID:     workspaceID,
		Path:            input.Path,
		CreatedByUserID: db.ParseUUIDOrEmpty(input.CreatedByUserID),
	})
	if err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	// The insert returns the bare row; the record carries the workspace kind
	// and binding id the service derives the target address from.
	return s.GetWorkdir(ctx, input.BotID, id.String())
}

func (s *Store) ListWorkdirs(ctx context.Context, botID string, includeArchived bool) ([]dbstore.BotWorkdirRecord, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotWorkdirs(ctx, dbsqlc.ListBotWorkdirsParams{
		BotID:           botUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, err
	}
	items := make([]dbstore.BotWorkdirRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, botWorkdirRecord(dbsqlc.GetBotWorkdirRow(row)))
	}
	return items, nil
}

func (s *Store) GetWorkdir(ctx context.Context, botID, workdirID string) (dbstore.BotWorkdirRecord, error) {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	row, err := s.queries.GetBotWorkdir(ctx, dbsqlc.GetBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
	})
	if err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	return botWorkdirRecord(row), nil
}

func (s *Store) RenameWorkdir(ctx context.Context, botID, workdirID, name string) (dbstore.BotWorkdirRecord, error) {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return dbstore.BotWorkdirRecord{}, err
	}
	if _, err := s.queries.RenameBotWorkdir(ctx, dbsqlc.RenameBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
		Name:      name,
	}); err != nil {
		return dbstore.BotWorkdirRecord{}, mapQueryErr(err)
	}
	return s.GetWorkdir(ctx, botID, workdirID)
}

func (s *Store) ArchiveWorkdir(ctx context.Context, botID, workdirID string) error {
	botUUID, workdirUUID, err := parseBotWorkdirIDs(botID, workdirID)
	if err != nil {
		return err
	}
	_, err = s.queries.ArchiveBotWorkdir(ctx, dbsqlc.ArchiveBotWorkdirParams{
		BotID:     botUUID,
		WorkdirID: workdirUUID,
	})
	return mapQueryErr(err)
}

func parseBotWorkdirIDs(botID, workdirID string) (botUUID, workdirUUID pgtype.UUID, err error) {
	botUUID, err = db.ParseUUID(botID)
	if err != nil {
		return botUUID, workdirUUID, err
	}
	workdirUUID, err = db.ParseUUID(workdirID)
	return botUUID, workdirUUID, err
}

func botWorkdirRecord(row dbsqlc.GetBotWorkdirRow) dbstore.BotWorkdirRecord {
	record := dbstore.BotWorkdirRecord{
		ID:            row.ID.String(),
		BotID:         row.BotID.String(),
		Name:          row.Name,
		WorkspaceID:   row.WorkspaceID.String(),
		WorkspaceKind: row.WorkspaceKind,
		Path:          row.Path,
		ArchivedAt:    db.TimeFromPg(row.ArchivedAt),
		CreatedAt:     db.TimeFromPg(row.CreatedAt),
		UpdatedAt:     db.TimeFromPg(row.UpdatedAt),
	}
	if row.BindingID.Valid {
		record.BindingID = row.BindingID.String()
	}
	if row.CreatedByUserID.Valid {
		record.CreatedByUserID = row.CreatedByUserID.String()
	}
	return record
}
