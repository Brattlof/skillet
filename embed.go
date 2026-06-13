package skillet

import "embed"

// SkillsFS embeds the registry metadata shards (one per-kind directory: skills,
// commands, hooks) so the binary always has a baseline index to fall back on when
// the remote registry and local cache are unavailable.
//
//go:embed skills commands hooks
var SkillsFS embed.FS
