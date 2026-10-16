-- Dependency installation records of managed computers. A row is one
-- dependency on one computer, keyed by (team_id, workspace_id, dependency_id);
-- the service resolves a bot to its computer before it gets here. Column
-- lists follow the table's physical order (workspace_id last) so sqlc maps
-- every query onto the ManagedComputerDependency model.

-- name: GetManagedComputerDependency :one
SELECT id, team_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error, last_error_code,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id
FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = $1
  AND dependency_id = $2
LIMIT 1;

-- name: ListManagedComputerDependencies :many
SELECT id, team_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error, last_error_code,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id
FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = $1
ORDER BY dependency_id;

-- name: ListManagedComputerDependenciesByStatus :many
SELECT id, team_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error, last_error_code,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id
FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND status = $1
ORDER BY workspace_id, dependency_id;

-- name: ListStaleManagedComputerDependencyOperations :many
SELECT id, team_id, dependency_id, source, status,
       installed_version, latest_version, last_checked_at, last_error, last_error_code,
       manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id
FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND status IN ('installing', 'updating', 'removing')
  AND updated_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::double precision)
ORDER BY updated_at, id;

-- name: UpsertManagedComputerDependencyIntent :one
INSERT INTO managed_computer_dependencies (
  workspace_id, dependency_id, source, status,
  installed_version, manifest_digest, source_url, registry_id, definition_revision
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (team_id, workspace_id, dependency_id)
DO UPDATE SET source = EXCLUDED.source,
              last_error = '',
              last_error_code = '',
              status = EXCLUDED.status,
              installed_version = EXCLUDED.installed_version,
              manifest_digest = EXCLUDED.manifest_digest,
              source_url = EXCLUDED.source_url, registry_id = EXCLUDED.registry_id,
              definition_revision = EXCLUDED.definition_revision,
              updated_at = now()
WHERE managed_computer_dependencies.operation_id = ''
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;

-- name: UpdateManagedComputerDependencyStatus :one
UPDATE managed_computer_dependencies
SET status = sqlc.arg(status),
    last_error = '',
    last_error_code = sqlc.arg(last_error_code),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = ''
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;

-- name: UpdateManagedComputerDependencyObserved :one
UPDATE managed_computer_dependencies
SET source = COALESCE(sqlc.narg(source)::text, source),
    installed_version = COALESCE(sqlc.narg(installed_version)::text, installed_version),
    latest_version = COALESCE(sqlc.narg(latest_version)::text, latest_version),
    last_checked_at = COALESCE(sqlc.narg(last_checked_at)::timestamptz, last_checked_at),
    last_error = COALESCE(sqlc.narg(last_error)::text, last_error),
    last_error_code = COALESCE(sqlc.narg(last_error_code)::text, last_error_code),
    manifest_digest = COALESCE(sqlc.narg(manifest_digest)::text, manifest_digest),
    source_url = COALESCE(sqlc.narg(source_url)::text, source_url),
    registry_id = COALESCE(sqlc.narg(registry_id)::text, registry_id),
    definition_revision = COALESCE(sqlc.narg(definition_revision)::text, definition_revision),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = ''
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;

-- name: DeleteManagedComputerDependency :execrows
DELETE FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = $1
  AND dependency_id = $2
  AND operation_id = '';

-- name: ClaimManagedComputerDependencyOperation :one
INSERT INTO managed_computer_dependencies (
  workspace_id, dependency_id, source, status,
  installed_version, manifest_digest, source_url, registry_id, definition_revision, operation_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (team_id, workspace_id, dependency_id)
DO UPDATE SET status = EXCLUDED.status,
              last_error = '',
              last_error_code = '',
              operation_id = EXCLUDED.operation_id,
              updated_at = now()
WHERE managed_computer_dependencies.status NOT IN ('installing', 'updating', 'removing')
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;

-- name: FinishManagedComputerDependencyOperation :one
UPDATE managed_computer_dependencies
SET source = sqlc.arg(source),
    status = sqlc.arg(status),
    installed_version = sqlc.arg(installed_version),
    latest_version = sqlc.arg(latest_version),
    last_checked_at = sqlc.narg(last_checked_at)::timestamptz,
    last_error = '',
    last_error_code = sqlc.arg(last_error_code),
    manifest_digest = sqlc.arg(manifest_digest),
    source_url = sqlc.arg(source_url),
    registry_id = sqlc.arg(registry_id),
    definition_revision = sqlc.arg(definition_revision),
    operation_id = '',
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = sqlc.arg(operation_id) AND operation_id <> ''
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;

-- name: DeleteManagedComputerDependencyOperation :one
DELETE FROM managed_computer_dependencies
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND dependency_id = sqlc.arg(dependency_id)
  AND operation_id = sqlc.arg(operation_id) AND operation_id <> ''
RETURNING id, team_id, dependency_id, source, status,
          installed_version, latest_version, last_checked_at, last_error, last_error_code,
          manifest_digest, source_url, registry_id, definition_revision, operation_id, created_at, updated_at, workspace_id;
