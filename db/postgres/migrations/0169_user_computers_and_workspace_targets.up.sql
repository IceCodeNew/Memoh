-- 0169_user_computers_and_workspace_targets
-- Every workspace target is persisted by its workspace id. The client-facing
-- addresses ("native" for the bot's managed computer, the binding id for a
-- remote mount) now exist only at the service boundary.
--
-- * user_runtimes becomes user_computers: the credential record of a user's
--   own computer, keyed by the workspace it shares its id with. The table is
--   renamed in place (rows keep their ids and the physical column order);
--   constraints, indexes and policies take the fresh-install names, and the
--   row now references its workspace.
-- * bot_workdirs: target_kind + remote_binding_id -> workspace_id. The slot
--   of target_kind is reused for workspace_id so a fresh install and this
--   upgrade end with the same physical column order (sqlc scans SELECT *
--   positionally). A workdir hangs off the bot's binding to that workspace
--   ((team_id, bot_id, workspace_id) is the binding's natural key), so
--   unbinding still cascades to the workdirs, as it did for remote mounts.
--   Native workdirs of a bot without a managed workspace cannot be attached
--   anywhere and are deleted; remote workdirs always resolve (their binding
--   key was enforced).
-- * tool_approval_requests / user_input_requests: workspace_target_id (text)
--   -> workspace_id (uuid, nullable). The column is converted in place so it
--   keeps its position. "native" and an empty target map to the bot's
--   managed workspace, a binding id to its workspace; a target that resolves
--   to nothing (binding gone, bot without managed computer) stays NULL.
-- * bot_sessions.metadata: the workspace_target snapshot gains workspace_id.
--   The client-facing workspace_target_id / workspace_target.target_id keep
--   their addresses (the web client restores the composer selection from
--   them); the server compares and resolves by workspace_id from here on.
-- * The managed binding's tool_approval_config is copied from
--   bots.tool_approval_config: the native approval policy is read from the
--   binding from now on. bots.tool_approval_config stays for one version and
--   is written alongside.
--
-- Mapping rule, as in 0168: a bot's managed computer is the workspace behind
-- its first managed binding (by binding created_at).
--
-- Idempotency: a fresh install already has the final shape from 0001, so the
-- legacy steps are guarded by catalog checks inside DO blocks (sqlc derives
-- its catalog from these files and only sees top-level DDL, which here is a
-- no-op against the final shape).

BEGIN;

-- ---------------------------------------------------------------------------
-- user_runtimes -> user_computers
-- ---------------------------------------------------------------------------
-- On a fresh install 0001 already declares user_computers and the legacy 0107
-- recreates an empty user_runtimes shell (the way 0092 / 0153 do for the
-- tables 0168 merged); the shell is dropped before its index names could
-- collide with the renames below.
DO $$
BEGIN
    IF to_regclass('public.user_runtimes') IS NOT NULL THEN
        IF to_regclass('public.user_computers') IS NULL THEN
            ALTER TABLE public.user_runtimes RENAME TO user_computers;
        ELSE
            DROP TABLE public.user_runtimes;
        END IF;
    END IF;
END
$$;
DROP TABLE IF EXISTS public.user_runtimes;

-- Constraints that survive the rename get the names a fresh install gives
-- them (the 0112 helper key is md5('user_computers:user_computers_pkey')).
DO $$
DECLARE
    r record;
BEGIN
    IF to_regclass('public.user_computers') IS NULL THEN
        RETURN;
    END IF;
    FOR r IN
        SELECT * FROM (VALUES
            ('user_runtimes_pkey',                   'user_computers_pkey'),
            ('user_runtimes_team_id_fkey',           'user_computers_team_id_fkey'),
            ('user_runtimes_user_id_fkey',           'user_computers_user_id_fkey'),
            ('user_runtimes_api_token_key',          'user_computers_api_token_key'),
            ('user_runtimes_name_check',             'user_computers_name_check'),
            ('user_runtimes_activation_state_check', 'user_computers_activation_state_check'),
            ('memoh_team_key_eeb71d700aa6',          'memoh_team_key_ead4597a0f2b')
        ) AS v(old_name, new_name)
    LOOP
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.user_computers'::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.user_computers'::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.user_computers RENAME CONSTRAINT %I TO %I', r.old_name, r.new_name);
        END IF;
    END LOOP;
    -- PostgreSQL 18 records NOT NULL as named constraints that a rename
    -- leaves behind; older servers have no such rows and skip this.
    FOR r IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'public.user_computers'::regclass
          AND c.contype = 'n' AND c.conname LIKE 'user\_runtimes\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.user_computers RENAME CONSTRAINT %I TO %I',
            r.conname, 'user_computers' || substr(r.conname, length('user_runtimes') + 1));
    END LOOP;
END
$$;

ALTER INDEX IF EXISTS public.idx_user_runtimes_active_user_name RENAME TO idx_user_computers_active_user_name;
ALTER INDEX IF EXISTS public.idx_user_runtimes_user_id          RENAME TO idx_user_computers_user_id;

DROP POLICY IF EXISTS user_runtimes_team_select ON public.user_computers;
DROP POLICY IF EXISTS user_runtimes_team_insert ON public.user_computers;
DROP POLICY IF EXISTS user_runtimes_team_update ON public.user_computers;
DROP POLICY IF EXISTS user_runtimes_team_delete ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_select ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_insert ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_update ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_delete ON public.user_computers;
CREATE POLICY user_computers_team_select ON public.user_computers
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY user_computers_team_insert ON public.user_computers
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY user_computers_team_update ON public.user_computers
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY user_computers_team_delete ON public.user_computers
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- Backfill. It spans every team: like other cross-team migrations, lift the
-- request-scoped policies (memoh.team_id is not set while migrating, and the
-- migration role need not have BYPASSRLS) and restore FORCE RLS before
-- committing.
-- ---------------------------------------------------------------------------
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_computers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_computers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workdirs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workdirs DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;

-- 1. Every paired computer has its user workspace (0167 created them for the
--    rows of its time; a row paired without the workspace hook gets one now)
--    so the reference below holds for every row.
INSERT INTO public.workspaces (id, team_id, owner_user_id, kind, name, created_at, updated_at)
SELECT r.id, r.team_id, r.user_id, 'user', r.name, r.created_at, r.updated_at
FROM public.user_computers r
WHERE NOT EXISTS (SELECT 1 FROM public.workspaces w WHERE w.team_id = r.team_id AND w.id = r.id)
ON CONFLICT (id) DO NOTHING;

-- A computer record is its workspace's credential: it goes with the
-- workspace. The check waits for the end of the transaction: pairing inserts
-- the credential first and creates its workspace through the same
-- transaction's attach hook. workspaces is under FORCE ROW LEVEL SECURITY;
-- NOT VALID skips the policy-scoped validation scan. New rows are still
-- checked.
ALTER TABLE public.user_computers
    DROP CONSTRAINT IF EXISTS user_computers_workspace_fkey;
ALTER TABLE public.user_computers
    ADD CONSTRAINT user_computers_workspace_fkey
    FOREIGN KEY (team_id, id)
    REFERENCES public.workspaces(team_id, id) ON DELETE CASCADE
    DEFERRABLE INITIALLY DEFERRED
    NOT VALID;

-- 2. Workspace targets. The whole conversion runs in one block: the legacy
--    columns are gone from the final shape, so nothing here may be top-level.
DO $$
DECLARE
    uuid_pattern constant text := '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$';
BEGIN
    -- A bot's managed computer: the workspace behind its first managed binding.
    CREATE TEMP TABLE IF NOT EXISTS _wt_managed ON COMMIT DROP AS
    SELECT DISTINCT ON (b.team_id, b.bot_id) b.team_id, b.bot_id, b.workspace_id
    FROM public.bot_workspace_bindings b
    JOIN public.workspaces w ON w.team_id = b.team_id AND w.id = b.workspace_id
    WHERE w.kind = 'managed'
    ORDER BY b.team_id, b.bot_id, b.created_at ASC, b.id ASC;

    -- 2a. bot_workdirs
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'bot_workdirs' AND column_name = 'target_kind') THEN
        ALTER TABLE public.bot_workdirs ADD COLUMN workspace_id_backfill UUID;
        UPDATE public.bot_workdirs d
           SET workspace_id_backfill = b.workspace_id
          FROM public.bot_workspace_bindings b
         WHERE d.target_kind = 'remote'
           AND b.team_id = d.team_id AND b.id = d.remote_binding_id;
        UPDATE public.bot_workdirs d
           SET workspace_id_backfill = m.workspace_id
          FROM _wt_managed m
         WHERE d.target_kind = 'native'
           AND m.team_id = d.team_id AND m.bot_id = d.bot_id;
        -- Native workdirs of a bot without a managed workspace, and remote
        -- workdirs whose binding belongs to another bot, have no binding to
        -- hang off: deleted (bound sessions lose the pin through the SET
        -- NULL key, as they do when a mount is removed).
        DELETE FROM public.bot_workdirs d
         WHERE d.workspace_id_backfill IS NULL
            OR NOT EXISTS (SELECT 1 FROM public.bot_workspace_bindings b
                            WHERE b.team_id = d.team_id AND b.bot_id = d.bot_id
                              AND b.workspace_id = d.workspace_id_backfill);
        DROP INDEX IF EXISTS public.bot_workdirs_target_path_unique;
        ALTER TABLE public.bot_workdirs
            DROP CONSTRAINT IF EXISTS bot_workdirs_target_kind_check,
            DROP CONSTRAINT IF EXISTS bot_workdirs_binding_check,
            DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;
        -- target_kind's slot becomes workspace_id (NOT NULL carries over:
        -- every remaining row was mapped above).
        ALTER TABLE public.bot_workdirs ALTER COLUMN target_kind TYPE uuid USING workspace_id_backfill;
        ALTER TABLE public.bot_workdirs RENAME COLUMN target_kind TO workspace_id;
        ALTER TABLE public.bot_workdirs DROP COLUMN remote_binding_id;
        ALTER TABLE public.bot_workdirs DROP COLUMN workspace_id_backfill;
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_target_kind_not_null') THEN
            ALTER TABLE public.bot_workdirs RENAME CONSTRAINT bot_workdirs_target_kind_not_null TO bot_workdirs_workspace_id_not_null;
        END IF;
    END IF;

    -- 2b. tool_approval_requests / user_input_requests. Converted only while
    --     workspace_id is still missing: on a fresh install 0001 declares it
    --     and the legacy 0110 re-adds the text column next to it, which the
    --     top-level DROP COLUMN below removes.
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'tool_approval_requests' AND column_name = 'workspace_target_id')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'tool_approval_requests' AND column_name = 'workspace_id') THEN
        ALTER TABLE public.tool_approval_requests ADD COLUMN workspace_id_backfill UUID;
        UPDATE public.tool_approval_requests r
           SET workspace_id_backfill = m.workspace_id
          FROM _wt_managed m
         WHERE btrim(r.workspace_target_id) IN ('', 'native')
           AND m.team_id = r.team_id AND m.bot_id = r.bot_id;
        UPDATE public.tool_approval_requests r
           SET workspace_id_backfill = b.workspace_id
          FROM public.bot_workspace_bindings b
         WHERE btrim(r.workspace_target_id) ~ uuid_pattern
           AND b.team_id = r.team_id AND b.id = btrim(r.workspace_target_id)::uuid;
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_target_id DROP DEFAULT;
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_target_id DROP NOT NULL;
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_target_id TYPE uuid USING workspace_id_backfill;
        ALTER TABLE public.tool_approval_requests RENAME COLUMN workspace_target_id TO workspace_id;
        ALTER TABLE public.tool_approval_requests DROP COLUMN workspace_id_backfill;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'user_input_requests' AND column_name = 'workspace_target_id')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'user_input_requests' AND column_name = 'workspace_id') THEN
        ALTER TABLE public.user_input_requests ADD COLUMN workspace_id_backfill UUID;
        UPDATE public.user_input_requests r
           SET workspace_id_backfill = m.workspace_id
          FROM _wt_managed m
         WHERE btrim(r.workspace_target_id) IN ('', 'native')
           AND m.team_id = r.team_id AND m.bot_id = r.bot_id;
        UPDATE public.user_input_requests r
           SET workspace_id_backfill = b.workspace_id
          FROM public.bot_workspace_bindings b
         WHERE btrim(r.workspace_target_id) ~ uuid_pattern
           AND b.team_id = r.team_id AND b.id = btrim(r.workspace_target_id)::uuid;
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_target_id DROP DEFAULT;
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_target_id DROP NOT NULL;
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_target_id TYPE uuid USING workspace_id_backfill;
        ALTER TABLE public.user_input_requests RENAME COLUMN workspace_target_id TO workspace_id;
        ALTER TABLE public.user_input_requests DROP COLUMN workspace_id_backfill;
    END IF;

    -- 2c. bot_sessions.metadata.workspace_target gains workspace_id. The
    --     client-facing target_id / workspace_target_id stay as they are.
    UPDATE public.bot_sessions s
       SET metadata = jsonb_set(s.metadata, '{workspace_target,workspace_id}', to_jsonb(m.workspace_id::text), true)
      FROM _wt_managed m
     WHERE jsonb_typeof(s.metadata -> 'workspace_target') = 'object'
       AND NOT (s.metadata -> 'workspace_target' ? 'workspace_id')
       AND btrim(COALESCE(s.metadata -> 'workspace_target' ->> 'target_id', '')) = 'native'
       AND m.team_id = s.team_id AND m.bot_id = s.bot_id;
    UPDATE public.bot_sessions s
       SET metadata = jsonb_set(s.metadata, '{workspace_target,workspace_id}', to_jsonb(b.workspace_id::text), true)
      FROM public.bot_workspace_bindings b
     WHERE jsonb_typeof(s.metadata -> 'workspace_target') = 'object'
       AND NOT (s.metadata -> 'workspace_target' ? 'workspace_id')
       AND btrim(COALESCE(s.metadata -> 'workspace_target' ->> 'target_id', '')) ~ uuid_pattern
       AND b.team_id = s.team_id
       AND b.id = btrim(s.metadata -> 'workspace_target' ->> 'target_id')::uuid;

    -- 3. The native approval policy moves to the managed binding.
    UPDATE public.bot_workspace_bindings b
       SET tool_approval_config = bot.tool_approval_config
      FROM _wt_managed m
      JOIN public.bots bot ON bot.team_id = m.team_id AND bot.id = m.bot_id
     WHERE b.team_id = m.team_id AND b.bot_id = m.bot_id AND b.workspace_id = m.workspace_id
       AND bot.tool_approval_config IS NOT NULL
       AND b.tool_approval_config IS DISTINCT FROM bot.tool_approval_config;
END
$$;

-- Legacy shells a fresh install still carries at this point, all empty: 0001
-- keeps remote_binding_id as a slot for the 0129 / 0167 statements that name
-- it, and 0110 re-adds the text target columns next to 0001's workspace_id.
-- They go here, at top level, so sqlc's catalog loses them too. On the
-- upgrade path the block above already converted them away.
ALTER TABLE public.bot_workdirs
    DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;
ALTER TABLE public.bot_workdirs
    DROP COLUMN IF EXISTS remote_binding_id;
ALTER TABLE public.tool_approval_requests
    DROP COLUMN IF EXISTS workspace_target_id;
ALTER TABLE public.user_input_requests
    DROP COLUMN IF EXISTS workspace_target_id;

-- A workdir hangs off the bot's binding to its workspace; unbinding cascades
-- to the workdirs. bot_workspace_bindings is under FORCE ROW LEVEL SECURITY
-- once this commits; NOT VALID skips the policy-scoped validation scan.
ALTER TABLE public.bot_workdirs
    DROP CONSTRAINT IF EXISTS bot_workdirs_binding_fkey;
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_binding_fkey
    FOREIGN KEY (team_id, bot_id, workspace_id)
    REFERENCES public.bot_workspace_bindings(team_id, bot_id, workspace_id) ON DELETE CASCADE
    NOT VALID;

-- One live workdir per directory per workspace, under the index's 0129 name
-- (the legacy definition was dropped with target_kind above).
CREATE UNIQUE INDEX IF NOT EXISTS bot_workdirs_target_path_unique
    ON public.bot_workdirs (team_id, bot_id, workspace_id, path)
    WHERE archived_at IS NULL;

ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workdirs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workdirs FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_computers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

COMMIT;
