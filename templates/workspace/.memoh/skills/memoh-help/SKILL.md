---
name: memoh-help
description: Use when the user asks about Memoh itself, such as what it can do, how to set something up, where a setting is, how bots, workspaces, channels, memory, skills, Supermarket, connectors, schedules, hooks, MCP, ACP agents, access, or self-hosting work, or before telling the user that Memoh does not support something.
---

# Memoh Help

You are running inside Memoh. Answer questions about Memoh from the official
documentation at https://docs.memoh.ai.

## Ground Rule

What this workspace has installed is not what Memoh supports. A missing CLI,
one Node.js version, or an absent config file only describes this bot's current
environment. Use the docs to decide whether Memoh supports a feature, and run
environment checks only to report this bot's current state.

When the docs do not cover a question, say that the documentation does not
state it, share what the docs do say nearby, and leave the question open.

## Find The Answer

1. Read `references/quick-reference.md` in this skill directory. It covers the
   core concepts, where each setting lives, and which doc page to open.
2. If the user needs details, steps, or limits, fetch one to three pages as
   Markdown with `web_fetch`:
   - English: `https://docs.memoh.ai/<path>.md`, e.g. `https://docs.memoh.ai/guides/skills.md`
   - Chinese: `https://docs.memoh.ai/zh/<path>.md`, e.g. `https://docs.memoh.ai/zh/guides/skills.md`

   Use the page language that matches the user's.
3. If the quick reference does not name a page for the topic, fetch the index
   `https://docs.memoh.ai/llms.txt` and pick pages from it. Skip index entries
   titled "Redirecting"; they are old paths.
4. Fetch individual pages only. `llms-full.txt` is the whole site in one file,
   far too large for one turn.

If `web_fetch` fails, try `curl -fsSL <url>` in the workspace. If the docs are
unreachable, answer from the quick reference and tell the user you could not
reach the online docs.

## Answer

- Lead with the answer, then give steps in app terms, for example: bot detail
  page → **Schedule** tab.
- Link every page you relied on, using the page URL without `.md`, for example
  `https://docs.memoh.ai/guides/schedule`.
- The online docs describe the latest Memoh. If behavior here differs, the
  deployment may run an older version; say so instead of picking one.
- Plans, credits, billing, and account questions are outside the docs; say so.
- If the user wants the change made and you have a tool for it, such as
  `schedule`, offer to do it.
