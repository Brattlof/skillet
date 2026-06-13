---
description: Draft a Keep a Changelog entry from the staged diff
---

Look at the staged changes with `git diff --cached`. Write a single Keep a Changelog
entry for them: choose the right heading (Added, Changed, Deprecated, Removed, Fixed,
or Security) and a one-line, user-facing description in plain language. If the diff
spans more than one kind of change, write one entry per heading. Do not restate the
diff line by line - describe the effect on someone using the project.
