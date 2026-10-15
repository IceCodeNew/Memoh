-- 0167_workspaces_and_bindings
-- Restore bot_remote_runtime_bindings in its pre-0167 shape (team_id last,
-- composite keys, FORCE RLS) from the bindings that point at user workspaces,
-- point bot_workdirs back at it and drop the two new tables.
--
-- Lost on the way down: managed workspaces and their bindings (the native
-- target is implicit again), workspaces.metadata, and any binding to a user
-- workspace whose user_runtimes row no longer exists. Remote mount ids,
-- primaries and tool approval choices survive.

BEGIN;

CREATE TABLE IF NOT EXISTS public.bot_remote_runtime_bindings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL,
  runtime_id UUID NOT NULL,
  is_primary BOOLEAN NOT NULL DEFAULT false,
  tool_approval_config JSONB NOT NULL DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"allow","bypass_commands":[],"force_review_commands":[]}}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  team_id UUID NOT NULL DEFAULT public.memoh_current_team_id(),
  CONSTRAINT bot_remote_runtime_bindings_bot_id_runtime_id_key UNIQUE (team_id, bot_id, runtime_id),
  CONSTRAINT memoh_team_key_fc924c120c12 UNIQUE (team_id, id),
  CONSTRAINT bot_remote_runtime_bindings_team_id_fkey
    FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT
);

-- bots and user_runtimes are under FORCE ROW LEVEL SECURITY; NOT VALID skips
-- the policy-scoped validation scan. New rows are still checked.
ALTER TABLE public.bot_remote_runtime_bindings
  DROP CONSTRAINT IF EXISTS bot_remote_runtime_bindings_bot_id_fkey;
ALTER TABLE public.bot_remote_runtime_bindings
  ADD CONSTRAINT bot_remote_runtime_bindings_bot_id_fkey
  FOREIGN KEY (team_id, bot_id)
  REFERENCES public.bots(team_id, id) ON DELETE CASCADE
  NOT VALID;
ALTER TABLE public.bot_remote_runtime_bindings
  DROP CONSTRAINT IF EXISTS bot_remote_runtime_bindings_runtime_id_fkey;
ALTER TABLE public.bot_remote_runtime_bindings
  ADD CONSTRAINT bot_remote_runtime_bindings_runtime_id_fkey
  FOREIGN KEY (team_id, runtime_id)
  REFERENCES public.user_runtimes(team_id, id) ON DELETE RESTRICT
  NOT VALID;

CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_runtime_id
  ON public.bot_remote_runtime_bindings(team_id, runtime_id);
CREATE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_bot_id
  ON public.bot_remote_runtime_bindings(team_id, bot_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_remote_runtime_bindings_primary
  ON public.bot_remote_runtime_bindings(bot_id)
  WHERE is_primary = TRUE;

-- The restore spans every team; lift the request-scoped policies while
-- copying and restore FORCE RLS after.
ALTER TABLE public.user_runtimes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.workspaces NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.workspaces DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspace_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspace_bindings DISABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF to_regclass('public.bot_workspace_bindings') IS NOT NULL
       AND to_regclass('public.workspaces') IS NOT NULL THEN
        INSERT INTO public.bot_remote_runtime_bindings
            (id, bot_id, runtime_id, is_primary, tool_approval_config, created_at, updated_at, team_id)
        SELECT b.id, b.bot_id, b.workspace_id, b.is_primary, b.tool_approval_config,
               b.created_at, b.updated_at, b.team_id
        FROM public.bot_workspace_bindings b
        JOIN public.workspaces w ON w.team_id = b.team_id AND w.id = b.workspace_id
        JOIN public.user_runtimes r ON r.team_id = w.team_id AND r.id = w.id
        WHERE w.kind = 'user'
        ON CONFLICT (id) DO NOTHING;
    END IF;
END
$$;

ALTER TABLE IF EXISTS public.bot_workspace_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.bot_workspace_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS public.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;

ALTER TABLE public.bot_remote_runtime_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_remote_runtime_bindings FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_remote_runtime_bindings_team_select ON public.bot_remote_runtime_bindings;
DROP POLICY IF EXISTS bot_remote_runtime_bindings_team_insert ON public.bot_remote_runtime_bindings;
DROP POLICY IF EXISTS bot_remote_runtime_bindings_team_update ON public.bot_remote_runtime_bindings;
DROP POLICY IF EXISTS bot_remote_runtime_bindings_team_delete ON public.bot_remote_runtime_bindings;

CREATE POLICY bot_remote_runtime_bindings_team_select ON public.bot_remote_runtime_bindings
  FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_remote_runtime_bindings_team_insert ON public.bot_remote_runtime_bindings
  FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_remote_runtime_bindings_team_update ON public.bot_remote_runtime_bindings
  FOR UPDATE
  USING (team_id = public.memoh_current_team_id())
  WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_remote_runtime_bindings_team_delete ON public.bot_remote_runtime_bindings
  FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Remote workdirs only ever referenced remote mounts in this stage, and every
-- one of those was restored above.
ALTER TABLE public.bot_workdirs
  DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;
ALTER TABLE public.bot_workdirs
  ADD CONSTRAINT bot_workdirs_remote_binding_fkey
  FOREIGN KEY (team_id, remote_binding_id)
  REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE CASCADE
  NOT VALID;

DROP TABLE IF EXISTS public.bot_workspace_bindings;
DROP TABLE IF EXISTS public.workspaces;

COMMIT;
