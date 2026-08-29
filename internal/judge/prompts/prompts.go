// Package prompts はセッション評価プロンプト本文と JSON Schema を
// go:embed でバイナリに埋め込み、薄いラッパーとして提供する。
// 実体（プロンプト文面・スキーマ定義）は同ディレクトリの .md / .json ファイルにあり、
// このファイルはそれを読み出すだけにとどめる。
package prompts

import (
	_ "embed"
	"encoding/json"
)

// PromptVersion はセッション評価プロンプトのバージョン識別子。
// internal/store の評価キャッシュ（session_evals テーブルの prompt_version 列）は
// (session_id, prompt_version) をキーにして評価結果をキャッシュする。
//
// 【規約】session_eval.md（評価軸・出力形式の指示）または
// session_eval.schema.json（返させたい JSON の形）の意味を変える変更をしたら、
// 必ずこの値を変更すること。変更しないと、古いプロンプトで評価済みのセッションが
// 新しいプロンプトでも同じキャッシュとして扱われてしまい、評価内容が古いまま
// 気付かれずに使い回されてしまう。
const PromptVersion = "session-eval-v3"

//go:embed session_eval.md
var sessionEvalPrompt string

//go:embed session_eval.schema.json
var sessionEvalSchema []byte

// SessionEvalPrompt はセッション評価の役割指示（5つの評価軸・出力形式のルールを含む）を返す。
// judge.Request.System に渡すことを想定している。
func SessionEvalPrompt() string {
	return sessionEvalPrompt
}

// SessionEvalSchema は model.Eval に対応する JSON Schema を返す。
// judge.Request.Schema に渡すことを想定している。
// 呼び出し側が書き換えても embed 元には影響しないよう、都度コピーを返す。
func SessionEvalSchema() json.RawMessage {
	out := make(json.RawMessage, len(sessionEvalSchema))
	copy(out, sessionEvalSchema)
	return out
}
