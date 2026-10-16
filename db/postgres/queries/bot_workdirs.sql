-- A workdir is stored by the workspace it lives on. Every read joins the
-- workspace's kind and the bot's binding to it so the service can derive the
-- client-facing target address ("native" for a managed computer, the binding
-- id for a user's computer) without a second lookup. The binding always
-- exists: bot_workdirs_binding_fkey hangs the row off it.

-- name: CreateBotWorkdir :one
INSERT INTO bot_workdirs (
  bot_id, name, workspace_id, path, created_by_user_id
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(name),
  sqlc.arg(workspace_id),
  sqlc.arg(path),
  sqlc.narg(created_by_user_id)::uuid
)
RETURNING id;

-- name: ListBotWorkdirs :many
SELECT
  d.*,
  w.kind AS workspace_kind,
  binding.id AS binding_id
FROM bot_workdirs d
JOIN workspaces w
  ON w.team_id = d.team_id
 AND w.id = d.workspace_id
JOIN bot_workspace_bindings binding
  ON binding.team_id = d.team_id
 AND binding.bot_id = d.bot_id
 AND binding.workspace_id = d.workspace_id
WHERE d.team_id = public.memoh_current_team_id()
  AND d.bot_id = sqlc.arg(bot_id)
  AND (sqlc.arg(include_archived)::bool OR d.archived_at IS NULL)
ORDER BY d.created_at ASC, d.id ASC;

-- name: GetBotWorkdir :one
-- Archived rows are returned on purpose: sessions bound to an archived
-- workdir keep resolving their working directory; callers that must refuse
-- archived workdirs (session creation) check archived_at themselves.
SELECT
  d.*,
  w.kind AS workspace_kind,
  binding.id AS binding_id
FROM bot_workdirs d
JOIN workspaces w
  ON w.team_id = d.team_id
 AND w.id = d.workspace_id
JOIN bot_workspace_bindings binding
  ON binding.team_id = d.team_id
 AND binding.bot_id = d.bot_id
 AND binding.workspace_id = d.workspace_id
WHERE d.team_id = public.memoh_current_team_id()
  AND d.bot_id = sqlc.arg(bot_id)
  AND d.id = sqlc.arg(workdir_id);

-- name: RenameBotWorkdir :one
UPDATE bot_workdirs
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(workdir_id)
  AND archived_at IS NULL
RETURNING id;

-- name: ArchiveBotWorkdir :one
UPDATE bot_workdirs
SET archived_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(workdir_id)
  AND archived_at IS NULL
RETURNING id;
