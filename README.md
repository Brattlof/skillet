<div align="center">

<img src="assets/banner.webp" alt="skillet" width="640">

# skillet

**A tiny, zero-dependency package manager for AI-agent skills, slash commands, and hooks.**

*Search a curated registry and install with one command.*

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

$ skillet list
Installed skills in ~/.claude/skills:
  - hello-skill
```

---

## Why

Copy-pasting skill folders by hand, hunting through random repos, never knowing what's worth
installing. `skillet` fixes that: search a curated, **human-tested** registry and install with
one command.

- One static binary, no dependencies. The registry ships embedded - no backend, no account.
- Every entry is hand-reviewed by someone who actually ran it. No slop.
- Installs into `~/.claude/skills`; point it elsewhere with `--dir`.

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
skillet add <name>[@ref] Install a skill (optionally pinned to a commit or tag)
skillet install          Restore skills from skillet.lock
skillet lock             Write skillet.lock from installed skills
skillet update [name]    Update an installed skill, or all of them
skillet doctor           Check installed skills for problems
skillet remove <name>    Remove an installed skill
skillet list             List installed skills
skillet search <query>   Search the registry (ranked + fuzzy; --kind, --tag)
skillet info <name>      Show details of a registry entry
skillet registry         Show every registry entry
skillet publish          How to publish your own skill
skillet completion <sh>  Output a bash, zsh, or fish completion script
skillet self-update      Update the skillet binary to the latest release
```

### Shell completion

```bash
# bash (current shell)
source <(skillet completion bash)
# zsh
skillet completion zsh > "${fpath[1]}/_skillet"
# fish
skillet completion fish > ~/.config/fish/completions/skillet.fish
```

Completion suggests subcommands, the `--dir` flag, and skill names pulled from the
local index, so it stays instant and works offline.

Override the target directory per-command or globally:

```bash
skillet add hello-skill --dir ~/.config/agent/skills
export SKILLET_SKILLS_DIR=~/.config/agent/skills
```

## Reproducible installs

Pin an install to an exact version with `skillet add <name>@<ref>` (a commit SHA or
tag). `skillet lock` writes a `skillet.lock` capturing every installed skill at its
resolved commit and checksum, and `skillet install` (no arguments) restores exactly
that set. Point the lockfile elsewhere with `SKILLET_LOCKFILE`.

## Environment

- `SKILLET_REGISTRY_URL` - override the registry index URL.
- `SKILLET_OFFLINE=1` - never hit the network; use the cached or embedded index.
- `SKILLET_SKILLS_DIR` - override the skills directory (skills kind only).
- `SKILLET_CACHE_DIR` - override where the fetched index is cached.
- `SKILLET_LOCKFILE` - override the lockfile path (default: `./skillet.lock`).

## How it works

1. Each skill is one file under [`skills/`](skills), sharded by first letter
   (`skills/g/git-commit.json`). CI compiles them into an index published
   over a CDN. `skillet` fetches that index, caches it locally with ETag revalidation, and falls
   back to a copy embedded in the binary when it is offline.
2. `skillet add` clones the entry's repo (pinned to its `ref` when set), copies just the skill
   folder into your skills directory, verifies its checksum when set, and cleans up. No `.git`.
3. Contributing a skill is a PR that adds one file `skills/<first-letter>/<name>.json` - see below.

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
- [ ] Install hooks and slash commands, not just skills
- [ ] Version pinning + a lockfile
- [x] `skillet doctor` - validate installed skills

## License

[MIT](LICENSE)
