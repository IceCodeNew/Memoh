-- Versions of a managed computer; numbers count per computer.

-- name: ListManagedComputerVersions :many
SELECT
  v.id,
  v.workspace_id,
  v.container_id,
  v.snapshot_id,
  v.version,
  v.created_at,
  s.runtime_snapshot_name,
  s.display_name
FROM managed_computer_versions v
JOIN managed_computer_snapshots s ON s.id = v.snapshot_id
WHERE v.team_id = public.memoh_current_team_id()
  AND s.team_id = public.memoh_current_team_id()
  AND v.workspace_id = sqlc.arg(workspace_id)
ORDER BY v.version ASC;

-- name: NextManagedComputerVersion :one
SELECT COALESCE(MAX(version), 0) + 1 FROM managed_computer_versions
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: InsertManagedComputerVersion :one
INSERT INTO managed_computer_versions (workspace_id, container_id, snapshot_id, version)
VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(container_id),
  sqlc.arg(snapshot_id),
  sqlc.arg(version)
)
RETURNING *;

-- name: GetManagedComputerVersionSnapshotRuntimeName :one
SELECT s.runtime_snapshot_name
FROM managed_computer_versions v
JOIN managed_computer_snapshots s ON s.id = v.snapshot_id
WHERE v.team_id = public.memoh_current_team_id()
  AND s.team_id = public.memoh_current_team_id()
  AND v.workspace_id = sqlc.arg(workspace_id)
  AND v.version = sqlc.arg(version);

-- name: DeleteManagedComputerVersions :exec
DELETE FROM managed_computer_versions
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);
