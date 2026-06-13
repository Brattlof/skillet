#!/usr/bin/env bash
# Example skillet hook. It runs when a Claude Code session starts and prints a
# short tip. Output goes to stderr so it is shown to you without being added to
# the model's context. Replace it with your own SessionStart logic, or remove it
# with: skillet remove session-greeting
echo "skillet: run 'skillet search <topic>' to find a skill for the task at hand." >&2
