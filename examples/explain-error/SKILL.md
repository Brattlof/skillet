---
name: explain-error
description: Explain a stack trace or error message and suggest concrete fixes.
---

# explain-error

Use this when the user pastes an error, a stack trace, or failing build output.

1. Find the actual failure: the first real error, not the cascade of messages after it.
2. Explain in plain language what it means and the most likely cause.
3. Point at the specific file and line when the trace gives one.
4. Suggest one or two concrete fixes, with the exact change to make.
5. If the cause is ambiguous, say what to check or run next to narrow it down.

Prefer the smallest fix that addresses the root cause over a workaround, and say so when
you are guessing rather than certain.
