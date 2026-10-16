package postgresstore

import (
	"context"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) CreateUserRuntime(ctx context.Context, input dbstore.CreateUserRuntimeInput, attach dbstore.UserRuntimeAttach) (dbstore.UserRuntimeRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	var record dbstore.UserRuntimeRecord
	err = s.inTx(ctx, func(queries *dbsqlc.Queries) error {
		row, err := queries.CreateUserComputer(ctx, dbsqlc.CreateUserComputerParams{
			UserID: userID, Name: input.Name, ApiToken: input.APIToken,
		})
		if err != nil {
			return err
		}
		record = userRuntimeRecord(row)
		if attach == nil {
			return nil
		}
		return attach(ctx, NewQueries(queries), record)
	})
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	return record, nil
}

func (s *Store) GetUserRuntimeByAPIToken(ctx context.Context, apiToken string) (dbstore.UserRuntimeRecord, error) {
	row, err := s.queries.GetUserComputerByAPIToken(ctx, apiToken)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, mapQueryErr(err)
	}
	return userRuntimeRecord(row), nil
}

func (s *Store) ActivateUserRuntime(ctx context.Context, runtimeID, apiToken string) (dbstore.UserRuntimeRecord, error) {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return dbstore.UserRuntimeRecord{}, err
	}
	row, err := s.queries.ActivateUserComputer(ctx, dbsqlc.ActivateUserComputerParams{ID: id, ApiToken: apiToken})
	if err != nil {
		return dbstore.UserRuntimeRecord{}, mapQueryErr(err)
	}
	return userRuntimeRecord(row), nil
}

func (s *Store) ExpirePendingUserRuntimes(ctx context.Context, userID string) error {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	return mapQueryErr(s.queries.ExpirePendingUserComputers(ctx, id))
}

func (s *Store) ListUserRuntimes(ctx context.Context, userID string) ([]dbstore.UserRuntimeRecord, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserComputers(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]dbstore.UserRuntimeRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, userRuntimeRecord(row))
	}
	return items, nil
}

func (s *Store) RevokeUserRuntime(ctx context.Context, runtimeID, userID string) error {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return err
	}
	ownerID, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(queries *dbsqlc.Queries) error {
		if _, err := queries.RevokeUserComputer(ctx, dbsqlc.RevokeUserComputerParams{ID: id, UserID: ownerID}); err != nil {
			return mapQueryErr(err)
		}
		// Cascade: dead mounts of the revoked runtime must not linger as
		// ghost rows on the bot page / grants view / composer selector. The
		// runtime's user workspace row stays; only the bindings to it go.
		_, err := queries.DeleteBotWorkspaceBindingsByWorkspace(ctx, id)
		return mapQueryErr(err)
	})
}

func (s *Store) BackfillUserRuntimeName(ctx context.Context, runtimeID, userID, name, defaultName string) (bool, error) {
	id, err := db.ParseUUID(runtimeID)
	if err != nil {
		return false, err
	}
	ownerID, err := db.ParseUUID(userID)
	if err != nil {
		return false, err
	}
	rows, err := s.queries.BackfillUserComputerName(ctx, dbsqlc.BackfillUserComputerNameParams{
		ID: id, UserID: ownerID, Name: name, DefaultName: defaultName,
	})
	if err != nil {
		return false, mapQueryErr(err)
	}
	return rows > 0, nil
}

func userRuntimeRecord(row dbsqlc.UserComputer) dbstore.UserRuntimeRecord {
	return dbstore.UserRuntimeRecord{
		ID: row.ID.String(), TeamID: row.TeamID.String(), UserID: row.UserID.String(), Name: row.Name,
		APIToken: row.ApiToken, ActivatedAt: db.TimeFromPg(row.ActivatedAt),
		PendingExpiresAt: db.TimeFromPg(row.PendingExpiresAt), CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}
