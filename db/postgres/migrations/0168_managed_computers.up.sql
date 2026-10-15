-- 0168_managed_computers
-- A managed computer is persisted by its workspace id. `containers` (the
-- instance record) and `bot_workspaces` (the reconciler's intent / observation
-- slot) merge into `managed_computers`, keyed by workspaces.id; snapshots,
-- versions, lifecycle events and resource limits hang off that key instead of
-- the instance id (which changes when a container is rebuilt) or the bot id
-- (a bot is bound to a computer, it no longer owns one).
--
-- Mapping rule: a bot's managed computer is the workspace behind its first
-- managed binding (bot_workspace_bindings JOIN workspaces WHERE kind =
-- 'managed', ordered by created_at). Bots created after 0167 have a managed
-- workspace id that differs from the bot id, so nothing here assumes the two
-- are equal.
--
-- Instance columns keep the container naming of this stage: new containers
-- are still called workspace-<bot_id>; only the record key moves.
--
-- Orphans: snapshot / version / event rows whose container_id has no
-- containers row, or whose bot has no managed binding, and resource limits of
-- bots without a managed computer cannot be attached to a workspace and are
-- deleted. Nothing user-visible changes: HTTP routes and payloads stay the
-- same.
--
-- Idempotency: a fresh install already has the final tables from 0001 and
-- none of the legacy ones (apart from the empty shells 0092 / 0153 recreate),
-- so every legacy step is guarded with to_regclass and the backfill reads
-- through temp tables that are empty when the legacy tables are missing.

BEGIN;

-- ---------------------------------------------------------------------------
-- managed_computers
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.managed_computers (
    workspace_id        UUID        PRIMARY KEY,
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    -- Intent, written only by the API layer.
    desired_state       TEXT        NOT NULL,
    desired_generation  BIGINT      NOT NULL DEFAULT 1,
    image               TEXT        NOT NULL DEFAULT '',
    preserve_data       BOOLEAN     NOT NULL DEFAULT false,
    -- Observation, written only by the reconciler while holding the lease.
    observed_state      TEXT        NOT NULL DEFAULT 'absent',
    observed_generation BIGINT      NOT NULL DEFAULT 0,
    ever_ready          BOOLEAN     NOT NULL DEFAULT false,
    last_error          TEXT        NOT NULL DEFAULT '',
    last_error_code     TEXT        NOT NULL DEFAULT '',
    last_error_phase    TEXT        NOT NULL DEFAULT '',
    attempts            INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner         TEXT        NOT NULL DEFAULT '',
    lease_until         TIMESTAMPTZ,
    version             BIGINT      NOT NULL DEFAULT 1,
    -- Instance record (the former containers row). instance_id is the runtime
    -- container id and is empty until a container exists.
    instance_id         TEXT        NOT NULL DEFAULT '',
    instance_name       TEXT        NOT NULL DEFAULT '',
    instance_status     TEXT        NOT NULL DEFAULT '',
    namespace           TEXT        NOT NULL DEFAULT 'default',
    auto_start          BOOLEAN     NOT NULL DEFAULT true,
    container_path      TEXT        NOT NULL DEFAULT '/data',
    last_started_at     TIMESTAMPTZ,
    last_stopped_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- md5('managed_computers:managed_computers_pkey'), the way 0112 derives
    -- team helper keys, so a fresh install and this upgrade agree.
    CONSTRAINT memoh_team_key_8ad0a56db6a1 UNIQUE (team_id, workspace_id),
    CONSTRAINT managed_computers_desired_state_check
        CHECK (desired_state IN ('present', 'absent')),
    CONSTRAINT managed_computers_observed_state_check
        CHECK (observed_state IN ('absent', 'provisioning', 'running', 'stopped', 'failed', 'removing')),
    CONSTRAINT managed_computers_last_error_phase_check
        CHECK (last_error_phase IN ('', 'image_prepare', 'start', 'bridge', 'bootstrap', 'teardown'))
);

-- workspaces is under FORCE ROW LEVEL SECURITY; NOT VALID skips the
-- policy-scoped validation scan. New rows are still checked.
ALTER TABLE public.managed_computers
    DROP CONSTRAINT IF EXISTS managed_computers_workspace_id_fkey;
ALTER TABLE public.managed_computers
    ADD CONSTRAINT managed_computers_workspace_id_fkey
    FOREIGN KEY (team_id, workspace_id)
    REFERENCES public.workspaces(team_id, id) ON DELETE CASCADE
    NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_computers_instance_id
    ON public.managed_computers (team_id, instance_id)
    WHERE instance_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_computers_instance_name
    ON public.managed_computers (team_id, instance_name)
    WHERE instance_name <> '';
-- The reconciler claims due rows ordered by next_attempt_at.
CREATE INDEX IF NOT EXISTS idx_managed_computers_due
    ON public.managed_computers (team_id, next_attempt_at);

ALTER TABLE public.managed_computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS managed_computers_team_select ON public.managed_computers;
DROP POLICY IF EXISTS managed_computers_team_insert ON public.managed_computers;
DROP POLICY IF EXISTS managed_computers_team_update ON public.managed_computers;
DROP POLICY IF EXISTS managed_computers_team_delete ON public.managed_computers;

CREATE POLICY managed_computers_team_select ON public.managed_computers
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computers_team_insert ON public.managed_computers
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computers_team_update ON public.managed_computers
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computers_team_delete ON public.managed_computers
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- snapshots / container_versions / lifecycle_events -> managed_computer_*
-- ---------------------------------------------------------------------------
-- The tables are renamed in place so rows keep their ids. container_id stays
-- as the instance the row was taken on; workspace_id becomes the key.
DO $$
BEGIN
    IF to_regclass('public.snapshots') IS NOT NULL
       AND to_regclass('public.managed_computer_snapshots') IS NULL THEN
        ALTER TABLE public.snapshots RENAME TO managed_computer_snapshots;
    END IF;
    IF to_regclass('public.container_versions') IS NOT NULL
       AND to_regclass('public.managed_computer_versions') IS NULL THEN
        ALTER TABLE public.container_versions RENAME TO managed_computer_versions;
    END IF;
    IF to_regclass('public.lifecycle_events') IS NOT NULL
       AND to_regclass('public.managed_computer_events') IS NULL THEN
        ALTER TABLE public.lifecycle_events RENAME TO managed_computer_events;
    END IF;
END
$$;

ALTER TABLE public.managed_computer_snapshots ADD COLUMN IF NOT EXISTS workspace_id UUID;
ALTER TABLE public.managed_computer_versions  ADD COLUMN IF NOT EXISTS workspace_id UUID;
ALTER TABLE public.managed_computer_events    ADD COLUMN IF NOT EXISTS workspace_id UUID;

-- Constraints that survive the rename get the names a fresh install gives
-- them (the primary key, the root team key, the 0112 helper key derived from
-- the new table name, and the snapshot reference of versions).
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('managed_computer_snapshots', 'snapshots_pkey',                     'managed_computer_snapshots_pkey'),
            ('managed_computer_snapshots', 'snapshots_team_id_fkey',             'managed_computer_snapshots_team_id_fkey'),
            ('managed_computer_snapshots', 'memoh_team_key_6bceec3b7fb5',        'memoh_team_key_78dc0d778ed1'),
            ('managed_computer_versions',  'container_versions_pkey',            'managed_computer_versions_pkey'),
            ('managed_computer_versions',  'container_versions_team_id_fkey',    'managed_computer_versions_team_id_fkey'),
            ('managed_computer_versions',  'container_versions_snapshot_id_fkey','managed_computer_versions_snapshot_id_fkey'),
            ('managed_computer_versions',  'memoh_team_key_4e4a7126f607',        'memoh_team_key_71818849b2d9'),
            ('managed_computer_events',    'lifecycle_events_pkey',              'managed_computer_events_pkey'),
            ('managed_computer_events',    'lifecycle_events_team_id_fkey',      'managed_computer_events_team_id_fkey'),
            ('managed_computer_events',    'memoh_team_key_38bce59cf848',        'memoh_team_key_2e51805b8402')
        ) AS v(tbl, old_name, new_name)
    LOOP
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = ('public.' || quote_ident(r.tbl))::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = ('public.' || quote_ident(r.tbl))::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I', r.tbl, r.old_name, r.new_name);
        END IF;
    END LOOP;
END
$$;

-- PostgreSQL 18 records NOT NULL as named constraints (<table>_<column>_not_null)
-- that a rename leaves behind; give them the fresh-install names. Older
-- servers have no such rows and skip this.
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT v.tbl, v.old_prefix, c.conname
        FROM (VALUES
            ('managed_computer_snapshots', 'snapshots'),
            ('managed_computer_versions',  'container_versions'),
            ('managed_computer_events',    'lifecycle_events')
        ) AS v(tbl, old_prefix)
        JOIN pg_constraint c ON c.conrelid = ('public.' || quote_ident(v.tbl))::regclass
        WHERE c.contype = 'n' AND c.conname LIKE v.old_prefix || '\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I',
            r.tbl, r.conname, r.tbl || substr(r.conname, length(r.old_prefix) + 1));
    END LOOP;
END
$$;

ALTER INDEX IF EXISTS public.idx_snapshots_runtime_name          RENAME TO idx_managed_computer_snapshots_runtime_name;
ALTER INDEX IF EXISTS public.idx_container_versions_snapshot_id  RENAME TO idx_managed_computer_versions_snapshot_id;
ALTER INDEX IF EXISTS public.idx_lifecycle_events_event_type     RENAME TO idx_managed_computer_events_event_type;

-- The instance id is no longer the key: drop the containers references, the
-- per-container version numbering and the per-container indexes.
ALTER TABLE public.managed_computer_snapshots DROP CONSTRAINT IF EXISTS snapshots_container_id_fkey;
ALTER TABLE public.managed_computer_versions  DROP CONSTRAINT IF EXISTS container_versions_container_id_fkey;
ALTER TABLE public.managed_computer_versions  DROP CONSTRAINT IF EXISTS container_versions_container_id_version_key;
ALTER TABLE public.managed_computer_events    DROP CONSTRAINT IF EXISTS lifecycle_events_container_id_fkey;
DROP INDEX IF EXISTS public.idx_snapshots_container_runtime_name;
DROP INDEX IF EXISTS public.idx_snapshots_container_created_at;
DROP INDEX IF EXISTS public.idx_container_versions_container_id;
DROP INDEX IF EXISTS public.idx_lifecycle_events_container_id;

-- ---------------------------------------------------------------------------
-- managed_computer_resource_limits
-- ---------------------------------------------------------------------------
-- Built fresh and filled from bot_workspace_resource_limits: the key column
-- changes meaning (bot -> workspace), so a copy is clearer than a rename.
CREATE TABLE IF NOT EXISTS public.managed_computer_resource_limits (
    workspace_id   UUID        PRIMARY KEY,
    team_id        UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                               REFERENCES public.teams(id) ON DELETE RESTRICT,
    cpu_millicores BIGINT      NOT NULL DEFAULT 0,
    memory_bytes   BIGINT      NOT NULL DEFAULT 0,
    storage_bytes  BIGINT      NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- md5('managed_computer_resource_limits:managed_computer_resource_limits_pkey')
    CONSTRAINT memoh_team_key_4256efe79722 UNIQUE (team_id, workspace_id),
    CONSTRAINT managed_computer_resource_limits_cpu_check CHECK (cpu_millicores >= 0),
    CONSTRAINT managed_computer_resource_limits_memory_check CHECK (memory_bytes >= 0),
    CONSTRAINT managed_computer_resource_limits_storage_check CHECK (storage_bytes >= 0)
);

ALTER TABLE public.managed_computer_resource_limits
    DROP CONSTRAINT IF EXISTS managed_computer_resource_limits_workspace_id_fkey;
ALTER TABLE public.managed_computer_resource_limits
    ADD CONSTRAINT managed_computer_resource_limits_workspace_id_fkey
    FOREIGN KEY (team_id, workspace_id)
    REFERENCES public.managed_computers(team_id, workspace_id) ON DELETE CASCADE
    NOT VALID;

ALTER TABLE public.managed_computer_resource_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_resource_limits FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS managed_computer_resource_limits_team_select ON public.managed_computer_resource_limits;
DROP POLICY IF EXISTS managed_computer_resource_limits_team_insert ON public.managed_computer_resource_limits;
DROP POLICY IF EXISTS managed_computer_resource_limits_team_update ON public.managed_computer_resource_limits;
DROP POLICY IF EXISTS managed_computer_resource_limits_team_delete ON public.managed_computer_resource_limits;

CREATE POLICY managed_computer_resource_limits_team_select ON public.managed_computer_resource_limits
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_resource_limits_team_insert ON public.managed_computer_resource_limits
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_resource_limits_team_update ON public.managed_computer_resource_limits
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_resource_limits_team_delete ON public.managed_computer_resource_limits
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- Backfill
-- ---------------------------------------------------------------------------
-- It spans every team: like other cross-team migrations, lift the
-- request-scoped policies (memoh.team_id is not set while migrating, and the
-- migration role need not have BYPASSRLS) and restore FORCE RLS before
-- committing.
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.containers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.containers DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspace_resource_limits NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspace_resource_limits DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_snapshots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_snapshots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_versions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_versions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_events DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_resource_limits NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_resource_limits DISABLE ROW LEVEL SECURITY;

-- The legacy tables are read through temp tables so the statements below do
-- not depend on their existence (a fresh install has none of them, or only
-- the empty shells 0092 / 0153 recreate). The whole backfill runs in one
-- block: sqlc derives its catalog from these files and only sees top-level
-- DDL.
DO $$
BEGIN
    IF to_regclass('public.containers') IS NOT NULL THEN
        CREATE TEMP TABLE _mc_containers ON COMMIT DROP AS
        SELECT team_id, bot_id, container_id, container_name, image, status, namespace, auto_start,
               container_path, last_started_at, last_stopped_at, created_at, updated_at
        FROM public.containers;
    ELSE
        CREATE TEMP TABLE _mc_containers (
            team_id uuid, bot_id uuid, container_id text, container_name text, image text, status text,
            namespace text, auto_start boolean, container_path text, last_started_at timestamptz,
            last_stopped_at timestamptz, created_at timestamptz, updated_at timestamptz
        ) ON COMMIT DROP;
    END IF;
    IF to_regclass('public.bot_workspaces') IS NOT NULL THEN
        CREATE TEMP TABLE _mc_slots ON COMMIT DROP AS
        SELECT bot_id, team_id, desired_state, desired_generation, image, preserve_data, observed_state,
               observed_generation, ever_ready, last_error, last_error_code, last_error_phase, attempts,
               next_attempt_at, lease_owner, lease_until, version, created_at, updated_at
        FROM public.bot_workspaces;
    ELSE
        CREATE TEMP TABLE _mc_slots (
            bot_id uuid, team_id uuid, desired_state text, desired_generation bigint, image text,
            preserve_data boolean, observed_state text, observed_generation bigint, ever_ready boolean,
            last_error text, last_error_code text, last_error_phase text, attempts integer,
            next_attempt_at timestamptz, lease_owner text, lease_until timestamptz, version bigint,
            created_at timestamptz, updated_at timestamptz
        ) ON COMMIT DROP;
    END IF;
    IF to_regclass('public.bot_workspace_resource_limits') IS NOT NULL THEN
        CREATE TEMP TABLE _mc_limits ON COMMIT DROP AS
        SELECT bot_id, team_id, cpu_millicores, memory_bytes, storage_bytes, created_at, updated_at
        FROM public.bot_workspace_resource_limits;
    ELSE
        CREATE TEMP TABLE _mc_limits (
            bot_id uuid, team_id uuid, cpu_millicores bigint, memory_bytes bigint, storage_bytes bigint,
            created_at timestamptz, updated_at timestamptz
        ) ON COMMIT DROP;
    END IF;

    -- A bot's managed computer: the workspace behind its first managed binding.
    CREATE TEMP TABLE _mc_bot_workspace ON COMMIT DROP AS
    SELECT DISTINCT ON (b.team_id, b.bot_id) b.team_id, b.bot_id, b.workspace_id
    FROM public.bot_workspace_bindings b
    JOIN public.workspaces w ON w.team_id = b.team_id AND w.id = b.workspace_id
    WHERE w.kind = 'managed'
    ORDER BY b.team_id, b.bot_id, b.created_at ASC, b.id ASC;

    -- The latest container record per bot carries the instance columns.
    CREATE TEMP TABLE _mc_latest_container ON COMMIT DROP AS
    SELECT DISTINCT ON (team_id, bot_id) *
    FROM _mc_containers
    ORDER BY team_id, bot_id, updated_at DESC;

    -- 1. One managed computer per workspace slot. The intent image falls back
    --    to the container's image so the merged row reports what runs.
    INSERT INTO public.managed_computers (
    workspace_id, team_id, desired_state, desired_generation, image, preserve_data,
    observed_state, observed_generation, ever_ready, last_error, last_error_code, last_error_phase,
    attempts, next_attempt_at, lease_owner, lease_until, version,
    instance_id, instance_name, instance_status, namespace, auto_start, container_path,
    last_started_at, last_stopped_at, created_at, updated_at
)
SELECT m.workspace_id, s.team_id, s.desired_state, s.desired_generation,
       COALESCE(NULLIF(s.image, ''), c.image, ''), s.preserve_data,
       s.observed_state, s.observed_generation, s.ever_ready, s.last_error, s.last_error_code, s.last_error_phase,
       s.attempts, s.next_attempt_at, s.lease_owner, s.lease_until, s.version,
       COALESCE(c.container_id, ''), COALESCE(c.container_name, ''), COALESCE(c.status, ''),
       COALESCE(c.namespace, 'default'), COALESCE(c.auto_start, true), COALESCE(c.container_path, '/data'),
       c.last_started_at, c.last_stopped_at, s.created_at, s.updated_at
    FROM _mc_slots s
    JOIN _mc_bot_workspace m ON m.team_id = s.team_id AND m.bot_id = s.bot_id
    LEFT JOIN _mc_latest_container c ON c.team_id = s.team_id AND c.bot_id = s.bot_id
    ON CONFLICT (workspace_id) DO NOTHING;

    -- 2. A bot with a container record but no slot (0153 never saw it):
    --    desired present, observed by the record's status, ever_ready so no
    --    automated step may delete its data, as 0153 ruled.
    INSERT INTO public.managed_computers (
    workspace_id, team_id, desired_state, desired_generation, image, preserve_data,
    observed_state, observed_generation, ever_ready, last_error, last_error_code, last_error_phase,
    attempts, next_attempt_at, lease_owner, lease_until, version,
    instance_id, instance_name, instance_status, namespace, auto_start, container_path,
    last_started_at, last_stopped_at, created_at, updated_at
)
SELECT m.workspace_id, c.team_id, 'present', 1, c.image, false,
       CASE WHEN c.status = 'running' THEN 'running' ELSE 'stopped' END, 1, true, '', '', '',
       0, now(), '', NULL, 1,
       c.container_id, c.container_name, c.status, c.namespace, c.auto_start, c.container_path,
       c.last_started_at, c.last_stopped_at, c.created_at, c.updated_at
    FROM _mc_latest_container c
    JOIN _mc_bot_workspace m ON m.team_id = c.team_id AND m.bot_id = c.bot_id
    WHERE NOT EXISTS (SELECT 1 FROM _mc_slots s WHERE s.team_id = c.team_id AND s.bot_id = c.bot_id)
    ON CONFLICT (workspace_id) DO NOTHING;

    -- 3. Snapshots, versions and events: container_id -> containers.bot_id
    --    -> the bot's managed workspace. The team is the container's.
    UPDATE public.managed_computer_snapshots s
       SET workspace_id = m.workspace_id, team_id = c.team_id
      FROM _mc_containers c
      JOIN _mc_bot_workspace m ON m.team_id = c.team_id AND m.bot_id = c.bot_id
     WHERE s.workspace_id IS NULL AND s.container_id = c.container_id;
    UPDATE public.managed_computer_versions v
       SET workspace_id = m.workspace_id, team_id = c.team_id
      FROM _mc_containers c
      JOIN _mc_bot_workspace m ON m.team_id = c.team_id AND m.bot_id = c.bot_id
     WHERE v.workspace_id IS NULL AND v.container_id = c.container_id;
    UPDATE public.managed_computer_events e
       SET workspace_id = m.workspace_id, team_id = c.team_id
      FROM _mc_containers c
      JOIN _mc_bot_workspace m ON m.team_id = c.team_id AND m.bot_id = c.bot_id
     WHERE e.workspace_id IS NULL AND e.container_id = c.container_id;

    -- Rows that reach no managed computer (their container record is gone,
    -- or the bot has no managed binding) are orphans and are deleted;
    -- versions go first because they RESTRICT the snapshot they point at.
    DELETE FROM public.managed_computer_versions  WHERE workspace_id IS NULL;
    DELETE FROM public.managed_computer_snapshots WHERE workspace_id IS NULL;
    DELETE FROM public.managed_computer_events    WHERE workspace_id IS NULL;

    -- 4. Resource limits follow the bot's managed computer; a bot without
    --    one has nothing to attach them to.
    INSERT INTO public.managed_computer_resource_limits (
        workspace_id, team_id, cpu_millicores, memory_bytes, storage_bytes, created_at, updated_at
    )
    SELECT m.workspace_id, l.team_id, l.cpu_millicores, l.memory_bytes, l.storage_bytes, l.created_at, l.updated_at
    FROM _mc_limits l
    JOIN _mc_bot_workspace m ON m.team_id = l.team_id AND m.bot_id = l.bot_id
    JOIN public.managed_computers mc ON mc.team_id = m.team_id AND mc.workspace_id = m.workspace_id
    ON CONFLICT (workspace_id) DO NOTHING;
END
$$;

ALTER TABLE public.managed_computer_snapshots ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE public.managed_computer_versions  ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE public.managed_computer_events    ALTER COLUMN workspace_id SET NOT NULL;

-- Keys on the new column. managed_computers is under FORCE ROW LEVEL
-- SECURITY once this commits; NOT VALID skips the policy-scoped validation.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_snapshots'::regclass
                      AND conname = 'managed_computer_snapshots_workspace_id_fkey') THEN
        ALTER TABLE public.managed_computer_snapshots
            ADD CONSTRAINT managed_computer_snapshots_workspace_id_fkey
            FOREIGN KEY (team_id, workspace_id)
            REFERENCES public.managed_computers(team_id, workspace_id) ON DELETE CASCADE
            NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_versions'::regclass
                      AND conname = 'managed_computer_versions_workspace_id_fkey') THEN
        ALTER TABLE public.managed_computer_versions
            ADD CONSTRAINT managed_computer_versions_workspace_id_fkey
            FOREIGN KEY (team_id, workspace_id)
            REFERENCES public.managed_computers(team_id, workspace_id) ON DELETE CASCADE
            NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_versions'::regclass
                      AND conname = 'managed_computer_versions_workspace_id_version_key') THEN
        -- Version numbers count per computer, not per container instance.
        ALTER TABLE public.managed_computer_versions
            ADD CONSTRAINT managed_computer_versions_workspace_id_version_key
            UNIQUE (team_id, workspace_id, version);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_events'::regclass
                      AND conname = 'managed_computer_events_workspace_id_fkey') THEN
        ALTER TABLE public.managed_computer_events
            ADD CONSTRAINT managed_computer_events_workspace_id_fkey
            FOREIGN KEY (team_id, workspace_id)
            REFERENCES public.managed_computers(team_id, workspace_id) ON DELETE CASCADE
            NOT VALID;
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_computer_snapshots_workspace_runtime_name
    ON public.managed_computer_snapshots (team_id, workspace_id, runtime_snapshot_name);
CREATE INDEX IF NOT EXISTS idx_managed_computer_snapshots_workspace_created_at
    ON public.managed_computer_snapshots (team_id, workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_managed_computer_snapshots_runtime_name
    ON public.managed_computer_snapshots (team_id, runtime_snapshot_name);
CREATE INDEX IF NOT EXISTS idx_managed_computer_versions_workspace_id
    ON public.managed_computer_versions (team_id, workspace_id);
CREATE INDEX IF NOT EXISTS idx_managed_computer_versions_snapshot_id
    ON public.managed_computer_versions (team_id, snapshot_id);
CREATE INDEX IF NOT EXISTS idx_managed_computer_events_workspace_id
    ON public.managed_computer_events (team_id, workspace_id);
CREATE INDEX IF NOT EXISTS idx_managed_computer_events_event_type
    ON public.managed_computer_events (team_id, event_type);

-- 5. The legacy tables are fully represented above.
DROP TABLE IF EXISTS public.containers;
DROP TABLE IF EXISTS public.bot_workspaces;
DROP TABLE IF EXISTS public.bot_workspace_resource_limits;

ALTER TABLE public.managed_computer_resource_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_resource_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_events FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

-- Policies of the renamed tables take the new table names.
DROP POLICY IF EXISTS snapshots_team_select ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS snapshots_team_insert ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS snapshots_team_update ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS snapshots_team_delete ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_select ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_insert ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_update ON public.managed_computer_snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_delete ON public.managed_computer_snapshots;
CREATE POLICY managed_computer_snapshots_team_select ON public.managed_computer_snapshots
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_snapshots_team_insert ON public.managed_computer_snapshots
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_snapshots_team_update ON public.managed_computer_snapshots
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_snapshots_team_delete ON public.managed_computer_snapshots
    FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS container_versions_team_select ON public.managed_computer_versions;
DROP POLICY IF EXISTS container_versions_team_insert ON public.managed_computer_versions;
DROP POLICY IF EXISTS container_versions_team_update ON public.managed_computer_versions;
DROP POLICY IF EXISTS container_versions_team_delete ON public.managed_computer_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_select ON public.managed_computer_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_insert ON public.managed_computer_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_update ON public.managed_computer_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_delete ON public.managed_computer_versions;
CREATE POLICY managed_computer_versions_team_select ON public.managed_computer_versions
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_versions_team_insert ON public.managed_computer_versions
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_versions_team_update ON public.managed_computer_versions
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_versions_team_delete ON public.managed_computer_versions
    FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS lifecycle_events_team_select ON public.managed_computer_events;
DROP POLICY IF EXISTS lifecycle_events_team_insert ON public.managed_computer_events;
DROP POLICY IF EXISTS lifecycle_events_team_update ON public.managed_computer_events;
DROP POLICY IF EXISTS lifecycle_events_team_delete ON public.managed_computer_events;
DROP POLICY IF EXISTS managed_computer_events_team_select ON public.managed_computer_events;
DROP POLICY IF EXISTS managed_computer_events_team_insert ON public.managed_computer_events;
DROP POLICY IF EXISTS managed_computer_events_team_update ON public.managed_computer_events;
DROP POLICY IF EXISTS managed_computer_events_team_delete ON public.managed_computer_events;
CREATE POLICY managed_computer_events_team_select ON public.managed_computer_events
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_events_team_insert ON public.managed_computer_events
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_events_team_update ON public.managed_computer_events
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_events_team_delete ON public.managed_computer_events
    FOR DELETE USING (team_id = public.memoh_current_team_id());

COMMIT;
