-- 0168_managed_computers
-- Restore containers, bot_workspaces and bot_workspace_resource_limits in
-- their pre-0168 shape, hang snapshots / versions / events back off
-- containers(container_id) and drop managed_computers.
--
-- Lost on the way down: managed computers nothing is bound to (no bot can
-- own their rows again), the rows of a workspace bound to several bots keep
-- only the first bot (by binding created_at), and snapshot / version / event
-- rows whose container_id is not the computer's current instance. The
-- containers -> bots and bot_workspaces -> bots keys come back NOT VALID
-- because bots is under FORCE ROW LEVEL SECURITY.

BEGIN;

-- ---------------------------------------------------------------------------
-- containers
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.containers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL,
  container_id TEXT NOT NULL,
  container_name TEXT NOT NULL,
  image TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  namespace TEXT NOT NULL DEFAULT 'default',
  auto_start BOOLEAN NOT NULL DEFAULT true,
  container_path TEXT NOT NULL DEFAULT '/data',
  workspace_backend TEXT NOT NULL DEFAULT 'container',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_started_at TIMESTAMPTZ,
  last_stopped_at TIMESTAMPTZ,
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  CONSTRAINT containers_container_id_unique UNIQUE (team_id, container_id),
  CONSTRAINT containers_container_name_unique UNIQUE (team_id, container_name),
  CONSTRAINT memoh_team_key_a179a87cbe03 UNIQUE (team_id, id),
  CONSTRAINT containers_team_id_fkey
    FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);
ALTER TABLE public.containers DROP CONSTRAINT IF EXISTS containers_bot_id_fkey;
ALTER TABLE public.containers
  ADD CONSTRAINT containers_bot_id_fkey
  FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE
  NOT VALID;
CREATE INDEX IF NOT EXISTS idx_containers_bot_id ON public.containers (team_id, bot_id);

-- ---------------------------------------------------------------------------
-- bot_workspaces
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.bot_workspaces (
    bot_id              UUID        PRIMARY KEY,
    team_id             UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                    REFERENCES public.teams(id) ON DELETE RESTRICT,
    desired_state       TEXT        NOT NULL,
    desired_generation  BIGINT      NOT NULL DEFAULT 1,
    image               TEXT        NOT NULL DEFAULT '',
    preserve_data       BOOLEAN     NOT NULL DEFAULT false,
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
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memoh_team_key_7443107a7495 UNIQUE (team_id, bot_id),
    CONSTRAINT bot_workspaces_desired_state_check
        CHECK (desired_state IN ('present', 'absent')),
    CONSTRAINT bot_workspaces_observed_state_check
        CHECK (observed_state IN ('absent', 'provisioning', 'running', 'stopped', 'failed', 'removing')),
    CONSTRAINT bot_workspaces_last_error_phase_check
        CHECK (last_error_phase IN ('', 'image_prepare', 'start', 'bridge', 'bootstrap', 'teardown'))
);
ALTER TABLE public.bot_workspaces DROP CONSTRAINT IF EXISTS bot_workspaces_bot_id_fkey;
ALTER TABLE public.bot_workspaces
    ADD CONSTRAINT bot_workspaces_bot_id_fkey
    FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;
CREATE INDEX IF NOT EXISTS idx_bot_workspaces_due
    ON public.bot_workspaces (team_id, next_attempt_at);

-- ---------------------------------------------------------------------------
-- bot_workspace_resource_limits
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.bot_workspace_resource_limits (
  bot_id UUID PRIMARY KEY,
  cpu_millicores BIGINT NOT NULL DEFAULT 0,
  memory_bytes BIGINT NOT NULL DEFAULT 0,
  storage_bytes BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  CONSTRAINT bot_workspace_resource_limits_cpu_check CHECK (cpu_millicores >= 0),
  CONSTRAINT bot_workspace_resource_limits_memory_check CHECK (memory_bytes >= 0),
  CONSTRAINT bot_workspace_resource_limits_storage_check CHECK (storage_bytes >= 0),
  CONSTRAINT memoh_team_key_1ea7647190ff UNIQUE (team_id, bot_id),
  CONSTRAINT bot_workspace_resource_limits_team_id_fkey
    FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);
ALTER TABLE public.bot_workspace_resource_limits DROP CONSTRAINT IF EXISTS bot_workspace_resource_limits_bot_id_fkey;
ALTER TABLE public.bot_workspace_resource_limits
  ADD CONSTRAINT bot_workspace_resource_limits_bot_id_fkey
  FOREIGN KEY (team_id, bot_id) REFERENCES public.bots(team_id, id) ON DELETE CASCADE
  NOT VALID;

-- ---------------------------------------------------------------------------
-- Backfill (cross-team: lift the request-scoped policies while copying)
-- ---------------------------------------------------------------------------
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computers DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_snapshots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_snapshots DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_versions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_versions DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_events DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_resource_limits NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.managed_computer_resource_limits DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_resource_limits NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_resource_limits DISABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    -- The bot that owns a workspace again: its first binding.
    CREATE TEMP TABLE _mc_workspace_bot ON COMMIT DROP AS
    SELECT DISTINCT ON (b.team_id, b.workspace_id) b.team_id, b.workspace_id, b.bot_id
    FROM public.bot_workspace_bindings b
    ORDER BY b.team_id, b.workspace_id, b.created_at ASC, b.id ASC;

    IF to_regclass('public.managed_computers') IS NOT NULL THEN
        INSERT INTO public.bot_workspaces (
            bot_id, team_id, desired_state, desired_generation, image, preserve_data,
            observed_state, observed_generation, ever_ready, last_error, last_error_code, last_error_phase,
            attempts, next_attempt_at, lease_owner, lease_until, version, created_at, updated_at
        )
        SELECT m.bot_id, mc.team_id, mc.desired_state, mc.desired_generation, mc.image, mc.preserve_data,
               mc.observed_state, mc.observed_generation, mc.ever_ready, mc.last_error, mc.last_error_code,
               mc.last_error_phase, mc.attempts, mc.next_attempt_at, mc.lease_owner, mc.lease_until,
               mc.version, mc.created_at, mc.updated_at
        FROM public.managed_computers mc
        JOIN _mc_workspace_bot m ON m.team_id = mc.team_id AND m.workspace_id = mc.workspace_id
        ON CONFLICT (bot_id) DO NOTHING;

        INSERT INTO public.containers (
            bot_id, container_id, container_name, image, status, namespace, auto_start, container_path,
            workspace_backend, created_at, updated_at, last_started_at, last_stopped_at, team_id
        )
        SELECT m.bot_id, mc.instance_id, mc.instance_name, mc.image,
               CASE WHEN mc.instance_status = '' THEN 'created' ELSE mc.instance_status END,
               mc.namespace, mc.auto_start, mc.container_path, 'container',
               mc.created_at, mc.updated_at, mc.last_started_at, mc.last_stopped_at, mc.team_id
        FROM public.managed_computers mc
        JOIN _mc_workspace_bot m ON m.team_id = mc.team_id AND m.workspace_id = mc.workspace_id
        WHERE mc.instance_id <> ''
        ON CONFLICT (team_id, container_id) DO NOTHING;
    END IF;

    IF to_regclass('public.managed_computer_resource_limits') IS NOT NULL THEN
        INSERT INTO public.bot_workspace_resource_limits (
            bot_id, cpu_millicores, memory_bytes, storage_bytes, created_at, updated_at, team_id
        )
        SELECT m.bot_id, l.cpu_millicores, l.memory_bytes, l.storage_bytes, l.created_at, l.updated_at, l.team_id
        FROM public.managed_computer_resource_limits l
        JOIN _mc_workspace_bot m ON m.team_id = l.team_id AND m.workspace_id = l.workspace_id
        ON CONFLICT (bot_id) DO NOTHING;
    END IF;
END
$$;

DROP TABLE IF EXISTS public.managed_computer_resource_limits;

-- ---------------------------------------------------------------------------
-- managed_computer_* -> snapshots / container_versions / lifecycle_events
-- ---------------------------------------------------------------------------
ALTER TABLE IF EXISTS public.managed_computer_snapshots DROP CONSTRAINT IF EXISTS managed_computer_snapshots_workspace_id_fkey;
ALTER TABLE IF EXISTS public.managed_computer_versions  DROP CONSTRAINT IF EXISTS managed_computer_versions_workspace_id_fkey;
ALTER TABLE IF EXISTS public.managed_computer_versions  DROP CONSTRAINT IF EXISTS managed_computer_versions_workspace_id_version_key;
ALTER TABLE IF EXISTS public.managed_computer_events    DROP CONSTRAINT IF EXISTS managed_computer_events_workspace_id_fkey;
DROP INDEX IF EXISTS public.idx_managed_computer_snapshots_workspace_runtime_name;
DROP INDEX IF EXISTS public.idx_managed_computer_snapshots_workspace_created_at;
DROP INDEX IF EXISTS public.idx_managed_computer_versions_workspace_id;
DROP INDEX IF EXISTS public.idx_managed_computer_events_workspace_id;

-- Rows whose container record was not restored above have no parent in
-- containers any more; versions go first because they RESTRICT snapshots.
DELETE FROM public.managed_computer_versions v
 WHERE NOT EXISTS (SELECT 1 FROM public.containers c
                    WHERE c.team_id = v.team_id AND c.container_id = v.container_id);
DELETE FROM public.managed_computer_snapshots s
 WHERE NOT EXISTS (SELECT 1 FROM public.containers c
                    WHERE c.team_id = s.team_id AND c.container_id = s.container_id);
DELETE FROM public.managed_computer_events e
 WHERE NOT EXISTS (SELECT 1 FROM public.containers c
                    WHERE c.team_id = e.team_id AND c.container_id = e.container_id);

ALTER TABLE IF EXISTS public.managed_computer_snapshots DROP COLUMN IF EXISTS workspace_id;
ALTER TABLE IF EXISTS public.managed_computer_versions  DROP COLUMN IF EXISTS workspace_id;
ALTER TABLE IF EXISTS public.managed_computer_events    DROP COLUMN IF EXISTS workspace_id;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('managed_computer_snapshots', 'managed_computer_snapshots_pkey',            'snapshots_pkey'),
            ('managed_computer_snapshots', 'managed_computer_snapshots_team_id_fkey',    'snapshots_team_id_fkey'),
            ('managed_computer_snapshots', 'memoh_team_key_78dc0d778ed1',                'memoh_team_key_6bceec3b7fb5'),
            ('managed_computer_versions',  'managed_computer_versions_pkey',             'container_versions_pkey'),
            ('managed_computer_versions',  'managed_computer_versions_team_id_fkey',     'container_versions_team_id_fkey'),
            ('managed_computer_versions',  'managed_computer_versions_snapshot_id_fkey', 'container_versions_snapshot_id_fkey'),
            ('managed_computer_versions',  'memoh_team_key_71818849b2d9',                'memoh_team_key_4e4a7126f607'),
            ('managed_computer_events',    'managed_computer_events_pkey',               'lifecycle_events_pkey'),
            ('managed_computer_events',    'managed_computer_events_team_id_fkey',       'lifecycle_events_team_id_fkey'),
            ('managed_computer_events',    'memoh_team_key_2e51805b8402',                'memoh_team_key_38bce59cf848')
        ) AS v(tbl, old_name, new_name)
    LOOP
        IF to_regclass('public.' || quote_ident(r.tbl)) IS NOT NULL
           AND EXISTS (SELECT 1 FROM pg_constraint
                        WHERE conrelid = ('public.' || quote_ident(r.tbl))::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                        WHERE conrelid = ('public.' || quote_ident(r.tbl))::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I', r.tbl, r.old_name, r.new_name);
        END IF;
    END LOOP;

    -- PostgreSQL 18 NOT NULL constraint names follow the table back.
    FOR r IN
        SELECT v.tbl, v.old_prefix, c.conname
        FROM (VALUES
            ('managed_computer_snapshots', 'snapshots'),
            ('managed_computer_versions',  'container_versions'),
            ('managed_computer_events',    'lifecycle_events')
        ) AS v(tbl, old_prefix)
        JOIN pg_constraint c ON c.conrelid = ('public.' || quote_ident(v.tbl))::regclass
        WHERE to_regclass('public.' || quote_ident(v.tbl)) IS NOT NULL
          AND c.contype = 'n' AND c.conname LIKE v.tbl || '\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I',
            r.tbl, r.conname, r.old_prefix || substr(r.conname, length(r.tbl) + 1));
    END LOOP;

    IF to_regclass('public.managed_computer_snapshots') IS NOT NULL
       AND to_regclass('public.snapshots') IS NULL THEN
        ALTER TABLE public.managed_computer_snapshots RENAME TO snapshots;
    END IF;
    IF to_regclass('public.managed_computer_versions') IS NOT NULL
       AND to_regclass('public.container_versions') IS NULL THEN
        ALTER TABLE public.managed_computer_versions RENAME TO container_versions;
    END IF;
    IF to_regclass('public.managed_computer_events') IS NOT NULL
       AND to_regclass('public.lifecycle_events') IS NULL THEN
        ALTER TABLE public.managed_computer_events RENAME TO lifecycle_events;
    END IF;
END
$$;

ALTER INDEX IF EXISTS public.idx_managed_computer_snapshots_runtime_name RENAME TO idx_snapshots_runtime_name;
ALTER INDEX IF EXISTS public.idx_managed_computer_versions_snapshot_id   RENAME TO idx_container_versions_snapshot_id;
ALTER INDEX IF EXISTS public.idx_managed_computer_events_event_type      RENAME TO idx_lifecycle_events_event_type;

-- Back on the instance key. RLS is still lifted here, so the references are
-- validated as they originally were.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.snapshots'::regclass AND conname = 'snapshots_container_id_fkey') THEN
        ALTER TABLE public.snapshots
            ADD CONSTRAINT snapshots_container_id_fkey
            FOREIGN KEY (team_id, container_id)
            REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.container_versions'::regclass AND conname = 'container_versions_container_id_fkey') THEN
        ALTER TABLE public.container_versions
            ADD CONSTRAINT container_versions_container_id_fkey
            FOREIGN KEY (team_id, container_id)
            REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.container_versions'::regclass AND conname = 'container_versions_container_id_version_key') THEN
        ALTER TABLE public.container_versions
            ADD CONSTRAINT container_versions_container_id_version_key
            UNIQUE (team_id, container_id, version);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.lifecycle_events'::regclass AND conname = 'lifecycle_events_container_id_fkey') THEN
        ALTER TABLE public.lifecycle_events
            ADD CONSTRAINT lifecycle_events_container_id_fkey
            FOREIGN KEY (team_id, container_id)
            REFERENCES public.containers(team_id, container_id) ON DELETE CASCADE;
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_snapshots_container_runtime_name
    ON public.snapshots (team_id, container_id, runtime_snapshot_name);
CREATE INDEX IF NOT EXISTS idx_snapshots_container_created_at
    ON public.snapshots (team_id, container_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_container_versions_container_id
    ON public.container_versions (team_id, container_id);
CREATE INDEX IF NOT EXISTS idx_lifecycle_events_container_id
    ON public.lifecycle_events (team_id, container_id);

DROP TABLE IF EXISTS public.managed_computers;

-- ---------------------------------------------------------------------------
-- Tenant isolation back on
-- ---------------------------------------------------------------------------
ALTER TABLE public.bot_workspace_resource_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_resource_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.lifecycle_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.lifecycle_events FORCE ROW LEVEL SECURITY;
ALTER TABLE public.container_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.container_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS containers_team_select ON public.containers;
DROP POLICY IF EXISTS containers_team_insert ON public.containers;
DROP POLICY IF EXISTS containers_team_update ON public.containers;
DROP POLICY IF EXISTS containers_team_delete ON public.containers;
CREATE POLICY containers_team_select ON public.containers
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY containers_team_insert ON public.containers
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY containers_team_update ON public.containers
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY containers_team_delete ON public.containers
  FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS bot_workspaces_team_select ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_insert ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_update ON public.bot_workspaces;
DROP POLICY IF EXISTS bot_workspaces_team_delete ON public.bot_workspaces;
CREATE POLICY bot_workspaces_team_select ON public.bot_workspaces
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_insert ON public.bot_workspaces
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_update ON public.bot_workspaces
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspaces_team_delete ON public.bot_workspaces
    FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS bot_workspace_resource_limits_team_select ON public.bot_workspace_resource_limits;
DROP POLICY IF EXISTS bot_workspace_resource_limits_team_insert ON public.bot_workspace_resource_limits;
DROP POLICY IF EXISTS bot_workspace_resource_limits_team_update ON public.bot_workspace_resource_limits;
DROP POLICY IF EXISTS bot_workspace_resource_limits_team_delete ON public.bot_workspace_resource_limits;
CREATE POLICY bot_workspace_resource_limits_team_select ON public.bot_workspace_resource_limits
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_resource_limits_team_insert ON public.bot_workspace_resource_limits
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_resource_limits_team_update ON public.bot_workspace_resource_limits
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_resource_limits_team_delete ON public.bot_workspace_resource_limits
  FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS managed_computer_snapshots_team_select ON public.snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_insert ON public.snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_update ON public.snapshots;
DROP POLICY IF EXISTS managed_computer_snapshots_team_delete ON public.snapshots;
DROP POLICY IF EXISTS snapshots_team_select ON public.snapshots;
DROP POLICY IF EXISTS snapshots_team_insert ON public.snapshots;
DROP POLICY IF EXISTS snapshots_team_update ON public.snapshots;
DROP POLICY IF EXISTS snapshots_team_delete ON public.snapshots;
CREATE POLICY snapshots_team_select ON public.snapshots
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY snapshots_team_insert ON public.snapshots
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY snapshots_team_update ON public.snapshots
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY snapshots_team_delete ON public.snapshots
  FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS managed_computer_versions_team_select ON public.container_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_insert ON public.container_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_update ON public.container_versions;
DROP POLICY IF EXISTS managed_computer_versions_team_delete ON public.container_versions;
DROP POLICY IF EXISTS container_versions_team_select ON public.container_versions;
DROP POLICY IF EXISTS container_versions_team_insert ON public.container_versions;
DROP POLICY IF EXISTS container_versions_team_update ON public.container_versions;
DROP POLICY IF EXISTS container_versions_team_delete ON public.container_versions;
CREATE POLICY container_versions_team_select ON public.container_versions
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY container_versions_team_insert ON public.container_versions
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY container_versions_team_update ON public.container_versions
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY container_versions_team_delete ON public.container_versions
  FOR DELETE USING (team_id = public.memoh_current_team_id());

DROP POLICY IF EXISTS managed_computer_events_team_select ON public.lifecycle_events;
DROP POLICY IF EXISTS managed_computer_events_team_insert ON public.lifecycle_events;
DROP POLICY IF EXISTS managed_computer_events_team_update ON public.lifecycle_events;
DROP POLICY IF EXISTS managed_computer_events_team_delete ON public.lifecycle_events;
DROP POLICY IF EXISTS lifecycle_events_team_select ON public.lifecycle_events;
DROP POLICY IF EXISTS lifecycle_events_team_insert ON public.lifecycle_events;
DROP POLICY IF EXISTS lifecycle_events_team_update ON public.lifecycle_events;
DROP POLICY IF EXISTS lifecycle_events_team_delete ON public.lifecycle_events;
CREATE POLICY lifecycle_events_team_select ON public.lifecycle_events
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY lifecycle_events_team_insert ON public.lifecycle_events
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY lifecycle_events_team_update ON public.lifecycle_events
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY lifecycle_events_team_delete ON public.lifecycle_events
  FOR DELETE USING (team_id = public.memoh_current_team_id());

COMMIT;
