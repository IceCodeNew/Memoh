-- Lifecycle events of a managed computer.

-- name: InsertManagedComputerEvent :exec
INSERT INTO managed_computer_events (id, workspace_id, container_id, event_type, payload)
VALUES (
  sqlc.arg(id),
  sqlc.arg(workspace_id),
  sqlc.arg(container_id),
  sqlc.arg(event_type),
  sqlc.arg(payload)
);

-- name: DeleteManagedComputerEvents :exec
DELETE FROM managed_computer_events
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);
