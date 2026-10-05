# draftplane

## Agentic plan review in your terminal

Place feedback threads anywhere on an agent's development plan, directly in your terminal. Your agents can read threads, reply, and revise the plan.

Your plans can stay wherever they are today, as markdown files on disk, Notion, GitHub, etc. Draftplane lets you view and comment on a plan without ever touching the file. Agents can write plans directly into Draftplane, update them, and read them back for revision.

## Install

```
npm install -g draftplane
```

Requires Node 18 or newer. The package ships a small launcher that starts the binary, so Node is needed to run `draftplane`. The binary includes the Draftplane TUI and stdio MCP interface for agents.

Register the MCP to review plans with your agents. Example MCP configuration:

```json
{
  "mcpServers": {
    "draftplane": {
      "command": "draftplane",
      "args": ["mcp"]
    }
  }
}
```

With Claude Code:

```
claude mcp add --scope user draftplane -- draftplane mcp
```

## Usage

```
draftplane review ~/plans/cache-iteration-42.md
```

Draftplane works out of the box with no configuration. Just install and review a plan with your agents - comment, revise, and iterate.

### Commands

| | |
|---|---|
| `draftplane` | see your plan list |
| `draftplane review <path\|id>` | open a plan by file or by id |
| `draftplane theme [dark\|light\|system]` | show or set the theme |
| `draftplane mcp` | the stdio MCP server for your agent |
| `draftplane version` | the installed version |

## Platforms

macOS and Linux, arm64 and x64. Windows only under WSL using the Linux build.

## Terminal experience

Plans are usually long-form content, and most terminals are optimized for command output. For an ideal review experience, slightly increase default line spacing:
// both increase spacing by 15%
- Ghostty: `adjust-cell-height = 15%`
- iTerm2: Settings → Profiles → Text → **Vertical spacing** `1.15`

## Licence

MIT. See LICENSE in this package.

Third-party components are listed with their licences in THIRD-PARTY-NOTICES.

Bugs: bugs@draftplane.io · https://draftplane.io
