// Package rollup はセッション単位の評価と使用量を、日次・任意期間へ集約する。
// ここで定義する型はそのまま Markdown のフロントマターと HTML の入力になるため、
// JSON タグは安定した契約として扱う（変更するとレポートの再集計が壊れる）。
package rollup

import (
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// Daily は 1 日分の集計結果。store の daily_rollups にこの JSON がそのまま入る。
type Daily struct {
	Date        string        `json:"date"` // YYYY-MM-DD（ローカルタイム基準）
	GeneratedAt time.Time     `json:"generated_at"`
	Totals      Totals        `json:"totals"`
	ByModel     []ModelUsage  `json:"by_model"`
	ByProject   []ProjectStat `json:"by_project"`
	Sessions    []SessionCard `json:"sessions"`
	Facets      Facets        `json:"facets"`
	Narrative   Narrative     `json:"narrative"` // 日報の本文（AI 生成）
	Retro       Retro         `json:"retro"`     // 振り返りの本文と改善提案（AI 生成）
	Meta        Meta          `json:"meta"`
}

// Totals はその日の合計。対話セッションと自動実行を分けて持つ。
// 自動実行（cron / SDK）を混ぜると対話の傾向が歪むため、レポートで別掲できるようにする。
type Totals struct {
	Sessions            int     `json:"sessions"`
	InteractiveSessions int     `json:"interactive_sessions"`
	AutomatedSessions   int     `json:"automated_sessions"` // entrypoint が非対話のもの
	SidechainSessions   int     `json:"sidechain_sessions"` // サブエージェント
	DurationMinutes     float64 `json:"duration_minutes"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheWriteTokens    int64   `json:"cache_write_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	// CacheReuseRatio は cache_read / cache_write。キャッシュ読み取りは
	// 未キャッシュ入力の 0.1 倍、書き込みは 1.25〜2 倍なので、この比が高いほど
	// 文脈を安く運べている。低いと文脈が毎回作り直されている（無駄）。
	// cache_write が 0 なら -1。
	CacheReuseRatio float64 `json:"cache_reuse_ratio"`
	// UnpricedEvents は単価が引けなかった usage の件数。0 でなければ CostUSD は過小評価。
	UnpricedEvents int `json:"unpriced_events"`
}

// ModelUsage はモデル別の使用量と金額。「どのモデルに金が消えたか」の一次データ。
type ModelUsage struct {
	Model            string  `json:"model"`
	Sessions         int     `json:"sessions"`
	Responses        int     `json:"responses"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Priced           bool    `json:"priced"` // false なら単価未登録。金額を信用しない
}

// ProjectStat はプロジェクト別の集計。時間と金の配分を見るために使う。
type ProjectStat struct {
	ProjectPath     string  `json:"project_path"`
	ProjectLabel    string  `json:"project_label"`
	Sessions        int     `json:"sessions"`
	DurationMinutes float64 `json:"duration_minutes"`
	CostUSD         float64 `json:"cost_usd"`
	// CostShare はその日の総コストに占める割合（0..1）。どこに金が向いたかを
	// 絶対額ではなく配分として見るために持つ。
	CostShare float64 `json:"cost_share"`
	Goal      string  `json:"goal,omitempty"` // 設定に書かれたこのプロジェクトの目標

	// 以下はプロジェクト単位の振り返りのために持つ。日次全体の分布だけでは
	// 「どのプロジェクトで何が起きたか」が分からず、改善行動に繋がらないため。
	EvaluatedSessions int    `json:"evaluated_sessions"`
	Facets            Facets `json:"facets"`
	// AchievedRatio は achieved / 評価済み。評価が 0 件なら -1（0% と区別する）。
	AchievedRatio float64 `json:"achieved_ratio"`
	// Highlights は個別に載せる価値があるセッション（コスト・時間の大きい順）。
	Highlights []SessionCard `json:"highlights"`
	// RolledUp は小さすぎて個別に載せる価値が無いセッションの合計。
	// 価値への寄与が薄い作業でレポートを埋めないための仕組み。
	RolledUp RolledUpGroup `json:"rolled_up"`
	// Review は AI が書くこのプロジェクトの定性所見。Synthesize が埋める。
	Review ProjectReview `json:"review"`
}

// RolledUpGroup は「丸めた」セッション群の合計。個別には載せないが、
// 合計は必ず出す（隠すのではなく畳む）。
type RolledUpGroup struct {
	Sessions        int     `json:"sessions"`
	DurationMinutes float64 `json:"duration_minutes"`
	CostUSD         float64 `json:"cost_usd"`
	// Reason は丸めた基準（しきい値）を人間に説明するための文言。
	Reason string `json:"reason,omitempty"`
}

// ProjectReview はプロジェクト単位の定性所見。数字の読み上げではなく、
// 「このコストに見合った価値が出たか」「次にどこを変えるか」を書く。
type ProjectReview struct {
	// Verdict は worth_it / mixed / not_worth_it / insufficient_data のいずれか。
	Verdict string `json:"verdict"`
	// Summary はコストに見合ったかどうかの判断とその理由。
	Summary string `json:"summary"`
	// Improvement はこのプロジェクトで次に変えること。無ければ空。
	Improvement string `json:"improvement,omitempty"`
}

// SessionCard は 1 セッションの要約。日報・振り返りの根拠として並べる。
type SessionCard struct {
	SessionID       string      `json:"session_id"`
	ProjectLabel    string      `json:"project_label"`
	Title           string      `json:"title"` // ai-title / サブエージェントの description / 空
	FirstPrompt     string      `json:"first_prompt"`
	StartedAt       time.Time   `json:"started_at"`
	DurationMinutes float64     `json:"duration_minutes"`
	IsSidechain     bool        `json:"is_sidechain"`
	Entrypoint      string      `json:"entrypoint"`
	Models          []string    `json:"models"`
	CostUSD         float64     `json:"cost_usd"` // このセッション自身のコスト（子を含まない）
	Priced          bool        `json:"priced"`
	EvidenceCount   int         `json:"evidence_count"`
	Eval            *model.Eval `json:"eval,omitempty"` // 未評価なら nil

	// サブエージェント（sidechain）は独立したセッションとしては扱わず、
	// 委譲元である親セッションにコストを計上する。子は情報量が少なく、
	// 単独で並べても価値の判断材料にならないため。
	ChildSessions int     `json:"child_sessions"`
	ChildCostUSD  float64 `json:"child_cost_usd"`
	// TotalCostUSD は CostUSD + ChildCostUSD。レポートはこちらを使う。
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// Facets は評価軸ごとの分布。列挙値をキーにした件数で持つ。
// 軸を増やしてもこの形のままレポート側が対応できるよう、map で持つ。
type Facets struct {
	Outcome          map[string]int `json:"outcome"`           // achieved / partial / abandoned / exploratory
	ArtifactValue    map[string]int `json:"artifact_value"`    // durable / transient / none
	InterventionCost map[string]int `json:"intervention_cost"` // low / medium / high
	ModelFit         map[string]int `json:"model_fit"`         // over / appropriate / under
	Ownership        map[string]int `json:"ownership"`         // understood / partial / black_box
	LearningValue    map[string]int `json:"learning_value"`    // none / some / high
	GoalCategory     map[string]int `json:"goal_category"`
	Confidence       map[string]int `json:"confidence"`
	ReworkOccurred   int            `json:"rework_occurred"`
}

// Narrative は日報の本文。「今日何を成し遂げたか」の記録であり、人に共有できる粒度で書く。
type Narrative struct {
	Headline   string   `json:"headline"`   // 1 行要約
	Body       string   `json:"body"`       // Markdown 本文
	Highlights []string `json:"highlights"` // 箇条書きの成果
}

// Retro は振り返りの本文。金と時間の行方、やり方の改善、前回提案の検証結果。
type Retro struct {
	// Headline は「今日は投じたコストに見合ったか」への 1 行の答え。
	// レポートはこれを最初に出す。数字はその根拠として後ろに置く。
	Headline string `json:"headline"`
	// Verdict は worth_it / mixed / not_worth_it / insufficient_data のいずれか。
	Verdict         string           `json:"verdict"`
	Body            string           `json:"body"`             // Markdown 本文
	CostObservation string           `json:"cost_observation"` // コスト対価値の所見
	Proposals       []Proposal       `json:"proposals"`        // 新しい改善提案（3件程度）
	Verifications   []ActionVerdict  `json:"verifications"`    // 未決着だった過去提案の検証結果
	Outliers        []OutlierFinding `json:"outliers"`         // コスト対価値が外れているセッション
	// ProjectReviews はプロジェクト単位の所見。キーは ProjectPath。
	// Synthesize がこれを埋め、集計側が ProjectStat.Review へ反映する。
	ProjectReviews map[string]ProjectReview `json:"project_reviews"`
}

// Proposal は新しく生まれた改善提案。store の actions テーブルに登録される。
type Proposal struct {
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Category string `json:"category"`
}

// ActionVerdict は過去の改善提案がその後どうなったかの判定。
type ActionVerdict struct {
	ActionID int64  `json:"action_id"`
	Title    string `json:"title"`
	Status   string `json:"status"`  // model.ActionStatus の値
	Verdict  string `json:"verdict"` // なぜそう判定したかの根拠
}

// OutlierFinding はコストに見合う価値が得られなかった（あるいは逆に効率が良かった）セッション。
type OutlierFinding struct {
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd"`
	Reason    string  `json:"reason"`
}

// Meta は集計そのものの信頼性に関わる情報。レポートの但し書きに使う。
type Meta struct {
	PromptVersion       string   `json:"prompt_version"`
	UnknownModels       []string `json:"unknown_models"`       // 単価を引けなかったモデル名
	UnevaluatedSessions int      `json:"unevaluated_sessions"` // 評価に失敗・未実施のセッション数
	MissingTranscripts  int      `json:"missing_transcripts"`  // 本文が取り込めていないセッション数
	JudgeCostUSD        float64  `json:"judge_cost_usd"`       // この集計を作るのにかかった評価コスト
	JudgeSessionIDs     []string `json:"judge_session_ids"`    // 評価実行自体のセッション ID（集計対象から除外する）
}

// Series は任意期間の HTML レポート用に、日次の値を時系列で並べたもの。
// 日次メタを級数として積み上げるため、Daily をそのまま持たず軽量な点の列にする。
type Series struct {
	From   string  `json:"from"` // YYYY-MM-DD
	To     string  `json:"to"`
	Points []Point `json:"points"`
	// ByModel は期間全体のモデル別合計。構成比の推移とは別に、期間の総額を出すために持つ。
	ByModel []ModelUsage `json:"by_model"`
	// Actions は期間内に生まれた改善提案とその消化状況。
	Actions []model.Action `json:"actions"`
	// EvalHealth は期間内の評価実行そのものの健全性。集計元が DB の実行記録で
	// 日次ロールアップからは作れないため、BuildSeries ではなく呼び出し側が入れる。
	// nil なら表示しない（記録が無い期間や、古い DB で評価した期間）。
	EvalHealth *EvalHealth `json:"eval_health,omitempty"`
}

// EvalHealth は期間内に行われた評価の成否とコスト。
// このツールは「評価そのものが本末転倒になっていないか」を自己監視する前提なので、
// 評価が失敗し続けていること自体が見えるようにする。
type EvalHealth struct {
	Total          int            `json:"total"`
	Succeeded      int            `json:"succeeded"`
	Failed         int            `json:"failed"`
	CostUSD        float64        `json:"cost_usd"` // 失敗した試行のぶんも含む
	FailuresByKind map[string]int `json:"failures_by_kind,omitempty"`
}

// Point は 1 日分の集計値。HTML のグラフはこの列だけを見る。
type Point struct {
	Date            string             `json:"date"`
	Sessions        int                `json:"sessions"`
	DurationMinutes float64            `json:"duration_minutes"`
	CostUSD         float64            `json:"cost_usd"`
	CostByModel     map[string]float64 `json:"cost_by_model"`
	Outcome         map[string]int     `json:"outcome"`
	ModelFit        map[string]int     `json:"model_fit"`
	Ownership       map[string]int     `json:"ownership"`
	// AchievedRatio は achieved / 評価済みセッション数。評価が 0 件の日は -1 を入れ、
	// 「0%」と「データなし」を区別できるようにする。
	AchievedRatio float64 `json:"achieved_ratio"`
}
