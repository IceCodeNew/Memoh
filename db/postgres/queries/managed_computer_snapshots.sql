-- Snapshots of a managed computer, keyed by the workspace. container_id is
-- the instance the snapshot was taken on.

-- name: UpsertManagedComputerSnapshot :one
INSERT INTO managed_computer_snapshots (
  workspace_id,
  container_id,
  runtime_snapshot_name,
  display_name,
  parent_runtime_snapshot_name,
  snapshotter,
  source
)
VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(container_id),
  sqlc.arg(runtime_snapshot_name),
  sqlc.arg(display_name),
  sqlc.arg(parent_runtime_snapshot_name),
  sqlc.arg(snapshotter),
  sqlc.arg(source)
)
ON CONFLICT (team_id, workspace_id, runtime_snapshot_name) DO UPDATE
SET
  container_id = EXCLUDED.container_id,
  display_name = EXCLUDED.display_name,
  parent_runtime_snapshot_name = EXCLUDED.parent_runtime_snapshot_name,
  snapshotter = EXCLUDED.snapshotter,
  source = EXCLUDED.source
RETURNING *;

-- name: ListManagedComputerSnapshots :many
SELECT * FROM managed_computer_snapshots
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at DESC;

-- name: ListManagedComputerSnapshotsWithVersion :many
SELECT
  s.id,
  s.workspace_id,
  s.container_id,
  s.runtime_snapshot_name,
  s.display_name,
  s.parent_runtime_snapshot_name,
  s.snapshotter,
  s.source,
  s.created_at,
  v.version
FROM managed_computer_snapshots s
LEFT JOIN managed_computer_versions v
  ON v.snapshot_id = s.id AND v.team_id = public.memoh_current_team_id()
WHERE s.team_id = public.memoh_current_team_id() AND s.workspace_id = sqlc.arg(workspace_id)
ORDER BY s.created_at DESC;

-- name: GetManagedComputerSnapshotByRuntimeName :one
SELECT * FROM managed_computer_snapshots
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND runtime_snapshot_name = sqlc.arg(runtime_snapshot_name)
LIMIT 1;

-- name: DeleteManagedComputerSnapshots :exec
-- Versions referencing the snapshots must be deleted first (RESTRICT).
DELETE FROM managed_computer_snapshots
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);
