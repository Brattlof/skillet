# Contributing to skillet

Thanks for helping keep this the *no-slop* registry. One rule matters; the rest is mechanics.

## The one rule

**You have actually used the skill you're adding.** Not "it looks cool." You ran it, for real
work, and kept it. If that's not true, don't add it yet. We'd rather have 30 hand-tested entries
than 3,000 dead links.

## Add a skill

1. Fork and create a branch.
2. Add one object to [`registry.json`](registry.json), in alphabetical order by `name`:

   ```json
   {
     "name": "your-skill",
     "description": "One single line on what it does. No marketing.",
     "repo": "https://github.com/you/your-repo",
     "path": "path/to/the/skill/folder",
     "author": "you",
     "tags": ["topic", "agent"]
   }
   ```

   - `repo` is the public repository to clone.
   - `path` is the folder *inside* that repo containing the skill (the one with `SKILL.md`).
3. Run `go test ./...` - the registry must still parse and the example must resolve.
4. Open a PR. In the description, tell us **how you've used it** (one or two sentences).

## What gets rejected

- Dead links, or paths that don't point at a real skill folder.
- Generated/listicle filler with no usage behind it.
- "Comment X for my course" / lead-magnet entries.
- Self-promotion you haven't actually shipped or used.
- Duplicates (improve the existing entry instead).

## Code changes

- Keep it dependency-free (standard library only). That's a feature.
- `go vet ./...` and `go test ./...` must pass; CI also runs `go test -race ./...`.
- Run `gofmt -w .` before committing.

## Reporting a bad entry

See something below the bar? [Open an issue](../../issues) and say why. Curation is a
conversation - flagging weak entries is as valuable as adding good ones.
