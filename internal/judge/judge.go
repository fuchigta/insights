// Package judge は AI 評価バックエンドの抽象を定義する。
// 既定は claude -p のサブプロセス実行だが、将来 Codex CLI などに
// 差し替えられるよう、プロンプトを渡して JSON を受け取るだけの契約にしている。
package judge

import (
	"context"
	"encoding/json"
)

// Request は 1 回の評価依頼。
type Request struct {
	// System は役割の指示。空でもよい。
	System string
	// Prompt は評価対象の整形済みテキスト。
	Prompt string
	// Schema は返させたい JSON Schema。バックエンドはこれに沿うよう指示する。
	Schema json.RawMessage
	// Model は使用モデル。空ならバックエンドの既定。
	Model string
}

// Judge は AI 評価の実行主体。
type Judge interface {
	// Name は "claude-cli" のようなバックエンド識別子。
	Name() string

	// Available は実行可能かを返す。doctor が使う。
	Available() error

	// Evaluate は Request を実行し、Schema に沿う JSON を返す。
	// スキーマ不一致・パース失敗は実装側で 1 回だけ再試行してよい。
	Evaluate(ctx context.Context, req Request) (json.RawMessage, error)
}
