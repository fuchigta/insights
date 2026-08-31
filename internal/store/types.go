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

// 評価失敗の分類。DB に保存する値なので、種類を増やすことはあっても既存の値は変えないこと。
// 種類を分けるのは、利用者が取るべき手当てが違うため（レート制限は待つ、スキーマ不適合は
// プロンプトを直す、といった具合に）。
const (
	EvalFailureRateLimit = "rate_limit"
	EvalFailureTimeout   = "timeout"
	EvalFailureSchema    = "schema"
	EvalFailureSave      = "save"
	EvalFailureOther     = "other"
)

// EvalRunRecord は評価 1 回分の実行記録。成否によらず 1 行残す。
// 成功した評価だけを見ていると「特定の形のセッションで失敗し続けている」ことに気づけない
// ため、失敗も履歴として残す。
type EvalRunRecord struct {
	SessionID     string
	PromptVersion string
	Judge         string
	JudgeModel    string
	OK            bool
	FailureKind   string  // EvalFailure* のいずれか。成功なら空
	FailureReason string  // 失敗理由の全文。成功なら空
	CostUSD       float64 // 失敗した試行にもコストは発生しうるので、成否によらず記録する
	RunSessionID  string
}

// EvalCostSample は「1 セッションの評価にいくらかかったか」の実績 1 件。
// 見積もりを固定値ではなく実績から出すために使う。MessageCount は評価対象セッションの
// 規模で、台本の長さ（＝入力トークン）とよく相関するため区分に使う。
type EvalCostSample struct {
	CostUSD      float64
	MessageCount int
}

// EvalRunStats は期間内の評価実行の集計。
type EvalRunStats struct {
	Total          int
	Succeeded      int
	Failed         int
	CostUSD        float64 // 失敗した試行のぶんも含む、実際に使った額
	FailuresByKind map[string]int
}

// SessionRow は一覧・集計向けの軽量なセッション情報。
// SessionByID のようにメッセージ全件を復元するコストをかけずに一覧表示や集計に使う。
type SessionRow struct {
	SessionID       string
	ProjectLabel    string
	ProjectPath     string
	Worktree        string // ワークツリー配下のセッションのみ。ProjectPath は元のプロジェクトを指す
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
