// Package assets は insights が配布するスキル本体（SKILL.md）を go:embed で
// バイナリに埋め込む。実体（説明文）は同ディレクトリの SKILL.md にあり、
// このファイルは埋め込みとバージョン定数の提供に徹する。
package assets

import _ "embed"

// Version は SKILL.md の内容バージョン。SKILL.md 内の frontmatter
// `x-insights-version` と常に同じ値にすること。
//
// 【規約】SKILL.md の本文・frontmatter の意味を変える変更をしたら、
// 必ずこの値と SKILL.md 内の x-insights-version の両方を上げること。
// 上げ忘れると、導入済み環境が internal/skill.StateOutdated を検出できず、
// 古い説明のまま気付かれずに使い回されてしまう
// （internal/judge/prompts.PromptVersion と同種の規約）。
const Version = "2"

//go:embed SKILL.md
var skillMD []byte

// SkillMD は埋め込んだ SKILL.md のバイト列を返す。
// 呼び出し側が返り値を書き換えても埋め込み元には影響しないよう、都度コピーを返す。
func SkillMD() []byte {
	out := make([]byte, len(skillMD))
	copy(out, skillMD)
	return out
}
