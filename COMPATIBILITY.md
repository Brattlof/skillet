# Compatibility policy

skillet follows semantic versioning. Within the 1.x series these stay backward compatible:

- The CLI command set and their documented flags. New commands and flags may be added;
  existing ones keep working.
- The registry shard schema and the compiled index format. Shards live under a per-kind
  directory (`skills/`, `commands/`, `hooks/`), first-letter sharded; the directory sets the
  kind. New optional fields may be added, and they are always `omitempty`.
- The install manifest (`<skills-dir>/.skillet/<name>.json`) and the lockfile
  (`skillet.lock`) formats. New optional fields may be added.
- The default install layout (`~/.claude/{skills,commands,hooks}`).
- The environment variables documented in the README.

Breaking changes to any of the above ship only in a new major version (2.0). Before 1.0
(the 0.x series) anything may change between minor versions.
