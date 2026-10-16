-- 0170_managed_computer_dependencies
-- A dependency installation record belongs to the managed computer the
-- software is installed on, not to the bot that asked for it.
-- bot_dependency_installations becomes managed_computer_dependencies, keyed
-- by (team_id, workspace_id, dependency_id) and referencing managed_computers;
-- the bot_id column goes away. The table is renamed in place so rows keep
-- their ids and every other column (last_error_code, operation_id, the
-- publication columns) keeps its value; constraints, indexes, policies and
-- the PostgreSQL 18 NOT NULL constraint names take the fresh-install names
-- (the team helper key is md5('<table>:<table>_pkey') the way 0112 derives
-- them).
--
-- Mapping rule, as in 0168 / 0169: a bot's managed computer is the workspace
-- behind its first managed binding (by binding created_at). Bots created
-- after 0167 have a managed workspace id that differs from the bot id, so
-- nothing here assumes the two are equal.
--
-- Deleted on the way up: rows of a bot without a managed computer, and rows
-- whose computer has no managed_computers record (the new key points there;
-- a record for software on a computer that was never provisioned describes
-- nothing). When several bots share one computer and recorded the same
-- dependency, the row of the first bound bot wins and the others go.
--
-- Nothing user-visible changes: /bots/{bot_id}/dependencies keeps its routes
-- and payloads; the service resolves the bot to its computer at the boundary.
--
-- Idempotency: a fresh install already has managed_computer_dependencies from
-- 0001, while the legacy 0148 re-creates an empty bot_dependency_installations
-- shell next to it (0152 / 0164 then shape that shell). The shell is dropped
-- and every legacy step is guarded by catalog checks inside DO blocks; sqlc
-- derives its catalog from the top-level statements, which here are no-ops
-- against the final shape.

BEGIN;

-- ---------------------------------------------------------------------------
-- bot_dependency_installations -> managed_computer_dependencies
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF to_regclass('public.bot_dependency_installations') IS NOT NULL THEN
        IF to_regclass('public.managed_computer_dependencies') IS NULL THEN
            ALTER TABLE public.bot_dependency_installations RENAME TO managed_computer_dependencies;
        ELSE
            DROP TABLE public.bot_dependency_installations;
        END IF;
    END IF;
END
$$;
DROP TABLE IF EXISTS public.bot_dependency_installations;

ALTER TABLE public.managed_computer_dependencies
    ADD COLUMN IF NOT EXISTS workspace_id UUID;

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
ALTER TABLE public.managed_computers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_dependencies NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_dependencies DISABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    -- Only the renamed legacy table still carries bot_id; a fresh install
    -- never enters this block.
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = 'public' AND table_name = 'managed_computer_dependencies' AND column_name = 'bot_id') THEN
        RETURN;
    END IF;

    -- A bot's managed computer: the workspace behind its first managed binding.
    CREATE TEMP TABLE IF NOT EXISTS _mcd_managed ON COMMIT DROP AS
    SELECT DISTINCT ON (b.team_id, b.bot_id) b.team_id, b.bot_id, b.workspace_id
    FROM public.bot_workspace_bindings b
    JOIN public.workspaces w ON w.team_id = b.team_id AND w.id = b.workspace_id
    WHERE w.kind = 'managed'
    ORDER BY b.team_id, b.bot_id, b.created_at ASC, b.id ASC;

    UPDATE public.managed_computer_dependencies d
       SET workspace_id = m.workspace_id
      FROM _mcd_managed m
     WHERE d.workspace_id IS NULL
       AND m.team_id = d.team_id AND m.bot_id = d.bot_id;

    -- Rows that cannot hang off a managed computer: the bot has none, or the
    -- computer has no record for the new key to reference.
    DELETE FROM public.managed_computer_dependencies d
     WHERE d.workspace_id IS NULL
        OR NOT EXISTS (SELECT 1 FROM public.managed_computers mc
                        WHERE mc.team_id = d.team_id AND mc.workspace_id = d.workspace_id);

    -- Several bots on one computer that recorded the same dependency: the
    -- row of the first bound bot (by binding created_at) wins.
    DELETE FROM public.managed_computer_dependencies d
     USING (
        SELECT d.id,
               row_number() OVER (PARTITION BY d.team_id, d.workspace_id, d.dependency_id
                                  ORDER BY b.created_at ASC, b.id ASC, d.created_at ASC, d.id ASC) AS rn
          FROM public.managed_computer_dependencies d
          JOIN public.bot_workspace_bindings b
            ON b.team_id = d.team_id AND b.bot_id = d.bot_id AND b.workspace_id = d.workspace_id
     ) ranked
     WHERE ranked.id = d.id AND ranked.rn > 1;

    ALTER TABLE public.managed_computer_dependencies
        DROP CONSTRAINT IF EXISTS bot_dependency_installations_bot_id_fkey;
    ALTER TABLE public.managed_computer_dependencies
        DROP CONSTRAINT IF EXISTS bot_dependency_installations_identity_key;
    ALTER TABLE public.managed_computer_dependencies DROP COLUMN bot_id;
END
$$;

-- Every remaining row was mapped above; a fresh install declares the column
-- NOT NULL already.
ALTER TABLE public.managed_computer_dependencies
    ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE public.managed_computer_dependencies
    DROP COLUMN IF EXISTS bot_id;

-- Constraints that survive the rename get the names a fresh install gives
-- them.
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('bot_dependency_installations_pkey',                'managed_computer_dependencies_pkey'),
            ('bot_dependency_installations_team_id_fkey',        'managed_computer_dependencies_team_id_fkey'),
            ('bot_dependency_installations_dependency_id_check', 'managed_computer_dependencies_dependency_id_check'),
            ('bot_dependency_installations_source_check',        'managed_computer_dependencies_source_check'),
            ('bot_dependency_installations_status_check',        'managed_computer_dependencies_status_check'),
            ('bot_dependency_installations_operation_id_check',  'managed_computer_dependencies_operation_id_check'),
            ('memoh_team_key_07a3be5666c2',                      'memoh_team_key_5ab3fe40cf41')
        ) AS v(old_name, new_name)
    LOOP
        IF EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_dependencies'::regclass AND conname = r.old_name)
           AND NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'public.managed_computer_dependencies'::regclass AND conname = r.new_name) THEN
            EXECUTE format('ALTER TABLE public.managed_computer_dependencies RENAME CONSTRAINT %I TO %I', r.old_name, r.new_name);
        END IF;
    END LOOP;
    -- PostgreSQL 18 records NOT NULL as named constraints that a rename
    -- leaves behind; older servers have no such rows and skip this.
    FOR r IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'public.managed_computer_dependencies'::regclass
          AND c.contype = 'n' AND c.conname LIKE 'bot\_dependency\_installations\_%'
    LOOP
        EXECUTE format('ALTER TABLE public.managed_computer_dependencies RENAME CONSTRAINT %I TO %I',
            r.conname, 'managed_computer_dependencies' || substr(r.conname, length('bot_dependency_installations') + 1));
    END LOOP;
END
$$;

-- The identity key now names the computer. The record references the
-- computer's row; managed_computers is under FORCE ROW LEVEL SECURITY once
-- this commits, so the key is NOT VALID (new rows are still checked).
ALTER TABLE public.managed_computer_dependencies
    DROP CONSTRAINT IF EXISTS managed_computer_dependencies_identity_key;
ALTER TABLE public.managed_computer_dependencies
    ADD CONSTRAINT managed_computer_dependencies_identity_key
    UNIQUE (team_id, workspace_id, dependency_id);
ALTER TABLE public.managed_computer_dependencies
    DROP CONSTRAINT IF EXISTS managed_computer_dependencies_workspace_id_fkey;
ALTER TABLE public.managed_computer_dependencies
    ADD CONSTRAINT managed_computer_dependencies_workspace_id_fkey
    FOREIGN KEY (team_id, workspace_id)
    REFERENCES public.managed_computers(team_id, workspace_id) ON DELETE CASCADE
    NOT VALID;

-- ---------------------------------------------------------------------------
-- Tenant isolation back on, policies under the new name
-- ---------------------------------------------------------------------------
ALTER TABLE public.managed_computer_dependencies ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computer_dependencies FORCE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.managed_computers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_dependency_installations_team_select ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS bot_dependency_installations_team_insert ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS bot_dependency_installations_team_update ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS bot_dependency_installations_team_delete ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_select ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_insert ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_update ON public.managed_computer_dependencies;
DROP POLICY IF EXISTS managed_computer_dependencies_team_delete ON public.managed_computer_dependencies;
CREATE POLICY managed_computer_dependencies_team_select ON public.managed_computer_dependencies
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_dependencies_team_insert ON public.managed_computer_dependencies
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_dependencies_team_update ON public.managed_computer_dependencies
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY managed_computer_dependencies_team_delete ON public.managed_computer_dependencies
    FOR DELETE USING (team_id = public.memoh_current_team_id());

COMMIT;
