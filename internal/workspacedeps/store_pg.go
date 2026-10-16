package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Team context for the PostgreSQL store.
//
// Row level security on managed_computer_dependencies resolves the team with
// public.memoh_current_team_id(), which reads the memoh.team_id GUC. Memoh
// binds that GUC per connection, not per request: db.OpenPostgres (and
// db.OpenPostgresDSN for tests) installs db.SetDefaultTeamOnConnect as the
// pgxpool AfterConnect hook, so every pooled connection carries the
// singleton team id at the session level. Nothing is read from the Go
// context.Context, and no HTTP middleware switches teams per request.
//
// A background worker (stale reaper, update checker) therefore needs no
// team-aware context: any context — including context.Background() with a
// deadline — behaves exactly like a request handler, provided the
// dbstore.Queries it holds is the FX-injected one backed by the shared pool
// (postgresstore.Queries over db.Open). Never build a private pool without
// the AfterConnect hook: memoh_current_team_id() then raises and every query
// fails closed.

type postgresStore struct {
	q dbstore.Queries
}

// NewPostgresStore returns a Store backed by the sqlc queries in q. The
// caller's context only carries cancellation; team scoping comes from the
// connection (see the package note above).
func NewPostgresStore(q dbstore.Queries) Store {
	return &postgresStore{q: q}
}

func (s *postgresStore) Get(ctx context.Context, key InstallationKey) (Installation, error) {
	workspaceID, err := parseWorkspaceID(key.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetManagedComputerDependency(ctx, dbsqlc.GetManagedComputerDependencyParams{
		WorkspaceID: workspaceID,

		DependencyID: key.DependencyID,
	})
	return installationResult(row, err)
}

func (s *postgresStore) ListForWorkspace(ctx context.Context, workspaceID string) ([]Installation, error) {
	workspaceUUID, err := parseWorkspaceID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListManagedComputerDependencies(ctx, workspaceUUID)
	return installationsResult(rows, err)
}

func (s *postgresStore) ListByStatus(ctx context.Context, status Status) ([]Installation, error) {
	rows, err := s.q.ListManagedComputerDependenciesByStatus(ctx, string(status))
	return installationsResult(rows, err)
}

func (s *postgresStore) ListStaleOperations(ctx context.Context, olderThan time.Duration) ([]Installation, error) {
	rows, err := s.q.ListStaleManagedComputerDependencyOperations(ctx, staleSeconds(olderThan))
	return installationsResult(rows, err)
}

func (s *postgresStore) Upsert(ctx context.Context, in UpsertInstallation) (Installation, error) {
	workspaceID, err := parseWorkspaceID(in.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpsertManagedComputerDependencyIntent(ctx, dbsqlc.UpsertManagedComputerDependencyIntentParams{
		WorkspaceID: workspaceID,

		DependencyID:     in.DependencyID,
		Source:           in.Source,
		Status:           string(in.Status),
		InstalledVersion: in.InstalledVersion,
		ManifestDigest:   in.ManifestDigest,
		SourceUrl:        in.SourceURL, RegistryID: in.RegistryID, DefinitionRevision: in.DefinitionRevision,
	})
	return operationResult(row, err)
}

// ClaimOperation establishes ownership in PostgreSQL before a Server runs a
// script. Existing installation facts survive the claim for failure recovery.
func (s *postgresStore) ClaimOperation(ctx context.Context, in UpsertInstallation, operationID string) (Installation, error) {
	if !in.Status.InProgress() || !validReceiptID(operationID) {
		return Installation{}, errors.New("workspace dependency store: invalid operation claim")
	}
	workspaceID, err := parseWorkspaceID(in.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.ClaimManagedComputerDependencyOperation(ctx, dbsqlc.ClaimManagedComputerDependencyOperationParams{
		WorkspaceID: workspaceID, DependencyID: in.DependencyID,
		Source: in.Source, Status: string(in.Status), InstalledVersion: in.InstalledVersion,
		ManifestDigest: in.ManifestDigest, SourceUrl: in.SourceURL, RegistryID: in.RegistryID,
		DefinitionRevision: in.DefinitionRevision, OperationID: operationID,
	})
	return operationResult(row, err)
}

// FinishOperation is the only terminal write for a claimed operation. A stale
// receipt or delayed Server cannot update or remove a newer operation's row.
func (s *postgresStore) FinishOperation(ctx context.Context, key InstallationKey, operationID string, terminal *Installation) (Installation, error) {
	if !validReceiptID(operationID) || (terminal != nil && terminal.Status.InProgress()) {
		return Installation{}, errors.New("workspace dependency store: invalid operation finish")
	}
	workspaceID, err := parseWorkspaceID(key.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	if terminal == nil {
		row, err := s.q.DeleteManagedComputerDependencyOperation(ctx, dbsqlc.DeleteManagedComputerDependencyOperationParams{
			WorkspaceID: workspaceID, DependencyID: key.DependencyID, OperationID: operationID,
		})
		return operationResult(row, err)
	}
	row, err := s.q.FinishManagedComputerDependencyOperation(ctx, dbsqlc.FinishManagedComputerDependencyOperationParams{
		WorkspaceID: workspaceID, DependencyID: key.DependencyID, OperationID: operationID,
		Source: terminal.Source, Status: string(terminal.Status), InstalledVersion: terminal.InstalledVersion,
		LatestVersion: terminal.LatestVersion, LastCheckedAt: nullableTimestamptz(terminal.LastCheckedAt), LastErrorCode: terminal.LastErrorCode,
		ManifestDigest: terminal.ManifestDigest, SourceUrl: terminal.SourceURL, RegistryID: terminal.RegistryID,
		DefinitionRevision: terminal.DefinitionRevision,
	})
	return operationResult(row, err)
}

func operationResult(row dbsqlc.ManagedComputerDependency, err error) (Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, ErrBusy
	}
	return installationResult(row, err)
}

func (s *postgresStore) SetStatus(ctx context.Context, key InstallationKey, status Status, lastErrorCode string) (Installation, error) {
	workspaceID, err := parseWorkspaceID(key.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateManagedComputerDependencyStatus(ctx, dbsqlc.UpdateManagedComputerDependencyStatusParams{
		Status:        string(status),
		LastErrorCode: lastErrorCode,
		WorkspaceID:   workspaceID,

		DependencyID: key.DependencyID,
	})
	return operationResult(row, err)
}

func (s *postgresStore) UpdateObserved(ctx context.Context, key InstallationKey, upd ObservedUpdate) (Installation, error) {
	workspaceID, err := parseWorkspaceID(key.WorkspaceID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateManagedComputerDependencyObserved(ctx, observedParams(workspaceID, key, upd))
	return operationResult(row, err)
}

func (s *postgresStore) Delete(ctx context.Context, key InstallationKey) error {
	workspaceID, err := parseWorkspaceID(key.WorkspaceID)
	if err != nil {
		return err
	}
	affected, err := s.q.DeleteManagedComputerDependency(ctx, dbsqlc.DeleteManagedComputerDependencyParams{
		WorkspaceID: workspaceID,

		DependencyID: key.DependencyID,
	})
	if err != nil {
		return fmt.Errorf("workspace dependency store: %w", err)
	}
	if affected == 0 {
		return ErrBusy
	}
	return nil
}

// observedParams maps an ObservedUpdate onto the COALESCE-guarded update: a
// nil field becomes an invalid (NULL) parameter, which leaves the column as
// it is; a non-nil pointer — including a pointer to "" — writes the value.
func observedParams(workspaceID pgtype.UUID, key InstallationKey, upd ObservedUpdate) dbsqlc.UpdateManagedComputerDependencyObservedParams {
	return dbsqlc.UpdateManagedComputerDependencyObservedParams{
		Source:           nullableText(upd.Source),
		InstalledVersion: nullableText(upd.InstalledVersion),
		LatestVersion:    nullableText(upd.LatestVersion),
		LastCheckedAt:    nullableTimestamptz(upd.LastCheckedAt),
		LastError:        nullableText(upd.LastError),
		LastErrorCode:    nullableText(upd.LastErrorCode),
		ManifestDigest:   nullableText(upd.ManifestDigest),
		SourceUrl:        nullableText(upd.SourceURL), RegistryID: nullableText(upd.RegistryID), DefinitionRevision: nullableText(upd.DefinitionRevision),
		WorkspaceID: workspaceID,

		DependencyID: key.DependencyID,
	}
}

// staleSeconds converts the reaper threshold to the query's seconds argument;
// a non-positive duration selects every in-progress record.
func staleSeconds(olderThan time.Duration) float64 {
	if olderThan <= 0 {
		return 0
	}
	return olderThan.Seconds()
}

func parseWorkspaceID(workspaceID string) (pgtype.UUID, error) {
	id, err := db.ParseUUID(workspaceID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("workspace dependency store: workspace id: %w", err)
	}
	return id, nil
}

func installationResult(row dbsqlc.ManagedComputerDependency, err error) (Installation, error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, ErrInstallationNotFound
		}
		return Installation{}, fmt.Errorf("workspace dependency store: %w", err)
	}
	return installationFromRow(row), nil
}

func installationsResult(rows []dbsqlc.ManagedComputerDependency, err error) ([]Installation, error) {
	if err != nil {
		return nil, fmt.Errorf("workspace dependency store: %w", err)
	}
	out := make([]Installation, 0, len(rows))
	for _, row := range rows {
		out = append(out, installationFromRow(row))
	}
	return out, nil
}

func installationFromRow(row dbsqlc.ManagedComputerDependency) Installation {
	inst := Installation{
		ID:          uuidString(row.ID),
		OperationID: row.OperationID,
		WorkspaceID: uuidString(row.WorkspaceID),

		DependencyID:     row.DependencyID,
		Source:           row.Source,
		Status:           Status(row.Status),
		InstalledVersion: row.InstalledVersion,
		LatestVersion:    row.LatestVersion,
		LastError:        row.LastError,
		LastErrorCode:    row.LastErrorCode,
		ManifestDigest:   row.ManifestDigest,
		SourceURL:        row.SourceUrl, RegistryID: row.RegistryID, DefinitionRevision: row.DefinitionRevision,
		CreatedAt: db.TimeFromPg(row.CreatedAt),
		UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
	if row.LastCheckedAt.Valid {
		checked := row.LastCheckedAt.Time
		inst.LastCheckedAt = &checked
	}
	return inst
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func nullableText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func nullableTimestamptz(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
