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
- The registry index is validated when it loads, and skillet refuses repo URLs and refs
  that could be smuggled into git as command-line options.

Treat skills like any other dependency: review what you install, pin versions, keep them
current with `skillet update`, and audit them with `skillet doctor`.
