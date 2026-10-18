# Memoh Quick Reference

A condensed map of the official docs at https://docs.memoh.ai. Use it to answer
basic questions and to pick the right page; fetch the page when the user needs
details, exact steps, or limits. Doc paths below are relative to the site root:
append `.md` to fetch Markdown, prefix `/zh` for Chinese.

## Concepts

| Concept | What it is | Doc path |
|---------|------------|----------|
| Bot | An agent with its own workspace, memory, channels, and permission boundary. Bots cannot see into each other; one bot per job. | `/guides/working-with-bots` |
| Workspace | The bot's private computer (`/data`): files, terminal, Node.js and Python runtimes, optional desktop and headed browser. State persists across sessions. | `/guides/container` |
| Session | One conversation context. Types: chat, discuss (observes groups, speaks selectively), schedule, subagent, ACP agent. Memory is shared across sessions. | `/guides/sessions` |
| Channel | A messaging platform connected on the bot's Platforms tab: Telegram, Slack, Discord, Feishu (Lark), DingTalk, WeCom, WeChat, QQ, LINE, plus the Memoh app. | `/integrations/channels` |
| Memory | Long-term memory across sessions and channels, backed by a memory provider (Built-in, Mem0, OpenViking) chosen on the General tab. | `/guides/memory` |
| Context compaction | Summarizes older history of one session; on by default. Different from memory compaction, which rewrites stored memories. | `/guides/compaction` |
| Skill | A `SKILL.md` prompt module the bot loads on demand. Managed on the Skills tab or installed from Supermarket. | `/guides/skills` |
| Supermarket | Built-in catalog under Settings with Connectors and Skills tabs; installs into a target bot. | `/guides/supermarket` |
| Connector | OAuth or API-key binding to a hosted service such as GitHub or Notion, via Connect-It. | `/guides/connectors` |
| Hook | Event rule in `/data/.memoh/hooks.json` that runs a command or tool at lifecycle events such as `PreToolUse` or `TurnEnd`. | `/guides/hooks` |
| MCP | Connection to an external MCP tool server, Stdio or Remote (HTTP/SSE), with optional OAuth. | `/guides/mcp` |
| ACP agent | Codex or Claude Code running inside the bot's container workspace on the user's own subscription or API key. | `/guides/acp` |
| Scheduled task | Cron rule that sends a command to the bot; the bot can create one with its `schedule` tool. Evaluated in the bot's timezone. | `/guides/schedule` |
| Browser / Computer Use | The bot drives a headed browser on a visible desktop; the user can take over for logins or CAPTCHAs. | `/guides/browser-computer-use` |
| Access control | Channel Members (IM identities) and Workspace Members (Memoh users) with tiers: chat, read files, write files, run commands, manage. | `/guides/access` |
| Approvals | Human approval for sensitive tool calls, set on the Tool Approval tab. | `/guides/approvals` |

Choosing between skills, hooks, MCP, connectors, and Supermarket:
`/guides/extensions`.

## Where Settings Live

Bot settings are on the bot's detail page (Bots page → click a bot). Tabs:

| Tab | Contents |
|-----|----------|
| Overview | Health of workspace runtime, database, channels, memory |
| General | Chat, title, and image models; memory, search, and TTS providers; timezone; language; reasoning effort; delete bot (Danger Zone) |
| Desktop | Display runtime, headed browser, display sessions |
| Workspace | Container lifecycle, snapshots, data export/import, CDI devices (GPU) |
| Network | Workspace network and overlay provider |
| Memory | Browse, search, create, edit, compact, rebuild memories |
| Platforms | Channel configurations |
| Access | ACL rules and default access behavior |
| Tool Approval | Tools that need human confirmation |
| Agents | ACP agents (Codex, Claude Code) |
| Connectors | Connected services: enable, disable, reauthorize, disconnect |
| Hooks | `/data/.memoh/hooks.json` rules |
| MCP | MCP connections |
| Compaction | Context compaction settings and logs |
| Schedule | Scheduled tasks and execution logs |
| Skills | Workspace skills |

Elsewhere in the app:

- Files, Terminal, and the desktop/browser panes are in the chat workspace UI.
- Supermarket is under Settings.
- Model providers and models: `/integrations/providers/llm`. Search, video,
  memory, and TTS providers sit alongside it.
- Interface language, theme, fonts, shortcuts: `/guides/preferences`.

## Slash Commands

`/help`, `/new [chat|discuss]`, `/stop`, plus groups such as `/schedule`,
`/model`, `/memory`, `/search`, `/settings`, `/skill`, `/fs`, `/status`,
`/access`, `/usage`, `/compact`, `/context`, `/reasoning`, `/language`, and
`/link <code>`. Full list: `/guides/slash-commands`.

## Other Pages

| Topic | Doc path |
|-------|----------|
| Product overview and FAQ | `/guides/overview` |
| First bot, BYOK (own API keys) | `/guides/quick-start` |
| Ready-made scenarios | `/guides/use-cases` |
| Workspace file manager | `/guides/files` |
| Self-hosted server deploy | `/self-hosted/docker` |
| Workspace backends (containerd, Docker, Apple, local) | `/self-hosted/workspace-backends` |
| Desktop app | `/self-hosted/desktop` |

Plans, credits, billing, and account questions are not covered by the docs;
the overview page links to the Memoh Help Center for them.
