<div align="center">

<img src="assets/banner.webp" alt="skillet" width="640">

# skillet

**A tiny, zero-dependency package manager for Agent Skills.**

*Search a curated registry and install with one command - into Claude Code, Cursor, Codex, Gemini CLI, or Copilot.*

[![CI](https://github.com/Brattlof/skillet/actions/workflows/ci.yml/badge.svg)](https://github.com/Brattlof/skillet/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go)
![GitHub stars](https://img.shields.io/github/stars/Brattlof/skillet?style=social)

</div>

<div align="center">
  <img src="assets/demo.gif" alt="skillet demo" width="700">
</div>

```console
$ skillet search example
NAME         DESCRIPTION                                                   TAGS
hello-skill  Minimal example skill that ships with skillet - install i...  example,starter,docs

$ skillet add hello-skill
Installing hello-skill from https://github.com/Brattlof/skillet ...
Installed hello-skill -> ~/.claude/skills/hello-skill

$ skillet add hello-skill --target agents
Installed hello-skill -> ~/.agents/skills/hello-skill   # Cursor, Codex, Gemini, Copilot

$ skillet list
NAME         KIND   INSTALLED  SOURCE                       STATUS
hello-skill  skill  a1b2c3d    github.com/Brattlof/skillet  tracking
```

---

## Why

Copy-pasting skill folders by hand, hunting through random repos, never knowing what's worth
installing. `skillet` fixes that: search a curated, **human-tested** registry and install with
one command.

- One static binary, no dependencies. The registry ships embedded - no backend, no account.
- Every entry is hand-reviewed by someone who actually ran it. No slop.
- Skills use the open [Agent Skills](https://agentskills.io) `SKILL.md` format, so they work
  across tools. Install for Claude Code (default) or for the shared `~/.agents/skills`
  directory with `--target agents`, which Cursor, Codex, Gemini CLI, and Copilot all read.

## Works with

skillet installs for Claude Code by default (`~/.claude/skills`, plus its slash commands and
hooks). Add `--target agents` and skills go to the shared `~/.agents/skills` directory that
Cursor, OpenAI Codex, Gemini CLI, and GitHub Copilot all read. Slash commands and hooks are
Claude Code specific; skills are the cross-tool part.

## Install

```bash
# Go users
go install github.com/Brattlof/skillet/cmd/skillet@latest

# Or grab a binary from Releases
# https://github.com/Brattlof/skillet/releases
```

> Building from source: `git clone` this repo, then `go build -o skillet ./cmd/skillet`.

## Usage

```text
skillet add <name>[@ref]   Install a skill, command, or hook (optionally pinned)
skillet install            Restore everything from skillet.lock
skillet install --frozen   Verify the install matches skillet.lock (no changes)
skillet lock               Write skillet.lock from what is installed
skillet update [name]      Update an installed item, or all of them
skillet doctor             Check installed items for problems
skillet remove <name>      Remove an installed item
skillet list               List installed items
skillet search <query>     Search the registry (ranked + fuzzy; --kind, --tag)
skillet info <name>        Show details of a registry entry
skillet registry           Show every registry entry
skillet publish            How to publish your own skill
skillet completion <sh>    Output a bash, zsh, or fish completion script
skillet self-update        Update the skillet binary to the latest release
```

Add `--target agents` to any command to use `~/.agents/skills` instead of
`~/.claude/skills`, or set `SKILLET_TARGET=agents`.

### Skills, commands, hooks, and MCP servers

A registry entry has a `kind`: `skill` (the default), `command`, `hook`, or `mcp`. skillet
installs each into the place the tool reads:

- a **skill** is a folder copied to `~/.claude/skills/<name>/` (or `~/.agents/skills/<name>/`
  with `--target agents`, the shared location Cursor, Codex, Gemini CLI, and Copilot read),
- a **command** is a single `.md` file copied to `~/.claude/commands/<name>.md`,
- a **hook** is a script copied to `~/.claude/hooks/`, and skillet registers it in
  `~/.claude/settings.json` under the event from the entry's `hook` block (and
  un-registers it on `remove`),
- an **MCP server** is registered into a client's MCP config; `--target` picks the client.

Commands and hooks are Claude Code specific. MCP servers work across tools:

```bash
skillet add context7 --target cursor     # -> ~/.cursor/mcp.json
skillet add context7 --target windsurf   # -> ~/.codeium/windsurf/mcp_config.json
skillet add context7 --target claude     # -> ./.mcp.json (project scope)
skillet remove context7 --target cursor
```

Supported MCP clients: claude, cursor, windsurf, gemini, cline. skillet merges the
server into the existing config and leaves every other entry untouched.

### Shell completion

```bash
# bash (current shell)
source <(skillet completion bash)
# zsh
skillet completion zsh > "${fpath[1]}/_skillet"
# fish
skillet completion fish > ~/.config/fish/completions/skillet.fish
```

Completion suggests subcommands, the `--target` and `--dir` flags, and skill names pulled
from the local index, so it stays instant and works offline.

Choose where skills install, per-command or globally:

```bash
skillet add hello-skill --target agents          # ~/.agents/skills (cross-tool)
export SKILLET_TARGET=agents                      # make it the default
skillet add hello-skill --dir ~/somewhere/else    # an exact directory
```

## Reproducible installs

Pin an install to an exact version with `skillet add <name>@<ref>` (a commit SHA or
tag). `skillet lock` writes a `skillet.lock` capturing every installed item at its
resolved commit and checksum, and `skillet install` (no arguments) restores exactly
that set. Restore prunes anything skillet installed that is no longer in the
lockfile, so the result matches the lock; a hand-placed file is left alone.
`skillet install --frozen` reports whether the install already matches the lockfile
without changing anything, which is what you want in CI. Point the lockfile
elsewhere with `SKILLET_LOCKFILE`.

## Environment

- `SKILLET_REGISTRY_URL` - override the registry index URL.
- `SKILLET_OFFLINE=1` - never hit the network; use the cached or embedded index.
- `SKILLET_TARGET` - default install target (`claude` or `agents`).
- `SKILLET_SKILLS_DIR` - override the skills directory (skills kind only).
- `SKILLET_CACHE_DIR` - override where the fetched index is cached.
- `SKILLET_LOCKFILE` - override the lockfile path (default: `./skillet.lock`).

## How it works

1. Each entry is one file under the directory for its kind - [`skills/`](skills),
   [`commands/`](commands), or [`hooks/`](hooks) - sharded by first letter
   (`commands/c/changelog.json`). The folder sets the kind. CI compiles them into an index
   published over a CDN. `skillet` fetches that index, caches it locally with ETag
   revalidation, and falls back to a copy embedded in the binary when it is offline.
2. `skillet add` clones the entry's repo (pinned to its `ref` when set), copies just that
   artifact into the right directory, verifies its checksum when set, and cleans up. No `.git`.
3. Contributing is a PR that adds one file under `skills/`, `commands/`, or `hooks/` - see below.

Point `skillet` at a different index with `SKILLET_REGISTRY_URL`, or force the cached/embedded
copy with `SKILLET_OFFLINE=1`.

## Security

A skill runs with your agent's privileges, so only install skills you trust. skillet warns
when you install an unpinned skill; pin to an exact version with `skillet add <name>@<ref>`
and audit installs with `skillet doctor`. See [SECURITY.md](SECURITY.md) for the full trust
model and how to report a vulnerability.

## Compatibility

skillet follows semantic versioning; see [COMPATIBILITY.md](COMPATIBILITY.md) for what stays
stable across the 1.x series.

## Contributing

The registry is only as good as the people keeping it honest. The one rule: **you've actually
used the skill you're adding.** See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome.

## Roadmap

- [x] `skillet update` - refresh installed skills
- [x] Install slash commands and hooks, not just skills
- [x] Version pinning + a lockfile (`skillet install --frozen`)
- [x] `skillet doctor` - validate installed skills

## License

[MIT](LICENSE)
