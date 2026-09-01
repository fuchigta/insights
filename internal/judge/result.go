package judge

import (
	"errors"
	"strings"
)

// RunInfo は 1 回のバックエンド実行のメタ情報。
//
// judge.Judge.Evaluate は json.RawMessage と error しか返せないため、実行メタ情報
// （バックエンド側のセッション ID・コストなど）を呼び出し元に渡す手段がインターフェース
// 上にない。バックエンドは Runner を実装することで、この情報を呼び出し元に渡せる。
type RunInfo struct {
	SessionID  string  // バックエンド側のセッション ID。評価セッション自体を集計から外すのに使う
	CostUSD    float64 // 実行に掛かった費用。バックエンドが報告しないなら 0
	DurationMS int64
	NumTurns   int
	Model      string
}

// 評価の失敗は種類ごとに意味（利用者が取るべき手当て）が違うため、番兵エラーで
// 仕分けられるようにしている。文字列一致で仕分けると、メッセージを直した瞬間に
// 静かに壊れる。バックエンドをまたいで同じ仕分けができるよう、番兵は個々の実装
// パッケージではなくここに置く。
var (
	// ErrRateLimited はレート制限らしき理由で実行が失敗したことを表す。
	//
	// レート制限は「このセッションの評価だけが失敗した」のではなく、アカウント全体に
	// 効いている状態を示す。呼び出し側はこれを見て残りのセッションの評価を打ち切れる
	// （制限中に叩き続けても失敗が増えるだけで、課金確認を通した意味も失われる）。
	ErrRateLimited = errors.New("評価バックエンドの実行がレート制限らしきエラーで失敗しました")

	// ErrTimeout は 1 回の実行が制限時間内に終わらなかったことを表す。
	ErrTimeout = errors.New("評価バックエンドの実行がタイムアウトしました")

	// ErrSchemaMismatch は再試行しても Schema に沿う JSON を得られなかったことを表す。
	ErrSchemaMismatch = errors.New("有効な評価 JSON を得られませんでした")
)

// rateLimitNeedles はプロセス出力からレート制限を推測するための手掛かり。
// バックエンドごとに文言は違うが、どれも HTTP 429 相当の状況を指す語を含むため
// 共通の集合で拾う。誤検出しても「時間をおいて再実行」を促すだけで害は小さく、
// 取りこぼすと制限中に叩き続けて失敗を増やすほうが痛い。
var rateLimitNeedles = []string{"rate limit", "rate_limit", "too many requests", "429", "overloaded", "usage limit"}

// LooksRateLimited はプロセス出力にレート制限を示唆する文字列が含まれるかを返す。
func LooksRateLimited(s string) bool {
	lower := strings.ToLower(s)
	for _, needle := range rateLimitNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
