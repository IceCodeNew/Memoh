-- Desired resource limits of a managed computer. 0 means unlimited.

-- name: GetManagedComputerResourceLimits :one
SELECT * FROM managed_computer_resource_limits
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: GetManagedComputerResourceLimitsByBotID :one
-- The limits of the computer behind the bot's first managed binding.
SELECT l.*
FROM managed_computer_resource_limits l
JOIN bot_workspace_bindings b
  ON b.team_id = l.team_id
 AND b.workspace_id = l.workspace_id
JOIN workspaces w
  ON w.team_id = l.team_id
 AND w.id = l.workspace_id
WHERE l.team_id = public.memoh_current_team_id()
  AND b.bot_id = sqlc.arg(bot_id)
  AND w.kind = 'managed'
ORDER BY b.created_at ASC, b.id ASC
LIMIT 1;

-- name: UpsertManagedComputerResourceLimits :one
INSERT INTO managed_computer_resource_limits (
  workspace_id, cpu_millicores, memory_bytes, storage_bytes
)
VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(cpu_millicores),
  sqlc.arg(memory_bytes),
  sqlc.arg(storage_bytes)
)
ON CONFLICT (team_id, workspace_id) DO UPDATE SET
  cpu_millicores = EXCLUDED.cpu_millicores,
  memory_bytes = EXCLUDED.memory_bytes,
  storage_bytes = EXCLUDED.storage_bytes,
  updated_at = now()
RETURNING *;
