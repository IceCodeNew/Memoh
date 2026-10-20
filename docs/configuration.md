# Configuration

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

## Configuration

The main configuration file is `config.toml` (copied from `conf/app.example.toml` or environment-specific templates for development), containing:

- `[log]` — Logging configuration (level, format)
- `[server]` — HTTP listen address and optional `public_url` for the externally reachable Web origin (for example, `https://app.example.com`). Set `MEMOH_SERVER_PUBLIC_URL` to override it. Capability tools use this base URL for credential setup links and the MCP OAuth callback at `/api/oauth/mcp/callback`. Configure it for channel conversations; without it, setup links are relative and OAuth keeps the existing local callback default. Do not include credentials, query parameters, or a fragment.
- `[admin]` — Admin account credentials
- `[auth]` — JWT authentication settings
- `[agent]` — Agent runtime limits, context scheduling rollout switches, and the policy for a hooks configuration that cannot be loaded (see below)
- `[database]` — Database backend selection (`postgres`)
- `[container]` — Workspace container backend selection (`docker`, `containerd`, `apple`) and common workspace image/data/bridge/CNI settings
- `[containerd]` / `[docker]` / `[apple]` — Backend-specific runtime configuration
- `[postgres]` — PostgreSQL connection
- `[qdrant]` — Qdrant vector database connection
- `[sparse]` — Sparse (BM25) search service connection
- `[channel]` — Channel service HTTP/RPC listen addresses (split mode)
- `[internal_rpc]` — Server↔Channel internal RPC targets and shared secret; empty secret selects the embedded all-in-one mode
- `[web]` — Web frontend address
- `[registry]` — Provider registry (`providers_dir` pointing to `conf/providers/`)
- `[supermarket]` — Supermarket integration (base_url)

### Hooks configuration that cannot be loaded

`[agent] hooks_config_unavailable` decides what a chat turn does when the bot's hooks configuration (`/data/.memoh/hooks.json`) cannot be loaded: the file cannot be parsed or read, or the workspace that holds it cannot be reached. A missing file is not a failure; it means the bot has no hooks.

- `continue` (default) — the turn is answered without tools, and the system prompt tells the model that hooks did not run. `UserMessageReceived` and `BeforeModelCall` hooks are skipped for that turn, so a rule that checks messages before they reach the model does not run either.
- `deny` — the turn is rejected, as it was before `continue` existed. Use it when hooks must check every message before it reaches an external model.

`PreToolUse` and `BeforeApprovalCreate` hooks refuse their operation in both modes. A workspace runtime that refuses the request for a reason of its own (removed, over quota, conflict) is reported as itself and rejects the turn in both modes. Discuss turns always reject: the bot speaks only through tools there, so a turn without tools would answer nothing.

Provider YAML templates in `conf/providers/` define preset configurations for various LLM providers (OpenAI, Anthropic, GitHub Copilot, etc.).

Configuration templates available in `conf/`:
- `app.example.toml` — Default template
- `app.docker.toml` — Docker deployment
- `app.apple.toml` — macOS (Apple Virtualization backend)
- `app.windows.toml` — Windows

Development configuration in `devenv/`:
- `app.dev.toml` — Development (connects to devenv docker-compose)
