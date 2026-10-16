-- 0169_user_computers_and_workspace_targets
-- Restore the pre-0169 shape: user_computers back to user_runtimes (with its
-- constraint, index and policy names), bot_workdirs with target_kind +
-- remote_binding_id in their original column positions (the table is
-- rebuilt for that: dropped columns cannot be re-inserted in the middle),
-- the text workspace_target_id on tool_approval_requests /
-- user_input_requests converted back in place, and the workspace_id key
-- stripped from the session target snapshots.
--
-- Lost on the way down: approval / user-input requests and workdirs whose
-- workspace no longer resolves to a target the old model can name (a user
-- workspace the bot is no longer bound to, a deleted workspace) come back
-- with an empty target or are dropped; the managed binding keeps the copied
-- tool approval policy (bots.tool_approval_config was never cleared); the
-- workspace rows created for computers that lacked one stay. The rebuilt
-- bot_workdirs keys come back NOT VALID (bots, team_members, teams and
-- bot_workspace_bindings are under FORCE ROW LEVEL SECURITY).

BEGIN;

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

DO $$
BEGIN
    -- The old address of a (bot, workspace) pair: "native" for the managed
    -- computer, the binding id for a user workspace.
    CREATE TEMP TABLE IF NOT EXISTS _wt_targets ON COMMIT DROP AS
    SELECT b.team_id, b.bot_id, b.workspace_id, w.kind,
           CASE WHEN w.kind = 'managed' THEN 'native' ELSE b.id::text END AS target_id,
           CASE WHEN w.kind = 'managed' THEN NULL ELSE b.id END AS binding_id
    FROM public.bot_workspace_bindings b
    JOIN public.workspaces w ON w.team_id = b.team_id AND w.id = b.workspace_id;

    -- 1. Session target snapshots drop the server-side key.
    UPDATE public.bot_sessions
       SET metadata = metadata #- '{workspace_target,workspace_id}'
     WHERE jsonb_typeof(metadata -> 'workspace_target') = 'object'
       AND metadata -> 'workspace_target' ? 'workspace_id';

    -- 2. tool_approval_requests / user_input_requests: workspace_id -> text
    --    workspace_target_id, in place so the column keeps its position.
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'tool_approval_requests' AND column_name = 'workspace_id') THEN
        ALTER TABLE public.tool_approval_requests ADD COLUMN workspace_target_backfill TEXT NOT NULL DEFAULT '';
        UPDATE public.tool_approval_requests r
           SET workspace_target_backfill = t.target_id
          FROM _wt_targets t
         WHERE r.workspace_id IS NOT NULL
           AND t.team_id = r.team_id AND t.bot_id = r.bot_id AND t.workspace_id = r.workspace_id;
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_id TYPE text USING workspace_target_backfill;
        ALTER TABLE public.tool_approval_requests RENAME COLUMN workspace_id TO workspace_target_id;
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_target_id SET DEFAULT '';
        ALTER TABLE public.tool_approval_requests ALTER COLUMN workspace_target_id SET NOT NULL;
        ALTER TABLE public.tool_approval_requests DROP COLUMN workspace_target_backfill;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'user_input_requests' AND column_name = 'workspace_id') THEN
        ALTER TABLE public.user_input_requests ADD COLUMN workspace_target_backfill TEXT NOT NULL DEFAULT '';
        UPDATE public.user_input_requests r
           SET workspace_target_backfill = t.target_id
          FROM _wt_targets t
         WHERE r.workspace_id IS NOT NULL
           AND t.team_id = r.team_id AND t.bot_id = r.bot_id AND t.workspace_id = r.workspace_id;
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_id TYPE text USING workspace_target_backfill;
        ALTER TABLE public.user_input_requests RENAME COLUMN workspace_id TO workspace_target_id;
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_target_id SET DEFAULT '';
        ALTER TABLE public.user_input_requests ALTER COLUMN workspace_target_id SET NOT NULL;
        ALTER TABLE public.user_input_requests DROP COLUMN workspace_target_backfill;
    END IF;

    -- 3. bot_workdirs is rebuilt in its pre-0169 column order. The
    --    referencing keys of bot_sessions and schedule are re-created below.
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'bot_workdirs' AND column_name = 'workspace_id') THEN
        CREATE TABLE public.bot_workdirs_restore (
            id                 UUID        NOT NULL DEFAULT gen_random_uuid(),
            team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id(),
            bot_id             UUID        NOT NULL,
            name               TEXT        NOT NULL,
            target_kind        TEXT        NOT NULL,
            remote_binding_id  UUID,
            path               TEXT        NOT NULL,
            created_by_user_id UUID,
            archived_at        TIMESTAMPTZ,
            created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
            updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
        );
        INSERT INTO public.bot_workdirs_restore
            (id, team_id, bot_id, name, target_kind, remote_binding_id, path, created_by_user_id, archived_at, created_at, updated_at)
        SELECT d.id, d.team_id, d.bot_id, d.name,
               CASE WHEN t.kind = 'managed' THEN 'native' ELSE 'remote' END,
               t.binding_id, d.path, d.created_by_user_id, d.archived_at, d.created_at, d.updated_at
          FROM public.bot_workdirs d
          JOIN _wt_targets t ON t.team_id = d.team_id AND t.bot_id = d.bot_id AND t.workspace_id = d.workspace_id;
        ALTER TABLE public.bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_workdir_id_fkey;
        ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_workdir_id_fkey;
        DROP TABLE public.bot_workdirs;
        ALTER TABLE public.bot_workdirs_restore RENAME TO bot_workdirs;
    END IF;
END
$$;

-- bot_workdirs keys, indexes and policies under their original names.
DO $$
DECLARE
    r record;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_pkey') THEN
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT bot_workdirs_pkey PRIMARY KEY (id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_team_key') THEN
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT bot_workdirs_team_key UNIQUE (team_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'memoh_team_key_f16b6908008c') THEN
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT memoh_team_key_f16b6908008c UNIQUE (team_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_team_id_fkey') THEN
        -- teams is under FORCE ROW LEVEL SECURITY: NOT VALID skips the
        -- validation scan a migration role without BYPASSRLS cannot run.
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT bot_workdirs_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
            NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_target_kind_check') THEN
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT bot_workdirs_target_kind_check
            CHECK (target_kind IN ('native', 'remote'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.bot_workdirs'::regclass AND conname = 'bot_workdirs_binding_check') THEN
        ALTER TABLE public.bot_workdirs ADD CONSTRAINT bot_workdirs_binding_check
            CHECK ((target_kind = 'remote') = (remote_binding_id IS NOT NULL));
    END IF;
    -- PostgreSQL 18 NOT NULL constraint names follow the table back.
    FOR r IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'public.bot_workdirs'::regclass
          AND c.contype = 'n' AND c.conname LIKE 'bot\_workdirs\_restore\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.bot_workdirs RENAME CONSTRAINT %I TO %I',
            r.conname, 'bot_workdirs' || substr(r.conname, length('bot_workdirs_restore') + 1));
    END LOOP;
END
$$;

-- bots, team_members and bot_workspace_bindings are under FORCE ROW LEVEL
-- SECURITY; NOT VALID skips the policy-scoped validation scan.
ALTER TABLE public.bot_workdirs DROP CONSTRAINT IF EXISTS bot_workdirs_bot_id_fkey;
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;
ALTER TABLE public.bot_workdirs DROP CONSTRAINT IF EXISTS bot_workdirs_created_by_user_id_fkey;
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id)
    NOT VALID;
ALTER TABLE public.bot_workdirs DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_remote_binding_fkey
    FOREIGN KEY (team_id, remote_binding_id)
    REFERENCES public.bot_workspace_bindings(team_id, id) ON DELETE CASCADE
    NOT VALID;

DROP INDEX IF EXISTS public.bot_workdirs_target_path_unique;
CREATE UNIQUE INDEX IF NOT EXISTS bot_workdirs_target_path_unique
    ON public.bot_workdirs (
        team_id, bot_id,
        COALESCE(remote_binding_id, '00000000-0000-0000-0000-000000000000'::uuid),
        path
    ) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bot_workdirs_bot
    ON public.bot_workdirs (team_id, bot_id, created_at DESC)
    WHERE archived_at IS NULL;

ALTER TABLE public.bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_workdir_id_fkey;
ALTER TABLE public.bot_sessions
    ADD CONSTRAINT bot_sessions_workdir_id_fkey
    FOREIGN KEY (team_id, workdir_id)
    REFERENCES public.bot_workdirs(team_id, id) ON DELETE SET NULL (workdir_id)
    NOT VALID;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_workdir_id_fkey;
ALTER TABLE public.schedule
    ADD CONSTRAINT schedule_workdir_id_fkey
    FOREIGN KEY (team_id, workdir_id)
    REFERENCES public.bot_workdirs(team_id, id) ON DELETE SET NULL (workdir_id)
    NOT VALID;

ALTER TABLE public.bot_workdirs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workdirs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS bot_workdirs_team_select ON public.bot_workdirs;
DROP POLICY IF EXISTS bot_workdirs_team_insert ON public.bot_workdirs;
DROP POLICY IF EXISTS bot_workdirs_team_update ON public.bot_workdirs;
DROP POLICY IF EXISTS bot_workdirs_team_delete ON public.bot_workdirs;
CREATE POLICY bot_workdirs_team_select ON public.bot_workdirs
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workdirs_team_insert ON public.bot_workdirs
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workdirs_team_update ON public.bot_workdirs
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workdirs_team_delete ON public.bot_workdirs
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- ---------------------------------------------------------------------------
-- user_computers -> user_runtimes
-- ---------------------------------------------------------------------------
ALTER TABLE public.user_computers DROP CONSTRAINT IF EXISTS user_computers_workspace_fkey;

DROP POLICY IF EXISTS user_computers_team_select ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_insert ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_update ON public.user_computers;
DROP POLICY IF EXISTS user_computers_team_delete ON public.user_computers;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('user_computers_pkey',                   'user_runtimes_pkey'),
            ('user_computers_team_id_fkey',           'user_runtimes_team_id_fkey'),
            ('user_computers_user_id_fkey',           'user_runtimes_user_id_fkey'),
            ('user_computers_api_token_key',          'user_runtimes_api_token_key'),
            ('user_computers_name_check',             'user_runtimes_name_check'),
            ('user_computers_activation_state_check', 'user_runtimes_activation_state_check'),
            ('memoh_team_key_ead4597a0f2b',           'memoh_team_key_eeb71d700aa6')
        ) AS v(old_name, new_name)
    LOOP
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.user_computers'::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.user_computers'::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.user_computers RENAME CONSTRAINT %I TO %I', r.old_name, r.new_name);
        END IF;
    END LOOP;
    FOR r IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'public.user_computers'::regclass
          AND c.contype = 'n' AND c.conname LIKE 'user\_computers\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.user_computers RENAME CONSTRAINT %I TO %I',
            r.conname, 'user_runtimes' || substr(r.conname, length('user_computers') + 1));
    END LOOP;
    IF to_regclass('public.user_runtimes') IS NULL THEN
        ALTER TABLE public.user_computers RENAME TO user_runtimes;
    END IF;
END
$$;

ALTER INDEX IF EXISTS public.idx_user_computers_active_user_name RENAME TO idx_user_runtimes_active_user_name;
ALTER INDEX IF EXISTS public.idx_user_computers_user_id          RENAME TO idx_user_runtimes_user_id;

ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS user_runtimes_team_select ON public.user_runtimes;
DROP POLICY IF EXISTS user_runtimes_team_insert ON public.user_runtimes;
DROP POLICY IF EXISTS user_runtimes_team_update ON public.user_runtimes;
DROP POLICY IF EXISTS user_runtimes_team_delete ON public.user_runtimes;
CREATE POLICY user_runtimes_team_select ON public.user_runtimes
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY user_runtimes_team_insert ON public.user_runtimes
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY user_runtimes_team_update ON public.user_runtimes
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY user_runtimes_team_delete ON public.user_runtimes
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_input_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.tool_approval_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

COMMIT;
