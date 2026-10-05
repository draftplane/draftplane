# Draftplane

**Agent plan review in your terminal.**

Draftplane lets you review plans with your agents in your terminal. You can
comment anywhere on an agent-written plan, and your agents can read the threads
over MCP, answer them, and update.

## Install

**Install the package**
```
npm install -g draftplane
```

**Connect the MCP**

```bash
# Standard JSON config
{
  "mcpServers": {
    "draftplane": {
      "command": "draftplane",
      "args": ["mcp"]
    }
  }
}

# claude code
$ claude mcp add --scope user draftplane -- draftplane mcp
```

Requires Node >=18, macOS | Linux, arm64 | x64. WSL with the Linux
build should work, but isn't tested.

## Getting Started
**Open a plan**

```
draftplane review ~/plans/auth.md
```

Then use the key actions to review. Add comments with `c`, reply with `r`, and more. Type `?` to see the full list of available actions.

When you've reviewed, you can ask your agents to fixup the plan. They can
reply/resolve comments, update the plan, and dispatch their own reviews via subagents.

```
# draftplane review ~/plans/auth.md

┌────────────────────────────────────────────────────────────────────┐
│                                                                    │
│     § Token refresh                                                │
│                                                                    │
│ ┃ ※ The client refreshes the access token on a 401 and retries the │
│ ┃   request once.                                                  │
│       | user-calm-mountain                                         │
│         what happens if the refresh itself 401s?                   │
│       | agent-blue-parakeet-f9                                     │
│         good catch — bounding that. rewriting the section now.     │
│                                                                    │
│     §§ Revocation                                                  │
│                                                                    │
│   ○ Codes are single-use and expire after ten minutes.             │
```

## How it works

Draftplane has one goal - give you a surface to review plans in your terminal.
It isn't an editor, and it isn't a file server. When you review a plan,
Draftplane follows the file but doesn't change it - Draftplane is a review layer
on top. As the plan changes, Draftplane reanchors the review content (threads,
approvals, etc) over the updated bytes.

When your plan is *not* a file on disk (Notion, GitHub, Linear etc) your agent can
just write the bytes directly to Draftplane for review. Plans written
directly to Draftplane can be exported at any time. Your plans can stay wherever
they are - files, Notion, GitHub - and Draftplane supports reviewing them.

Draftplane doesn't send any data anywhere during the review loop with your
agents. The draftplane binary hosts the stdio MCP server. Your agent can
dispatch subagents and have its own review loop. Draftplane records each agent
as a unique review identity.

See more on the [workflows page](https://draftplane.io/workflows.html).

## Commands

|                                           |                                                             |
| ----------------------------------------- | ----------------------------------------------------------- |
| `draftplane`                              | open the plan list                                          |
| `draftplane review <path\|id>`            | open a plan by file or by id                                |
| `draftplane theme [dark\|light\|system]`  | show or set the theme                                       |
| `draftplane mcp`                          | the stdio MCP server for your agent                         |
| `draftplane version`                      | the installed version                                       |

Draftplane is free and licensed under the [MIT license](https://github.com/draftplane/draftplane/blob/main/LICENSE)

[draftplane.io](https://draftplane.io)

Copyright 2026 Draftplane LLC
