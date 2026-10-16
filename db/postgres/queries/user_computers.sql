-- name: CreateUserComputer :one
INSERT INTO user_computers (user_id, name, api_token)
VALUES (sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(api_token))
RETURNING *;

-- name: GetUserComputerByAPIToken :one
SELECT runtime.*
FROM user_computers runtime
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = runtime.user_id
 AND owner_membership.is_active = TRUE
JOIN users owner ON owner.id = owner_membership.user_id AND owner.is_active = TRUE
WHERE runtime.team_id = public.memoh_current_team_id()
  AND runtime.api_token = sqlc.arg(api_token)
  AND runtime.revoked_at IS NULL
  AND (runtime.activated_at IS NOT NULL OR runtime.pending_expires_at > now());

-- name: ActivateUserComputer :one
-- The first ready connection consumes the short-lived pending state. Existing
-- activated credentials remain reusable for later reconnects.
UPDATE user_computers runtime
SET activated_at = COALESCE(runtime.activated_at, now()),
    pending_expires_at = NULL,
    updated_at = CASE WHEN runtime.activated_at IS NULL THEN now() ELSE runtime.updated_at END
FROM team_members owner_membership, users owner
WHERE runtime.team_id = public.memoh_current_team_id()
  AND runtime.id = sqlc.arg(id)
  AND runtime.api_token = sqlc.arg(api_token)
  AND runtime.revoked_at IS NULL
  AND (runtime.activated_at IS NOT NULL OR runtime.pending_expires_at > now())
  AND owner_membership.team_id = public.memoh_current_team_id()
  AND owner_membership.user_id = runtime.user_id
  AND owner_membership.is_active = TRUE
  AND owner.id = owner_membership.user_id
  AND owner.is_active = TRUE
RETURNING runtime.*;

-- name: ExpirePendingUserComputers :exec
-- Expired attempts can no longer authenticate. Mark them revoked and remove
-- any defensive direct-API bindings so they release names and grant rows too.
WITH expired AS (
  UPDATE user_computers
  SET revoked_at = now(), updated_at = now()
  WHERE team_id = public.memoh_current_team_id()
    AND user_id = sqlc.arg(user_id)
    AND revoked_at IS NULL
    AND activated_at IS NULL
    AND pending_expires_at <= now()
  RETURNING id
)
DELETE FROM bot_workspace_bindings binding
USING expired
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.workspace_id = expired.id;

-- name: ListUserComputers :many
SELECT * FROM user_computers
WHERE team_id = public.memoh_current_team_id()
  AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
ORDER BY created_at ASC, id ASC;

-- name: RevokeUserComputer :one
UPDATE user_computers
SET revoked_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING *;

-- name: BackfillUserComputerName :execrows
-- Fills the display name from the connecting machine, but ONLY while the row
-- still carries its creation-time default (or an empty name): a user-chosen
-- name is never overwritten by a later handshake.
UPDATE user_computers
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
  AND (name = '' OR name = sqlc.arg(default_name));

-- Remote mounts are bot_workspace_bindings whose workspace has kind 'user';
-- that workspace shares the user_computers id, so runtime_id below is the
-- binding's workspace_id.

-- name: CreateOrUpdateBotRemoteRuntimeMount :one
INSERT INTO bot_workspace_bindings (bot_id, workspace_id)
SELECT b.id, ws.id
FROM bots b
JOIN user_computers r
  ON r.id = sqlc.arg(runtime_id)
 AND r.team_id = public.memoh_current_team_id()
 AND r.user_id = b.owner_user_id
 AND r.revoked_at IS NULL
 AND r.activated_at IS NOT NULL
JOIN workspaces ws
  ON ws.team_id = public.memoh_current_team_id()
 AND ws.id = r.id
 AND ws.kind = 'user'
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = b.owner_user_id
 AND owner_membership.is_active = TRUE
JOIN users owner ON owner.id = owner_membership.user_id AND owner.is_active = TRUE
WHERE b.team_id = public.memoh_current_team_id()
  AND b.id = sqlc.arg(bot_id)
ON CONFLICT (team_id, bot_id, workspace_id) DO UPDATE SET
  updated_at = now()
RETURNING id;

-- name: ListBotRemoteRuntimeMounts :many
SELECT
  binding.id,
  binding.bot_id,
  binding.workspace_id AS runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_workspace_bindings binding
JOIN workspaces ws ON ws.id = binding.workspace_id AND ws.team_id = public.memoh_current_team_id() AND ws.kind = 'user'
JOIN user_computers runtime ON runtime.id = ws.id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND runtime.revoked_at IS NULL
ORDER BY binding.created_at ASC, binding.id ASC;

-- name: GetBotRemoteRuntimeMount :one
SELECT
  binding.id,
  binding.bot_id,
  binding.workspace_id AS runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_workspace_bindings binding
JOIN workspaces ws ON ws.id = binding.workspace_id AND ws.team_id = public.memoh_current_team_id() AND ws.kind = 'user'
JOIN user_computers runtime ON runtime.id = ws.id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.id = sqlc.arg(target_id);

-- name: GetPrimaryBotRemoteRuntimeMount :one
-- No row when the bot's primary binding is its managed workspace: callers
-- read that as "native is primary".
SELECT
  binding.id,
  binding.bot_id,
  binding.workspace_id AS runtime_id,
  binding.is_primary,
  binding.tool_approval_config,
  binding.created_at,
  binding.updated_at,
  runtime.name AS runtime_name,
  runtime.user_id AS runtime_user_id,
  (runtime.revoked_at IS NOT NULL OR NOT owner.is_active OR NOT owner_membership.is_active) AS runtime_unavailable,
  bot.owner_user_id AS bot_owner_user_id
FROM bot_workspace_bindings binding
JOIN workspaces ws ON ws.id = binding.workspace_id AND ws.team_id = public.memoh_current_team_id() AND ws.kind = 'user'
JOIN user_computers runtime ON runtime.id = ws.id AND runtime.team_id = public.memoh_current_team_id()
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN team_members owner_membership
  ON owner_membership.team_id = public.memoh_current_team_id()
 AND owner_membership.user_id = bot.owner_user_id
JOIN users owner ON owner.id = owner_membership.user_id
WHERE binding.team_id = public.memoh_current_team_id()
  AND binding.bot_id = sqlc.arg(bot_id)
  AND binding.is_primary = TRUE;

-- name: ListBotRemoteRuntimeGrantsByRuntimeOwner :many
-- Account-level reverse lookup: every live mount held by the owner's bots.
-- A mount can only exist while runtime.user_id = bot.owner_user_id (enforced
-- by CreateOrUpdateBotRemoteRuntimeMount), so scoping by the bot owner covers
-- exactly the grants on the owner's own runtimes.
SELECT
  binding.id,
  binding.bot_id,
  binding.workspace_id AS runtime_id,
  binding.is_primary
FROM bot_workspace_bindings binding
JOIN workspaces ws ON ws.id = binding.workspace_id AND ws.team_id = public.memoh_current_team_id() AND ws.kind = 'user'
JOIN bots bot ON bot.id = binding.bot_id AND bot.team_id = public.memoh_current_team_id()
JOIN user_computers runtime ON runtime.id = ws.id AND runtime.team_id = public.memoh_current_team_id()
WHERE binding.team_id = public.memoh_current_team_id()
  AND bot.owner_user_id = sqlc.arg(owner_user_id)
  AND runtime.revoked_at IS NULL
ORDER BY binding.created_at ASC, binding.id ASC;
