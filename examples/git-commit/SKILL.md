---
name: git-commit
description: Write a clear, conventional commit message from the staged diff.
---

# git-commit

Use this when the user asks you to commit staged changes and wants a well-formed message.

1. Run `git diff --cached` to see exactly what is staged.
2. Write a single imperative subject line, 50 characters or fewer, capitalized, with no
   trailing period (for example "Add retry to the upload client").
3. If the change needs context, add a blank line and a short body that explains what
   changed and why, wrapped at 72 columns.
4. Show the message and run `git commit` only after the user confirms.

Describe what the diff actually does, not what was intended. Keep one logical change per
commit; if the subject needs the word "and", the commit is probably too big.
