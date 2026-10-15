-- 0167_workspaces_and_bindings
-- A Bot no longer owns exactly one computer. `workspaces` records every
-- computer a team knows about: the managed workspace the server provisions
-- for a bot (kind 'managed') and the user's own machines paired through
-- Remote Runtime (kind 'user', one row per user_runtimes row sharing its id).
-- `bot_workspace_bindings` records which bots may use which workspace and
-- which binding is the bot's primary target. It replaces
-- bot_remote_runtime_bindings: every remote mount is copied with its id, so
-- bot_workdirs.remote_binding_id keeps resolving, and every managed workspace
-- gets a managed binding that is primary unless a remote mount already was.
--
-- Backfill decision: every bot with a bot_workspaces row OR a containers row
-- gets a managed workspace whose id is the bot id, including bots whose
-- desired state is 'absent'. In this stage the bot_workspaces row *is* the
-- bot's managed workspace slot, so a one-to-one mapping is the least
-- surprising outcome; the design note that proposed skipping absent bots
-- without a container is deliberately not followed here.
--
-- Nothing user-visible changes: HTTP routes and payloads stay the same, and
-- the managed binding's tool_approval_config keeps the column default (the
-- bot's native approval policy is not copied yet).

BEGIN;

CREATE TABLE IF NOT EXISTS public.workspaces (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    owner_user_id UUID        NOT NULL,
    kind          TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    metadata      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Team-prefixed identity key, named the way 0112 derives it
    -- (md5('workspaces:workspaces_pkey')) so a fresh install and this upgrade
    -- end with one identical helper key.
    CONSTRAINT memoh_team_key_78c83a5def92 UNIQUE (team_id, id),
    CONSTRAINT workspaces_kind_check CHECK (kind IN ('managed', 'user')),
    CONSTRAINT workspaces_name_check CHECK (btrim(name) <> ''),
    -- Same shape as bots_owner_user_id_fkey: the owner is a team membership.
    CONSTRAINT workspaces_owner_user_id_fkey
        FOREIGN KEY (team_id, owner_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workspaces_owner
    ON public.workspaces (team_id, owner_user_id, kind);

ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS workspaces_team_select ON public.workspaces;
DROP POLICY IF EXISTS workspaces_team_insert ON public.workspaces;
DROP POLICY IF EXISTS workspaces_team_update ON public.workspaces;
DROP POLICY IF EXISTS workspaces_team_delete ON public.workspaces;

CREATE POLICY workspaces_team_select ON public.workspaces
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY workspaces_team_insert ON public.workspaces
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY workspaces_team_update ON public.workspaces
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY workspaces_team_delete ON public.workspaces
    FOR DELETE USING (team_id = public.memoh_current_team_id());

CREATE TABLE IF NOT EXISTS public.bot_workspace_bindings (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id              UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                     REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id               UUID        NOT NULL,
    workspace_id         UUID        NOT NULL,
    is_primary           BOOLEAN     NOT NULL DEFAULT false,
    tool_approval_config JSONB       NOT NULL DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"allow","bypass_commands":[],"force_review_commands":[]}}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- md5('bot_workspace_bindings:bot_workspace_bindings_pkey'); see above.
    CONSTRAINT memoh_team_key_3bd860ed9422 UNIQUE (team_id, id),
    CONSTRAINT bot_workspace_bindings_bot_workspace_key
        UNIQUE (team_id, bot_id, workspace_id)
);

-- bots and workspaces are under FORCE ROW LEVEL SECURITY; add the references
-- NOT VALID so the constraints are never validated through the policy-scoped
-- scan. New rows are still checked.
ALTER TABLE public.bot_workspace_bindings
    DROP CONSTRAINT IF EXISTS bot_workspace_bindings_bot_id_fkey;
ALTER TABLE public.bot_workspace_bindings
    ADD CONSTRAINT bot_workspace_bindings_bot_id_fkey
    FOREIGN KEY (team_id, bot_id)
    REFERENCES public.bots(team_id, id) ON DELETE CASCADE
    NOT VALID;
ALTER TABLE public.bot_workspace_bindings
    DROP CONSTRAINT IF EXISTS bot_workspace_bindings_workspace_id_fkey;
-- A bound workspace cannot be deleted, but the check waits for the end of
-- the transaction: removing a team membership cascades to the member's bots
-- (and so to these bindings) and to their workspaces in one statement, and
-- an immediate RESTRICT would fire before the bindings are gone, in an order
-- that depends on constraint creation order.
ALTER TABLE public.bot_workspace_bindings
    ADD CONSTRAINT bot_workspace_bindings_workspace_id_fkey
    FOREIGN KEY (team_id, workspace_id)
    REFERENCES public.workspaces(team_id, id) ON DELETE NO ACTION
    DEFERRABLE INITIALLY DEFERRED
    NOT VALID;

-- One primary binding per bot.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_workspace_bindings_primary
    ON public.bot_workspace_bindings (team_id, bot_id)
    WHERE is_primary;
CREATE INDEX IF NOT EXISTS idx_bot_workspace_bindings_workspace
    ON public.bot_workspace_bindings (team_id, workspace_id);

ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_workspace_bindings_team_select ON public.bot_workspace_bindings;
DROP POLICY IF EXISTS bot_workspace_bindings_team_insert ON public.bot_workspace_bindings;
DROP POLICY IF EXISTS bot_workspace_bindings_team_update ON public.bot_workspace_bindings;
DROP POLICY IF EXISTS bot_workspace_bindings_team_delete ON public.bot_workspace_bindings;

CREATE POLICY bot_workspace_bindings_team_select ON public.bot_workspace_bindings
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_bindings_team_insert ON public.bot_workspace_bindings
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_bindings_team_update ON public.bot_workspace_bindings
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_workspace_bindings_team_delete ON public.bot_workspace_bindings
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Backfill. It spans every team: like other cross-team migrations, lift the
-- request-scoped policies (memoh.team_id is not set while migrating, and the
-- migration role need not have BYPASSRLS) and restore FORCE RLS before
-- committing. bot_remote_runtime_bindings is referenced IF EXISTS so a re-run
-- after the drop below stays harmless.
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;

-- 1. Managed workspaces: one per bot that has a workspace slot or a container
--    record. The workspace keeps the bot id so the slot and the computer stay
--    one-to-one for this stage. Bots created from now on get a fresh id.
INSERT INTO public.workspaces (id, team_id, owner_user_id, kind, name, created_at, updated_at)
SELECT b.id, b.team_id, b.owner_user_id, 'managed', b.name, b.created_at, b.updated_at
FROM public.bots b
WHERE EXISTS (SELECT 1 FROM public.bot_workspaces w WHERE w.bot_id = b.id)
   OR EXISTS (SELECT 1 FROM public.containers c WHERE c.bot_id = b.id)
ON CONFLICT (id) DO NOTHING;

-- 2. User workspaces: one per paired computer, revoked ones included so the
--    history behind their old mounts stays resolvable.
INSERT INTO public.workspaces (id, team_id, owner_user_id, kind, name, created_at, updated_at)
SELECT r.id, r.team_id, r.user_id, 'user', r.name, r.created_at, r.updated_at
FROM public.user_runtimes r
ON CONFLICT (id) DO NOTHING;

-- 3. Remote mounts become bindings to the runtime's user workspace. Ids are
--    kept so bot_workdirs.remote_binding_id and client-held target ids stay
--    valid.
DO $$
BEGIN
    IF to_regclass('public.bot_remote_runtime_bindings') IS NOT NULL THEN
        INSERT INTO public.bot_workspace_bindings
            (id, team_id, bot_id, workspace_id, is_primary, tool_approval_config, created_at, updated_at)
        SELECT m.id, m.team_id, m.bot_id, m.runtime_id, m.is_primary, m.tool_approval_config,
               m.created_at, m.updated_at
        FROM public.bot_remote_runtime_bindings m
        JOIN public.workspaces w ON w.team_id = m.team_id AND w.id = m.runtime_id
        ON CONFLICT (id) DO NOTHING;
    END IF;
END
$$;

-- 4. Every managed workspace gets its binding. It is primary exactly when no
--    remote mount already is, which is what "native is primary" meant before.
INSERT INTO public.bot_workspace_bindings (team_id, bot_id, workspace_id, is_primary)
SELECT w.team_id, w.id, w.id,
       NOT EXISTS (
           SELECT 1 FROM public.bot_workspace_bindings p
           WHERE p.team_id = w.team_id AND p.bot_id = w.id AND p.is_primary
       )
FROM public.workspaces w
WHERE w.kind = 'managed'
ON CONFLICT (team_id, bot_id, workspace_id) DO NOTHING;

-- The binding -> workspace key is deferred; run its checks now so the ALTER
-- TABLE statements below do not meet pending trigger events.
SET CONSTRAINTS ALL IMMEDIATE;

ALTER TABLE public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_remote_runtime_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;
ALTER TABLE public.containers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.containers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;

-- 5. Remote workdirs now hang off the new binding row (same id, same
--    cascade). The constraint keeps its name.
ALTER TABLE public.bot_workdirs
    DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_remote_binding_fkey
    FOREIGN KEY (team_id, remote_binding_id)
    REFERENCES public.bot_workspace_bindings(team_id, id) ON DELETE CASCADE
    NOT VALID;

-- 6. The old mount table is fully represented above.
DROP TABLE IF EXISTS public.bot_remote_runtime_bindings;

COMMIT;
