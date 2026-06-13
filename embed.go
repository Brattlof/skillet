package skillet

import "embed"

// SkillsFS embeds the skill metadata shards so the binary always has a baseline
// index to fall back on when the remote registry and local cache are unavailable.
//
//go:embed skills
var SkillsFS embed.FS
