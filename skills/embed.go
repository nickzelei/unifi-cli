// Package skills embeds the agent skills shipped with the CLI, so
// `unifi skill install` works without a checkout.
package skills

import "embed"

// FS holds each skill as a top-level directory, e.g. unifi/SKILL.md.
//
//go:embed unifi
var FS embed.FS
