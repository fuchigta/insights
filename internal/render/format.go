// Package render は rollup.Daily / rollup.Series を人が読むレポート（日報・振り返り・
// 任意期間の HTML）に変換する。
//
// フロントマター・サイドカー YAML の組み立ては本ファイルが担当する。Markdown 描画は
// markdown.go、HTML 描画は html.go / html_template.go が担当する。評価軸の英語列挙値の
// 日本語表示ラベルは labels.go に一元化する。
package render

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fuchigta/insights/internal/rollup"
)

// frontMatterDelim は YAML フロントマターの開始・終了に使う区切り行。
const frontMatterDelim = "---"

// metaDirName / metaFileExt はサイドカー YAML の置き場所の規約。
// WriteReports は <outDir>/meta/YYYY-MM-DD.yaml に書き出す。daily/ と retro/ は
// meta/ と同じ階層にあるため、両レポートから見た相対パスは同じ ("../meta/<date>.yaml") になる。
const (
	metaDirName = "meta"
	metaFileExt = ".yaml"
)

// MiniFrontMatter は日報・振り返り Markdown の先頭に埋め込む「最小限」の YAML フロントマター。
//
// ユーザーレビュー「フロントマターが巨大すぎて本文が読めない」を受け、Markdown 本文には
// 人間が本文を読む妨げにならない最小限の情報だけを載せる。完全な構造化データ（モデル別・
// プロジェクト別集計、Meta など）は同じディレクトリのサイドカー YAML
// （<outDir>/meta/YYYY-MM-DD.yaml）に書き出し、Meta フィールドにその相対パスを持つ。
type MiniFrontMatter struct {
	Date            string  `yaml:"date"`
	Sessions        int     `yaml:"sessions"`
	DurationMinutes float64 `yaml:"duration_minutes"`
	CostUSD         float64 `yaml:"cost_usd"`
	// AchievedRatio は achieved / 評価済みセッション数。評価済みが 0 件の日は -1 を入れ、
	// 「0%」と「データなし」を区別できるようにする（rollup.Point.AchievedRatio と同じ規約）。
	AchievedRatio float64 `yaml:"achieved_ratio"`
	PromptVersion string  `yaml:"prompt_version"`
	// Meta はサイドカー YAML（完全な構造化データ）への相対パス。
	Meta string `yaml:"meta"`
}

// FrontMatter はサイドカー YAML（<outDir>/meta/YYYY-MM-DD.yaml）の内容。
//
// ユーザーが明示的に要求している再集計要件のための機械可読データである。
// ここに定義する yaml タグとネスト構造は「サイドカー YAML だけから
// rollup.Point（日付・セッション数・所要時間・コスト・モデル別コスト・
// outcome / model_fit / ownership の分布・達成率）を再構成できる」という
// 契約そのものである。加えてモデル別トークン量・プロジェクト別集計・Meta も
// 復元できるようにする。
//
// 契約なので、既存の yaml タグの変更・削除・型変更は行わないこと
// （行うと、書き出し済みの過去サイドカー YAML からの再集計が壊れる）。
// フィールドの追加は問題ない。
type FrontMatter struct {
	// Date と GeneratedAt は rollup.Daily.Date / GeneratedAt にそのまま対応する。
	Date        string    `yaml:"date"`
	GeneratedAt time.Time `yaml:"generated_at"`

	// --- rollup.Point 相当（必須） ---

	Sessions        int          `yaml:"sessions"`
	DurationMinutes float64      `yaml:"duration_minutes"`
	CostUSD         float64      `yaml:"cost_usd"`
	CostByModel     []ModelCost  `yaml:"cost_by_model"`
	Outcome         []FacetCount `yaml:"outcome"`
	ModelFit        []FacetCount `yaml:"model_fit"`
	Ownership       []FacetCount `yaml:"ownership"`
	// AchievedRatio は achieved / 評価済みセッション数。評価済みが 0 件の日は -1 を入れ、
	// 「0%」と「データなし」を区別できるようにする（rollup.Point.AchievedRatio と同じ規約）。
	AchievedRatio float64 `yaml:"achieved_ratio"`

	// --- Totals の内訳（付加情報） ---

	InteractiveSessions int `yaml:"interactive_sessions"`
	AutomatedSessions   int `yaml:"automated_sessions"`
	SidechainSessions   int `yaml:"sidechain_sessions"`
	UnpricedEvents      int `yaml:"unpriced_events"`

	// --- 評価軸の全分布（rollup.Facets 全体を復元するため） ---

	ArtifactValue    []FacetCount `yaml:"artifact_value"`
	InterventionCost []FacetCount `yaml:"intervention_cost"`
	LearningValue    []FacetCount `yaml:"learning_value"`
	GoalCategory     []FacetCount `yaml:"goal_category"`
	Confidence       []FacetCount `yaml:"confidence"`
	ReworkOccurred   int          `yaml:"rework_occurred"`

	// --- モデル別集計（トークン量を含む） ---

	ByModel []ModelUsageFM `yaml:"by_model"`

	// --- プロジェクト別集計 ---

	ByProject []ProjectStatFM `yaml:"by_project"`

	// --- Meta（集計の信頼性） ---

	PromptVersion       string   `yaml:"prompt_version"`
	UnknownModels       []string `yaml:"unknown_models,omitempty"`
	UnevaluatedSessions int      `yaml:"unevaluated_sessions"`
	MissingTranscripts  int      `yaml:"missing_transcripts"`
	JudgeCostUSD        float64  `yaml:"judge_cost_usd"`
	JudgeSessionIDs     []string `yaml:"judge_session_ids,omitempty"`
}

// ModelCost はモデル名とコストの組。rollup.Point.CostByModel（map）を
// 決定的な順序で YAML に出すためのスライス表現。
type ModelCost struct {
	Model   string  `yaml:"model"`
	CostUSD float64 `yaml:"cost_usd"`
}

// FacetCount は評価軸の列挙値と件数の組。rollup.Facets の各 map を
// 決定的な順序で YAML に出すためのスライス表現。
type FacetCount struct {
	Key   string `yaml:"key"`
	Count int    `yaml:"count"`
}

// ModelUsageFM は rollup.ModelUsage のフロントマター表現。
type ModelUsageFM struct {
	Model            string  `yaml:"model"`
	Sessions         int     `yaml:"sessions"`
	Responses        int     `yaml:"responses"`
	InputTokens      int64   `yaml:"input_tokens"`
	OutputTokens     int64   `yaml:"output_tokens"`
	CacheReadTokens  int64   `yaml:"cache_read_tokens"`
	CacheWriteTokens int64   `yaml:"cache_write_tokens"`
	CostUSD          float64 `yaml:"cost_usd"`
	Priced           bool    `yaml:"priced"`
}

// ProjectStatFM は rollup.ProjectStat のフロントマター表現。
type ProjectStatFM struct {
	ProjectPath     string  `yaml:"project_path"`
	ProjectLabel    string  `yaml:"project_label"`
	Sessions        int     `yaml:"sessions"`
	DurationMinutes float64 `yaml:"duration_minutes"`
	CostUSD         float64 `yaml:"cost_usd"`
	Goal            string  `yaml:"goal,omitempty"`
}

// buildFrontMatter は rollup.Daily から FrontMatter を組み立てる。
// d が nil でも落ちず、ゼロ値相当の FrontMatter を返す。
func buildFrontMatter(d *rollup.Daily) *FrontMatter {
	fm := &FrontMatter{AchievedRatio: -1}
	if d == nil {
		return fm
	}

	fm.Date = d.Date
	fm.GeneratedAt = d.GeneratedAt

	fm.Sessions = d.Totals.Sessions
	fm.DurationMinutes = d.Totals.DurationMinutes
	fm.CostUSD = d.Totals.CostUSD
	fm.InteractiveSessions = d.Totals.InteractiveSessions
	fm.AutomatedSessions = d.Totals.AutomatedSessions
	fm.SidechainSessions = d.Totals.SidechainSessions
	fm.UnpricedEvents = d.Totals.UnpricedEvents

	fm.CostByModel = costByModel(d.ByModel)
	fm.ByModel = modelUsageFM(d.ByModel)
	fm.ByProject = projectStatFM(d.ByProject)

	fm.Outcome = facetCounts(d.Facets.Outcome)
	fm.ArtifactValue = facetCounts(d.Facets.ArtifactValue)
	fm.InterventionCost = facetCounts(d.Facets.InterventionCost)
	fm.ModelFit = facetCounts(d.Facets.ModelFit)
	fm.Ownership = facetCounts(d.Facets.Ownership)
	fm.LearningValue = facetCounts(d.Facets.LearningValue)
	fm.GoalCategory = facetCounts(d.Facets.GoalCategory)
	fm.Confidence = facetCounts(d.Facets.Confidence)
	fm.ReworkOccurred = d.Facets.ReworkOccurred

	fm.AchievedRatio = achievedRatio(d.Facets.Outcome)

	fm.PromptVersion = d.Meta.PromptVersion
	fm.UnknownModels = sortedCopy(d.Meta.UnknownModels)
	fm.UnevaluatedSessions = d.Meta.UnevaluatedSessions
	fm.MissingTranscripts = d.Meta.MissingTranscripts
	fm.JudgeCostUSD = d.Meta.JudgeCostUSD
	fm.JudgeSessionIDs = sortedCopy(d.Meta.JudgeSessionIDs)

	return fm
}

// costByModel は ModelUsage のスライスから、モデル名でソートした ModelCost を作る。
func costByModel(models []rollup.ModelUsage) []ModelCost {
	if len(models) == 0 {
		return nil
	}
	out := make([]ModelCost, 0, len(models))
	for _, m := range models {
		out = append(out, ModelCost{Model: m.Model, CostUSD: m.CostUSD})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// modelUsageFM は ModelUsage のスライスをモデル名でソートしてフロントマター表現に変換する。
func modelUsageFM(models []rollup.ModelUsage) []ModelUsageFM {
	if len(models) == 0 {
		return nil
	}
	out := make([]ModelUsageFM, 0, len(models))
	for _, m := range models {
		out = append(out, ModelUsageFM{
			Model:            m.Model,
			Sessions:         m.Sessions,
			Responses:        m.Responses,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CacheReadTokens:  m.CacheReadTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			CostUSD:          m.CostUSD,
			Priced:           m.Priced,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// projectStatFM は ProjectStat のスライスを ProjectPath でソートしてフロントマター表現に変換する。
func projectStatFM(projects []rollup.ProjectStat) []ProjectStatFM {
	if len(projects) == 0 {
		return nil
	}
	out := make([]ProjectStatFM, 0, len(projects))
	for _, p := range projects {
		out = append(out, ProjectStatFM{
			ProjectPath:     p.ProjectPath,
			ProjectLabel:    p.ProjectLabel,
			Sessions:        p.Sessions,
			DurationMinutes: p.DurationMinutes,
			CostUSD:         p.CostUSD,
			Goal:            p.Goal,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectPath < out[j].ProjectPath })
	return out
}

// facetCounts は評価軸の map[string]int をキーでソートした FacetCount のスライスに変換する。
// map の反復順序をそのまま出力すると非決定的になるため、必ずここを通す。
func facetCounts(m map[string]int) []FacetCount {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]FacetCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, FacetCount{Key: k, Count: m[k]})
	}
	return out
}

// achievedRatio は achieved / 評価済みセッション数を返す。評価済みが 0 件なら -1。
func achievedRatio(outcome map[string]int) float64 {
	total := 0
	for _, n := range outcome {
		total += n
	}
	if total == 0 {
		return -1
	}
	return float64(outcome["achieved"]) / float64(total)
}

// sortedCopy はソート済みの新しいスライスを返す。nil / 空なら nil を返す。
func sortedCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	sort.Strings(out)
	return out
}

// sidecarRelPath は日報・振り返り Markdown（<outDir>/daily|retro/YYYY-MM-DD.md）から見た
// サイドカー YAML（<outDir>/meta/YYYY-MM-DD.yaml）への相対パスを返す。
// daily/ と retro/ は meta/ と同じ階層にあるため、どちらから見ても同じ相対パスになる。
func sidecarRelPath(date string) string {
	if date == "" {
		date = "unknown-date"
	}
	return "../" + metaDirName + "/" + date + metaFileExt
}

// buildMiniFrontMatter は rollup.Daily から、Markdown 本文に埋め込む最小限の
// フロントマターを組み立てる。d が nil でも落ちない。
func buildMiniFrontMatter(d *rollup.Daily) *MiniFrontMatter {
	fm := &MiniFrontMatter{AchievedRatio: -1}
	if d == nil {
		fm.Meta = sidecarRelPath("")
		return fm
	}
	fm.Date = d.Date
	fm.Sessions = d.Totals.Sessions
	fm.DurationMinutes = d.Totals.DurationMinutes
	fm.CostUSD = d.Totals.CostUSD
	fm.AchievedRatio = achievedRatio(d.Facets.Outcome)
	fm.PromptVersion = d.Meta.PromptVersion
	fm.Meta = sidecarRelPath(d.Date)
	return fm
}

// marshalMiniFrontMatter は MiniFrontMatter を "---" で囲んだ YAML ブロックにエンコードする。
// 手書きの文字列連結ではなく yaml.Marshal を通すことで、YAML エスケープの正しさを保証する。
func marshalMiniFrontMatter(fm *MiniFrontMatter) (string, error) {
	data, err := yaml.Marshal(fm)
	if err != nil {
		return "", fmt.Errorf("フロントマターの YAML エンコードに失敗しました: %w", err)
	}
	return wrapFrontMatterBlock(data), nil
}

// wrapFrontMatterBlock は YAML バイト列を "---" 区切りのフロントマターブロック文字列にする。
func wrapFrontMatterBlock(data []byte) string {
	var b strings.Builder
	b.WriteString(frontMatterDelim)
	b.WriteString("\n")
	b.Write(data)
	if !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteString("\n")
	}
	b.WriteString(frontMatterDelim)
	b.WriteString("\n")
	return b.String()
}

// marshalSidecarYAML は FrontMatter（完全な構造化データ）を、装飾の無いプレーンな
// YAML バイト列にエンコードする。サイドカーファイルはそのまま YAML ファイルとして
// 読めるべきなので、Markdown フロントマターのような "---" 区切りは付けない。
func marshalSidecarYAML(fm *FrontMatter) ([]byte, error) {
	data, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("サイドカー YAML のエンコードに失敗しました: %w", err)
	}
	return data, nil
}

// ParseFrontMatter は Markdown 本文の先頭にある最小限の YAML フロントマターを取り出して
// パースする。フロントマターが見つからない場合はエラーを返す。
// 完全な構造化データからの再集計には ParseSidecar を使うこと。
func ParseFrontMatter(md []byte) (*MiniFrontMatter, error) {
	yamlPart, err := extractFrontMatterBlock(md)
	if err != nil {
		return nil, err
	}
	var fm MiniFrontMatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return nil, fmt.Errorf("フロントマターの YAML デコードに失敗しました: %w", err)
	}
	return &fm, nil
}

// extractFrontMatterBlock は Markdown 本文の先頭 "---" 〜 "---" の中身（YAML 部分）を返す。
func extractFrontMatterBlock(md []byte) (string, error) {
	raw := string(md)
	if !strings.HasPrefix(raw, frontMatterDelim+"\n") {
		return "", fmt.Errorf("フロントマターが見つかりません（先頭が %q ではありません）", frontMatterDelim)
	}
	rest := raw[len(frontMatterDelim)+1:]
	end := strings.Index(rest, "\n"+frontMatterDelim)
	if end < 0 {
		return "", fmt.Errorf("フロントマターの終端 %q が見つかりません", frontMatterDelim)
	}
	return rest[:end], nil
}

// ParseSidecar はサイドカー YAML（装飾の無いプレーンな YAML バイト列）をパースして
// FrontMatter を復元する。「サイドカー YAML だけから rollup.Point 等を再構成できる」
// という要件のロールトリップの片側を担う。
func ParseSidecar(data []byte) (*FrontMatter, error) {
	var fm FrontMatter
	if err := yaml.Unmarshal(data, &fm); err != nil {
		return nil, fmt.Errorf("サイドカー YAML のデコードに失敗しました: %w", err)
	}
	return &fm, nil
}

// --- 本文の整形ヘルパ ---

// formatMoney は評価対象のコストを固定桁の金額表示にする。
// priced が false の場合は「0 円」と誤読させないよう「単価未登録」と明示する。
func formatMoney(cost float64, priced bool) string {
	if !priced {
		return "単価未登録"
	}
	return formatMoneyPlain(cost)
}

// formatMoneyPlain は Priced フラグを持たない集計値（Totals / ProjectStat など）向けの
// 固定桁金額表示。
func formatMoneyPlain(cost float64) string {
	return fmt.Sprintf("$%.4f", cost)
}

// formatDuration は分単位の所要時間を読みやすい固定桁表示にする。
func formatDuration(minutes float64) string {
	return fmt.Sprintf("%.1f分", minutes)
}

// formatPercent は分子・分母から百分率を作る。分母が 0 なら「-」を返す（ゼロ除算回避）。
func formatPercent(n, denom int) string {
	if denom <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(denom))
}

// formatRatioPercent は 0..1 の比率を百分率表示にする。
func formatRatioPercent(ratio float64) string {
	return fmt.Sprintf("%.1f%%", ratio*100)
}

// formatAchievedRatio は達成率を表示用文字列にする。ratio が -1（評価データなし）の場合、
// 「0%」と誤読させないよう「評価データなし」と明示する
// （rollup.ProjectStat.AchievedRatio / rollup.Point.AchievedRatio と同じ -1 規約）。
func formatAchievedRatio(ratio float64) string {
	if ratio < 0 {
		return "評価データなし"
	}
	return formatRatioPercent(ratio)
}

// truncateRunes は s をルーン単位で maxRunes に切り詰め、切り詰めた場合は末尾に "…" を付ける。
func truncateRunes(s string, maxRunes int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes]) + "…"
}

// escapeTableCell は Markdown テーブルのセルに埋め込む文字列をエスケープする。
// パイプはテーブル区切りと衝突するためエスケープし、改行はスペースに畳む。
func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// orDash は空文字列なら "-" を返す。表の空セルを分かりやすくするためのヘルパ。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
