# Security policy

## Reporting a vulnerability

Report security issues privately through GitHub Security Advisories:
https://github.com/Brattlof/skillet/security/advisories/new

Please do not open a public issue for a vulnerability. You will get an acknowledgement
within a few days.

## Supported versions

Security fixes target the latest released minor version. Older versions are not patched.

## Trust model for skills

skillet installs a skill by cloning a third-party Git repository and copying a folder
into your skills directory. Know the trust boundary:

- A skill's contents run with your agent's privileges. Only install skills you trust.
- An unpinned entry tracks the source repo's default branch, so its content can change
  after it was reviewed. skillet prints a warning when you install an unpinned skill.
  Pin an install to an exact commit or tag with `skillet add <name>@<ref>`, and prefer
  entries that set a `cksum`, which skillet verifies on install.
- Pinning prevents silent changes but not repository deletion or force-push: the content
  lives in the author's repo, outside skillet's control. There is no transparency log.
- The registry index is validated when it loads. skillet requires repo and remote MCP URLs
  to use https (plaintext http is allowed only for a loopback host, for local development),
  and refuses URLs and refs that could be smuggled into git as command-line options.

## Community skills (skills.sh)

`skillet add owner/repo/skill`, `skillet search`, `skillet trending`, and `skillet hot`
reach beyond the curated registry to [skills.sh](https://skills.sh). Those skills are not
reviewed by skillet:

- Before installing or updating one, skillet fetches the security audit shown on the
  skill's skills.sh page (Gen Agent Trust Hub, Socket, Snyk, and any other scanners
  skills.sh adds) and refuses a Fail from any of them unless you pass `--force`. A skill
  with no audit, or an audit skillet cannot reach, installs with a warning. If that
  endpoint stops answering without a token, skillet falls back to the older risk-level
  endpoint and treats high or critical as a Fail.
- The audit covers a skill's current version only. A pinned `@ref` on a community skill
  therefore needs `--force`; for reproducible installs, use `skillet.lock`, which records
  the exact commit of a normal install.
- A lockfile decides content: it pins exact commits, and it can point at any https repo
  (true before skills.sh support, too). skillet validates every entry and re-checks the
  audit of the skills.sh skills in it, but that audit is of the current version, not the
  pinned one. Review a lockfile you did not write like code.
- skillet never deletes a file or folder it did not install. Replacing one takes
  `--replace`, which is separate from `--force` so neither implies the other.
- Everything skills.sh returns is treated as untrusted: owner, repo, and skill names are
  validated before they reach a URL, git, or the filesystem; text is stripped of control
  characters before it reaches your terminal; responses are size-limited.
- skillet finds the skill folder in the cloned repo itself, never follows symlinks while
  doing so, and skips `node_modules`, build output, and very deep or very large trees.
- git runs with credential prompts disabled, so a mistyped repo fails instead of asking
  for a password.
- The trending and hot lists are built in CI, published next to the registry index, and
  validated on load like the index. Their spam filter drops forks, archived repos, low-star
  repos, implausible install counts, and failed audits. It reduces noise; it is not a
  security review.

Treat skills like any other dependency: review what you install, pin versions, keep them
current with `skillet update`, and audit them with `skillet doctor`.
