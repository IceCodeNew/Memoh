-- workspaces: computers a team knows about (managed per bot, or a user's own
-- machine paired through Remote Runtime). bot_workspace_bindings: which bots
-- may use which workspace, one primary per bot. Remote-mount compatibility
-- queries that join user_runtimes live in user_runtimes.sql.

-- name: CreateWorkspace :one
-- A user workspace reuses its runtime id; managed workspaces take a new one.
INSERT INTO workspaces (id, owner_user_id, kind, name, metadata)
VALUES (
  COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()),
  sqlc.arg(owner_user_id),
  sqlc.arg(kind),
  sqlc.arg(name),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id);

-- name: ListWorkspacesByOwner :many
SELECT * FROM workspaces
WHERE team_id = public.memoh_current_team_id()
  AND owner_user_id = sqlc.arg(owner_user_id)
ORDER BY created_at ASC, id ASC;

-- name: ListWorkspacesByBot :many
-- Every workspace the bot is bound to, in binding order.
SELECT w.*
FROM workspaces w
JOIN bot_workspace_bindings binding
  ON binding.team_id = w.team_id
 AND binding.workspace_id = w.id
WHERE w.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
ORDER BY binding.created_at ASC, binding.id ASC;

-- name: UpdateWorkspaceName :one
UPDATE workspaces
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteWorkspace :execrows
-- Fails with a foreign key violation while a binding still references it
-- (the key is deferred, so outside autocommit the error surfaces at COMMIT).
DELETE FROM workspaces
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id);

-- name: DeleteWorkspaceIfUnbound :execrows
-- Removes the workspace only once nothing binds it; zero rows means it is
-- still bound or already gone.
DELETE FROM workspaces w
WHERE w.team_id = public.memoh_current_team_id()
  AND w.id = sqlc.arg(id)
  AND NOT EXISTS (
    SELECT 1 FROM bot_workspace_bindings binding
    WHERE binding.team_id = w.team_id
      AND binding.workspace_id = w.id
  );

-- name: UpsertBotWorkspaceBinding :one
-- Binds the bot to the workspace; an existing binding keeps its primary flag
-- and approval config. Both rows must belong to the current team.
INSERT INTO bot_workspace_bindings (bot_id, workspace_id, is_primary)
SELECT b.id, w.id, sqlc.arg(is_primary)::boolean
FROM bots b
JOIN workspaces w
  ON w.team_id = public.memoh_current_team_id()
 AND w.id = sqlc.arg(workspace_id)
WHERE b.team_id = public.memoh_current_team_id()
  AND b.id = sqlc.arg(bot_id)
ON CONFLICT (team_id, bot_id, workspace_id) DO UPDATE SET
  updated_at = now()
RETURNING *;

-- name: GetBotWorkspaceBinding :one
SELECT * FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(binding_id);

-- name: ListBotWorkspaceBindingsByBot :many
SELECT * FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
ORDER BY created_at ASC, id ASC;

-- name: ListBotWorkspaceBindingsByWorkspace :many
SELECT * FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at ASC, id ASC;

-- name: GetPrimaryBotWorkspaceBinding :one
SELECT * FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND is_primary;

-- name: ClearBotWorkspaceBindingPrimary :exec
UPDATE bot_workspace_bindings
SET is_primary = FALSE, updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND is_primary;

-- name: SetBotWorkspaceBindingPrimary :execrows
-- Callers clear the previous primary in the same transaction first.
UPDATE bot_workspace_bindings
SET is_primary = TRUE,
    updated_at = CASE WHEN is_primary THEN updated_at ELSE now() END
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(binding_id);

-- name: SetBotManagedWorkspaceBindingPrimary :execrows
-- "Primary is native": the bot's managed binding becomes primary. Zero rows
-- means the bot has no managed workspace, which callers report rather than
-- ignore.
UPDATE bot_workspace_bindings binding
SET is_primary = TRUE,
    updated_at = CASE WHEN binding.is_primary THEN binding.updated_at ELSE now() END
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.id = (
    SELECT managed.id
    FROM bot_workspace_bindings managed
    JOIN workspaces w
      ON w.team_id = managed.team_id
     AND w.id = managed.workspace_id
     AND w.kind = 'managed'
    WHERE managed.team_id = public.memoh_current_team_id()
      AND managed.bot_id = sqlc.arg(bot_id)
    ORDER BY managed.created_at ASC, managed.id ASC
    LIMIT 1
  );

-- name: UpdateBotWorkspaceBindingToolApproval :one
UPDATE bot_workspace_bindings
SET tool_approval_config = sqlc.arg(tool_approval_config),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(binding_id)
RETURNING *;

-- name: DeleteBotWorkspaceBinding :execrows
DELETE FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(binding_id);

-- name: DeleteBotWorkspaceBindingsByWorkspace :execrows
-- Revoking a user's computer removes every bot's binding to it in the same
-- transaction; the workspace row itself stays.
DELETE FROM bot_workspace_bindings
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id);

-- name: GetBotManagedWorkspaceID :one
-- The bot's managed computer: the workspace behind its first managed binding.
SELECT w.id
FROM workspaces w
JOIN bot_workspace_bindings binding
  ON binding.team_id = w.team_id
 AND binding.workspace_id = w.id
WHERE w.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND w.kind = 'managed'
ORDER BY binding.created_at ASC, binding.id ASC
LIMIT 1;
