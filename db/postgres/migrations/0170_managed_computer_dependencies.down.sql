-- 0170_managed_computer_dependencies
-- Restore bot_dependency_installations in its pre-0170 shape: bot_id back
-- (the first bot bound to the computer, by binding created_at), the identity
-- key on (team_id, bot_id, dependency_id), workspace_id gone, and the table,
-- its constraints, its PostgreSQL 18 NOT NULL constraint names and its
-- policies under their old names.
--
-- Lost on the way down: records of a computer nothing is bound to (no bot can
-- own them again) are deleted, and a computer shared by several bots hands
-- all of its records to the first bound bot. The bots key comes back NOT
-- VALID because bots is under FORCE ROW LEVEL SECURITY.

BEGIN;

ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_dependencies NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_dependencies DISABLE ROW LEVEL SECURITY;

ALTER TABLE public.managed_computer_dependencies
    ADD COLUMN IF NOT EXISTS bot_id UUID;

DO $$
BEGIN
    -- The bot that owns a computer's records again: its first binding.
    CREATE TEMP TABLE IF NOT EXISTS _mcd_workspace_bot ON COMMIT DROP AS
    SELECT DISTINCT ON (b.team_id, b.workspace_id) b.team_id, b.workspace_id, b.bot_id
    FROM public.bot_workspace_bindings b
    ORDER BY b.team_id, b.workspace_id, b.created_at ASC, b.id ASC;

    UPDATE public.managed_computer_dependencies d
       SET bot_id = w.bot_id
      FROM _mcd_workspace_bot w
     WHERE d.bot_id IS NULL
       AND w.team_id = d.team_id AND w.workspace_id = d.workspace_id;

    DELETE FROM public.managed_computer_dependencies WHERE bot_id IS NULL;
END
$$;

ALTER TABLE public.managed_computer_dependencies
    ALTER COLUMN bot_id SET NOT NULL;
ALTER TABLE public.managed_computer_dependencies
    DROP CONSTRAINT IF EXISTS managed_computer_dependencies_workspace_id_fkey;
ALTER TABLE public.managed_computer_dependencies
    DROP CONSTRAINT IF EXISTS managed_computer_dependencies_identity_key;
ALTER TABLE public.managed_computer_dependencies
    DROP COLUMN IF EXISTS workspace_id;

DROP POLICY IF EXISTS managed_computer_dependencies_team_select ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_insert ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_update ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_delete ON public.managed_computer_dependencies;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('managed_computer_dependencies_pkey',                'bot_dependency_installations_pkey'),
            ('managed_computer_dependencies_team_id_fkey',        'bot_dependency_installations_team_id_fkey'),
            ('managed_computer_dependencies_dependency_id_check', 'bot_dependency_installations_dependency_id_check'),
            ('managed_computer_dependencies_source_check',        'bot_dependency_installations_source_check'),
            ('managed_computer_dependencies_status_check',        'bot_dependency_installations_status_check'),
            ('managed_computer_dependencies_operation_id_check',  'bot_dependency_installations_operation_id_check'),
            ('memoh_team_key_5ab3fe40cf41',                       'memoh_team_key_07a3be5666c2')
        ) AS v(old_name, new_name)
    LOOP
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_dependencies'::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_dependencies'::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.managed_computer_dependencies RENAME CONSTRAINT %I TO %I', r.old_name, r.new_name);
        END IF;
    END LOOP;
    -- PostgreSQL 18 NOT NULL constraint names follow the table back.
    FOR r IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'public.managed_computer_dependencies'::regclass
          AND c.contype = 'n' AND c.conname LIKE 'managed\_computer\_dependencies\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.managed_computer_dependencies RENAME CONSTRAINT %I TO %I',
            r.conname, 'bot_dependency_installations' || substr(r.conname, length('managed_computer_dependencies') + 1));
    END LOOP;
    IF to_regclass('public.bot_dependency_installations') IS NULL THEN
        ALTER TABLE public.managed_computer_dependencies RENAME TO bot_dependency_installations;
    END IF;
END
$$;

ALTER TABLE public.bot_dependency_installations
    DROP CONSTRAINT IF EXISTS bot_dependency_installations_identity_key;
ALTER TABLE public.bot_dependency_installations
    ADD CONSTRAINT bot_dependency_installations_identity_key
    UNIQUE (team_id, bot_id, dependency_id);
-- bots is under FORCE ROW LEVEL SECURITY; NOT VALID skips the policy-scoped
-- validation scan, as 0148 did.
ALTER TABLE public.bot_dependency_installations
    DROP CONSTRAINT IF EXISTS bot_dependency_installations_bot_id_fkey;
ALTER TABLE public.bot_dependency_installations
    ADD CONSTRAINT bot_dependency_installations_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;

ALTER TABLE public.bot_dependency_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_dependency_installations FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_dependency_installations_team_select ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_insert ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_update ON public.bot_dependency_installations;
DROP POLICY IF EXISTS bot_dependency_installations_team_delete ON public.bot_dependency_installations;
CREATE POLICY bot_dependency_installations_team_select ON public.bot_dependency_installations
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_insert ON public.bot_dependency_installations
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_update ON public.bot_dependency_installations
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_dependency_installations_team_delete ON public.bot_dependency_installations
    FOR DELETE USING (team_id = public.memoh_current_team_id());

COMMIT;
