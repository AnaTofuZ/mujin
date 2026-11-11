// Package skill は司令塔用の SKILL.md を埋め込む。
package skill

import _ "embed"

//go:embed SKILL.md
var Markdown string
