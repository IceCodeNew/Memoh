package postgresstore

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// The Store is the ManagedComputerStore of the workspace manager.
var _ dbstore.ManagedComputerStore = (*Store)(nil)

func (s *Store) GetManagedComputer(ctx context.Context, workspaceID string) (dbstore.ManagedComputerRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return dbstore.ManagedComputerRecord{}, err
	}
	row, err := s.queries.GetManagedComputer(ctx, id)
	if err != nil {
		return dbstore.ManagedComputerRecord{}, mapQueryErr(err)
	}
	return managedComputerRecord(row), nil
}

func (s *Store) GetManagedComputerByInstanceID(ctx context.Context, instanceID string) (dbstore.ManagedComputerRecord, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return dbstore.ManagedComputerRecord{}, db.ErrNotFound
	}
	row, err := s.queries.GetManagedComputerByInstanceID(ctx, instanceID)
	if err != nil {
		return dbstore.ManagedComputerRecord{}, mapQueryErr(err)
	}
	return managedComputerRecord(row), nil
}

func (s *Store) ListAutoStartManagedComputers(ctx context.Context) ([]dbstore.ManagedComputerRecord, error) {
	rows, err := s.queries.ListAutoStartManagedComputers(ctx)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.ManagedComputerRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, managedComputerRecord(row))
	}
	return records, nil
}

func (s *Store) UpsertManagedComputerInstance(ctx context.Context, input dbstore.ManagedComputerInstanceInput) error {
	id, err := db.ParseUUID(input.WorkspaceID)
	if err != nil {
		return err
	}
	namespace := strings.TrimSpace(input.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	return mapQueryErr(s.queries.UpsertManagedComputerInstance(ctx, dbsqlc.UpsertManagedComputerInstanceParams{
		WorkspaceID:    id,
		Image:          input.Image,
		InstanceStatus: input.Status,
		InstanceID:     input.InstanceID,
		InstanceName:   input.InstanceName,
		Namespace:      namespace,
		AutoStart:      input.AutoStart,
		ContainerPath:  input.ContainerPath,
	}))
}

func (s *Store) ClearManagedComputerInstance(ctx context.Context, workspaceID string) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	// Versions RESTRICT the snapshots they point at, so they go first.
	return s.inTx(ctx, func(queries *dbsqlc.Queries) error {
		if err := queries.DeleteManagedComputerVersions(ctx, id); err != nil {
			return mapQueryErr(err)
		}
		if err := queries.DeleteManagedComputerSnapshots(ctx, id); err != nil {
			return mapQueryErr(err)
		}
		if err := queries.DeleteManagedComputerEvents(ctx, id); err != nil {
			return mapQueryErr(err)
		}
		return mapQueryErr(queries.ClearManagedComputerInstance(ctx, id))
	})
}

func (s *Store) MarkManagedComputerStarted(ctx context.Context, workspaceID string) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	return mapQueryErr(s.queries.MarkManagedComputerStarted(ctx, id))
}

func (s *Store) MarkManagedComputerStopped(ctx context.Context, workspaceID string) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	return mapQueryErr(s.queries.MarkManagedComputerStopped(ctx, id))
}

func (s *Store) UpdateManagedComputerInstanceStatus(ctx context.Context, workspaceID, status string) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	return mapQueryErr(s.queries.UpdateManagedComputerInstanceStatus(ctx, dbsqlc.UpdateManagedComputerInstanceStatusParams{
		InstanceStatus: status,
		WorkspaceID:    id,
	}))
}

func (s *Store) ListManagedComputerSnapshots(ctx context.Context, workspaceID string) ([]dbstore.ManagedComputerSnapshotRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListManagedComputerSnapshotsWithVersion(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.ManagedComputerSnapshotRecord, 0, len(rows))
	for _, row := range rows {
		record := dbstore.ManagedComputerSnapshotRecord{
			ID:                        row.ID.String(),
			WorkspaceID:               row.WorkspaceID.String(),
			ContainerID:               row.ContainerID,
			RuntimeSnapshotName:       row.RuntimeSnapshotName,
			DisplayName:               row.DisplayName.String,
			ParentRuntimeSnapshotName: row.ParentRuntimeSnapshotName.String,
			Snapshotter:               row.Snapshotter,
			Source:                    row.Source,
			CreatedAt:                 db.TimeFromPg(row.CreatedAt),
		}
		if row.Version.Valid {
			version := int(row.Version.Int32)
			record.Version = &version
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) ListManagedComputerVersions(ctx context.Context, workspaceID string) ([]dbstore.ManagedComputerVersionRecord, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListManagedComputerVersions(ctx, id)
	if err != nil {
		return nil, mapQueryErr(err)
	}
	records := make([]dbstore.ManagedComputerVersionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, dbstore.ManagedComputerVersionRecord{
			ID:                  row.ID.String(),
			WorkspaceID:         row.WorkspaceID.String(),
			ContainerID:         row.ContainerID,
			SnapshotID:          row.SnapshotID.String(),
			Version:             int(row.Version),
			RuntimeSnapshotName: row.RuntimeSnapshotName,
			DisplayName:         row.DisplayName.String,
			CreatedAt:           db.TimeFromPg(row.CreatedAt),
		})
	}
	return records, nil
}

func (s *Store) GetManagedComputerVersionSnapshotName(ctx context.Context, workspaceID string, version int) (string, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return "", err
	}
	name, err := s.queries.GetManagedComputerVersionSnapshotRuntimeName(ctx, dbsqlc.GetManagedComputerVersionSnapshotRuntimeNameParams{
		WorkspaceID: id,
		Version:     int32(version), //nolint:gosec // callers validate the range
	})
	if err != nil {
		return "", mapQueryErr(err)
	}
	return name, nil
}

func (s *Store) RecordManagedComputerVersion(ctx context.Context, input dbstore.RecordManagedComputerVersionInput) (dbstore.ManagedComputerVersionRecord, error) {
	id, err := db.ParseUUID(input.WorkspaceID)
	if err != nil {
		return dbstore.ManagedComputerVersionRecord{}, err
	}
	displayName := strings.TrimSpace(input.DisplayName)
	parent := strings.TrimSpace(input.ParentRuntimeSnapshotName)
	var record dbstore.ManagedComputerVersionRecord
	err = s.inTx(ctx, func(queries *dbsqlc.Queries) error {
		snapshot, err := queries.UpsertManagedComputerSnapshot(ctx, dbsqlc.UpsertManagedComputerSnapshotParams{
			WorkspaceID:               id,
			ContainerID:               input.ContainerID,
			RuntimeSnapshotName:       input.RuntimeSnapshotName,
			DisplayName:               pgtype.Text{String: displayName, Valid: displayName != ""},
			ParentRuntimeSnapshotName: pgtype.Text{String: parent, Valid: parent != ""},
			Snapshotter:               input.Snapshotter,
			Source:                    input.Source,
		})
		if err != nil {
			return mapQueryErr(err)
		}
		version, err := queries.NextManagedComputerVersion(ctx, id)
		if err != nil {
			return mapQueryErr(err)
		}
		row, err := queries.InsertManagedComputerVersion(ctx, dbsqlc.InsertManagedComputerVersionParams{
			WorkspaceID: id,
			ContainerID: input.ContainerID,
			SnapshotID:  snapshot.ID,
			Version:     version,
		})
		if err != nil {
			return mapQueryErr(err)
		}
		record = dbstore.ManagedComputerVersionRecord{
			ID:                  row.ID.String(),
			WorkspaceID:         row.WorkspaceID.String(),
			ContainerID:         row.ContainerID,
			SnapshotID:          row.SnapshotID.String(),
			Version:             int(row.Version),
			RuntimeSnapshotName: snapshot.RuntimeSnapshotName,
			DisplayName:         snapshot.DisplayName.String,
			CreatedAt:           db.TimeFromPg(row.CreatedAt),
		}
		return nil
	})
	if err != nil {
		return dbstore.ManagedComputerVersionRecord{}, err
	}
	return record, nil
}

func (s *Store) InsertManagedComputerEvent(ctx context.Context, input dbstore.ManagedComputerEventInput) error {
	id, err := db.ParseUUID(input.WorkspaceID)
	if err != nil {
		return err
	}
	payload := input.Payload
	if len(payload) == 0 {
		payload = dbstore.JSON(`{}`)
	}
	return mapQueryErr(s.queries.InsertManagedComputerEvent(ctx, dbsqlc.InsertManagedComputerEventParams{
		ID:          input.ID,
		WorkspaceID: id,
		ContainerID: input.ContainerID,
		EventType:   input.EventType,
		Payload:     payload,
	}))
}

func (s *Store) GetManagedComputerResourceLimits(ctx context.Context, workspaceID string) (dbstore.ManagedComputerResourceLimits, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return dbstore.ManagedComputerResourceLimits{}, err
	}
	row, err := s.queries.GetManagedComputerResourceLimits(ctx, id)
	if err != nil {
		return dbstore.ManagedComputerResourceLimits{}, mapQueryErr(err)
	}
	return dbstore.ManagedComputerResourceLimits{
		CPUMillicores: row.CpuMillicores,
		MemoryBytes:   row.MemoryBytes,
		StorageBytes:  row.StorageBytes,
	}, nil
}

func (s *Store) UpsertManagedComputerResourceLimits(ctx context.Context, workspaceID string, limits dbstore.ManagedComputerResourceLimits) error {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpsertManagedComputerResourceLimits(ctx, dbsqlc.UpsertManagedComputerResourceLimitsParams{
		WorkspaceID:   id,
		CpuMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	})
	return mapQueryErr(err)
}

func managedComputerRecord(row dbsqlc.ManagedComputer) dbstore.ManagedComputerRecord {
	return dbstore.ManagedComputerRecord{
		WorkspaceID:    row.WorkspaceID.String(),
		TeamID:         row.TeamID.String(),
		Image:          row.Image,
		InstanceID:     row.InstanceID,
		InstanceName:   row.InstanceName,
		InstanceStatus: row.InstanceStatus,
		Namespace:      row.Namespace,
		AutoStart:      row.AutoStart,
		ContainerPath:  row.ContainerPath,
		LastStartedAt:  db.TimeFromPg(row.LastStartedAt),
		LastStoppedAt:  db.TimeFromPg(row.LastStoppedAt),
		CreatedAt:      db.TimeFromPg(row.CreatedAt),
		UpdatedAt:      db.TimeFromPg(row.UpdatedAt),
	}
}
