-- Managed computers: the server-provisioned workspace of a bot, keyed by the
-- workspace id. Intent columns are written by the API layer; observation
-- columns and the lease by the reconciler only; instance columns by the
-- workspace manager when it creates, starts, stops or removes the container.

-- name: UpsertManagedComputerIntent :one
-- Record a new intent for the computer. A new row starts at generation 1; an
-- existing row bumps the generation, keeps the previous image when none is
-- given, resets the retry budget and makes the row due now.
INSERT INTO managed_computers (workspace_id, desired_state, image, preserve_data)
VALUES (sqlc.arg(workspace_id), sqlc.arg(desired_state), sqlc.arg(image), sqlc.arg(preserve_data))
ON CONFLICT (workspace_id) DO UPDATE SET
  desired_state      = EXCLUDED.desired_state,
  desired_generation = managed_computers.desired_generation + 1,
  image              = CASE WHEN EXCLUDED.image <> '' THEN EXCLUDED.image ELSE managed_computers.image END,
  preserve_data      = EXCLUDED.preserve_data,
  attempts           = 0,
  next_attempt_at    = now(),
  version            = managed_computers.version + 1,
  updated_at         = now()
RETURNING *;

-- name: CancelManagedComputerRemoval :one
-- Withdraw a removal that failed: the computer is desired again and recorded
-- as the backend reports it, in one write. Split in two, a pass in between
-- would see a present computer observed as failed and provision over one that
-- is still there. Matches nothing unless a removal failed and is settled.
UPDATE managed_computers
SET
  desired_state       = 'present',
  desired_generation  = desired_generation + 1,
  preserve_data       = false,
  observed_state      = sqlc.arg(observed_state),
  observed_generation = desired_generation + 1,
  ever_ready          = ever_ready OR sqlc.arg(observed_state) = 'running',
  last_error          = '',
  last_error_phase    = '',
  attempts            = 0,
  next_attempt_at     = now(),
  version             = version + 1,
  updated_at          = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND desired_state = 'absent'
  AND observed_state = 'failed'
  AND observed_generation >= desired_generation
RETURNING *;

-- name: GetManagedComputer :one
SELECT * FROM managed_computers
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: GetManagedComputerByBotID :one
-- The computer behind the bot's first managed binding. Every bot -> computer
-- mapping goes through bot_workspace_bindings: the workspace id of a bot
-- created after 0167 is not the bot id.
SELECT mc.*
FROM managed_computers mc
JOIN bot_workspace_bindings b
  ON b.team_id = mc.team_id
 AND b.workspace_id = mc.workspace_id
JOIN workspaces w
  ON w.team_id = mc.team_id
 AND w.id = mc.workspace_id
WHERE mc.team_id = public.memoh_current_team_id()
  AND b.bot_id = sqlc.arg(bot_id)
  AND w.kind = 'managed'
ORDER BY b.created_at ASC, b.id ASC
LIMIT 1;

-- name: GetManagedComputerByInstanceID :one
SELECT * FROM managed_computers
WHERE team_id = public.memoh_current_team_id() AND instance_id = sqlc.arg(instance_id);

-- name: ClaimManagedComputers :many
-- Claim due rows for one reconcile pass. A row is due when it has not yet
-- responded to its latest intent, is stuck in a transitional state whose lease
-- expired, or is a failed provisioning whose backoff elapsed. A failed removal
-- is not due: it waits for a new intent. SKIP LOCKED keeps concurrent Server
-- instances from claiming the same row; an expired lease lets another instance
-- take over a crashed pass.
UPDATE managed_computers
SET
  lease_owner = sqlc.arg(lease_owner),
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  version     = version + 1,
  updated_at  = now()
WHERE workspace_id IN (
  SELECT workspace_id FROM managed_computers
  WHERE team_id = public.memoh_current_team_id()
    AND next_attempt_at <= now()
    AND (lease_until IS NULL OR lease_until < now())
    AND (
      observed_generation < desired_generation
      OR observed_state IN ('provisioning', 'removing')
      OR (desired_state = 'present' AND observed_state IN ('absent', 'failed'))
      OR (desired_state = 'absent' AND observed_state NOT IN ('absent', 'failed'))
    )
  ORDER BY next_attempt_at
  LIMIT sqlc.arg(lim)::int
  FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ClaimManagedComputer :one
-- Claim one specific row (for an on-demand observation after a user action)
-- when nobody else holds its lease.
UPDATE managed_computers
SET
  lease_owner = sqlc.arg(lease_owner),
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  version     = version + 1,
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND (lease_until IS NULL OR lease_until < now())
RETURNING *;

-- name: RenewManagedComputerLease :execrows
UPDATE managed_computers
SET
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: UpdateManagedComputerObserved :one
-- Write an observation. The lease must still be held by the caller; the
-- version pins the row the caller read so a concurrent intent bump is not
-- silently overwritten (the caller re-reads and re-decides instead).
UPDATE managed_computers
SET
  observed_state      = sqlc.arg(observed_state),
  observed_generation = sqlc.arg(observed_generation),
  ever_ready          = ever_ready OR sqlc.arg(mark_ready)::boolean,
  last_error          = sqlc.arg(last_error),
  last_error_code     = sqlc.arg(last_error_code),
  last_error_phase    = sqlc.arg(last_error_phase),
  attempts            = sqlc.arg(attempts),
  next_attempt_at     = sqlc.arg(next_attempt_at),
  lease_owner         = CASE WHEN sqlc.arg(release_lease)::boolean THEN '' ELSE lease_owner END,
  lease_until         = CASE WHEN sqlc.arg(release_lease)::boolean THEN NULL ELSE lease_until END,
  version             = version + 1,
  updated_at          = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND lease_owner = sqlc.arg(lease_owner)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: ReleaseManagedComputerLease :execrows
UPDATE managed_computers
SET
  lease_owner = '',
  lease_until = NULL,
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND workspace_id = sqlc.arg(workspace_id)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: ListManagedComputersByObservedState :many
SELECT * FROM managed_computers
WHERE team_id = public.memoh_current_team_id()
  AND observed_state = sqlc.arg(observed_state)
ORDER BY updated_at DESC
LIMIT sqlc.arg(lim)::int;

-- name: CountManagedComputersByObservedState :many
SELECT observed_state, COUNT(*)::bigint AS total
FROM managed_computers
WHERE team_id = public.memoh_current_team_id()
GROUP BY observed_state;

-- name: UpsertManagedComputerInstance :exec
-- Record the container instance of a computer. A computer without a row yet
-- (a legacy container found by the manager) is desired present and observed
-- by the instance status, ever_ready so no automated step may delete its
-- data; an existing row keeps its intent and observation and only takes the
-- instance columns.
INSERT INTO managed_computers (
  workspace_id, desired_state, desired_generation, image,
  observed_state, observed_generation, ever_ready,
  instance_id, instance_name, instance_status, namespace, auto_start, container_path
)
VALUES (
  sqlc.arg(workspace_id),
  'present',
  1,
  sqlc.arg(image),
  CASE WHEN sqlc.arg(instance_status)::text = 'running' THEN 'running' ELSE 'stopped' END,
  1,
  true,
  sqlc.arg(instance_id),
  sqlc.arg(instance_name),
  sqlc.arg(instance_status),
  sqlc.arg(namespace),
  sqlc.arg(auto_start),
  sqlc.arg(container_path)
)
ON CONFLICT (workspace_id) DO UPDATE SET
  image           = EXCLUDED.image,
  instance_id     = EXCLUDED.instance_id,
  instance_name   = EXCLUDED.instance_name,
  instance_status = EXCLUDED.instance_status,
  namespace       = EXCLUDED.namespace,
  auto_start      = EXCLUDED.auto_start,
  container_path  = EXCLUDED.container_path,
  updated_at      = now();

-- name: ClearManagedComputerInstance :exec
-- The container is gone; the computer and its intent stay.
UPDATE managed_computers
SET
  instance_id     = '',
  instance_name   = '',
  instance_status = '',
  last_started_at = NULL,
  last_stopped_at = NULL,
  updated_at      = now()
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: UpdateManagedComputerInstanceStatus :exec
UPDATE managed_computers
SET instance_status = sqlc.arg(instance_status), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: MarkManagedComputerStarted :exec
UPDATE managed_computers
SET instance_status = 'running', last_started_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: MarkManagedComputerStopped :exec
UPDATE managed_computers
SET instance_status = 'stopped', last_stopped_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND workspace_id = sqlc.arg(workspace_id);

-- name: ListAutoStartManagedComputers :many
-- Computers with a recorded container that the startup reconcile checks.
SELECT * FROM managed_computers
WHERE team_id = public.memoh_current_team_id()
  AND auto_start = true
  AND instance_id <> ''
ORDER BY updated_at DESC;
