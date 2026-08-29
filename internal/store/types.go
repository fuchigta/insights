package store

import (
	"encoding/json"
	"time"
)

// UsageCost は 1 usage イベント分の算出済みコスト。
// store は internal/pricing に依存しない（依存の向きを store <- pricing の一方向に保つため）。
// コストは呼び出し側（cmd 層など）が pricing.Table.Cost で計算し、この型に詰めて SaveSession に渡す。
type UsageCost struct {
	Seq     int     // model.Message.Seq に対応
	CostUSD float64 // 算出済みコスト（USD）
	Known   bool    // 単価が既知だったか。false の場合 CostUSD は無視して 0 として保存する
}

// EvalRun は 1 件の評価を行った claude 実行の実測値。判定バックエンドによっては
// 取得できないため（フェイク実装やコストを返さないバックエンド）、その場合はゼロ値になる。
type EvalRun struct {
	CostUSD   float64 // その評価 1 回にかかった実コスト（USD）
	SessionID string  // 評価を行った claude 実行自体の session_id。集計対象から除外する用
}

// SessionRow は一覧・集計向けの軽量なセッション情報。
// SessionByID のようにメッセージ全件を復元するコストをかけずに一覧表示や集計に使う。
type SessionRow struct {
	SessionID       string
	ProjectLabel    string
	ProjectPath     string
	StartedAt       time.Time
	EndedAt         time.Time
	IsSidechain     bool
	ParentSessionID string // サブエージェント(IsSidechain)の親セッションID。無ければ空
	Entrypoint      string
	FirstPrompt     string
	Title           string
	MessageCount    int
	ToolErrorCount  int
	ContentHash     string
}

// UsageRow は usage_events 1 行分。集計（コスト・トークン量の期間合計など）に使う。
type UsageRow struct {
	SessionID       string
	Timestamp       time.Time
	Model           string
	InputTokens     int
	OutputTokens    int
	ThinkingTokens  int
	CacheCreation5m int
	CacheCreation1h int
	CacheRead       int
	CostUSD         float64
	CostKnown       bool
}

// RollupRow は daily_rollups 1 行分。
type RollupRow struct {
	Date       string // YYYY-MM-DD
	RollupJSON json.RawMessage
	DailyPath  string
	RetroPath  string
	CreatedAt  time.Time
}
