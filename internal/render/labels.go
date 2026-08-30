// Package render の日本語ラベル変換テーブル。
//
// judge / synth が返す評価軸の値は英語の列挙値（achieved / black_box / over など）だが、
// レポート本文（Markdown・HTML の人が読む部分）にそのまま出すと意味が伝わらない
// （ユーザーレビュー: 「評価軸の数字だけ見ても結局どういうことか分からない」）。
// このファイルはその変換を一箇所に集約する。
//
// 対象は internal/model.Eval の各軸、Retro/ProjectReview の Verdict、model.ActionStatus。
// サイドカー YAML・HTML の data-* 相当の機械可読部分（front matter の元データ、
// テーブル行に埋め込む生の値そのもの）はここを通さず英語のまま保つこと。
// 変換するのは「人が読む表示文字列」だけ。
//
// 変換表に無い値が来ても落とさない（jpLabel は未知のキーをそのまま返す）。
package render

// outcomeLabels は Eval.Outcome（達成度）の表示ラベル。
var outcomeLabels = map[string]string{
	"achieved":    "達成",
	"partial":     "部分達成",
	"abandoned":   "放棄",
	"exploratory": "探索的",
}

// artifactValueLabels は Eval.ArtifactValue（成果物価値）の表示ラベル。
var artifactValueLabels = map[string]string{
	"durable":   "資産として残る",
	"transient": "その場限り",
	"none":      "成果物なし",
}

// interventionCostLevelLabels は Eval.InterventionCost.Level（介入コスト）の表示ラベル。
var interventionCostLevelLabels = map[string]string{
	"low":    "低い（ほぼ任せられた）",
	"medium": "中程度（軌道修正あり）",
	"high":   "高い（頻繁な介入）",
}

// modelFitVerdictLabels は Eval.ModelFit.Verdict（モデル適合）の表示ラベル。
var modelFitVerdictLabels = map[string]string{
	"over":        "過剰",
	"appropriate": "適切",
	"under":       "力不足",
}

// ownershipLevelLabels は Eval.Ownership.Level（主体性）の表示ラベル。
var ownershipLevelLabels = map[string]string{
	"understood": "理解して検収",
	"partial":    "部分的理解",
	"black_box":  "理解せず委譲",
}

// learningValueLabels は Eval.LearningValue（学び）の表示ラベル。
var learningValueLabels = map[string]string{
	"none": "学びなし",
	"some": "多少の学び",
	"high": "大きな学び",
}

// goalCategoryLabels は Eval.GoalCategory（目標カテゴリ）の表示ラベル。
var goalCategoryLabels = map[string]string{
	"feature":    "機能追加",
	"bugfix":     "不具合修正",
	"research":   "調査・検討",
	"automation": "自動化",
	"writing":    "文章作成",
	"ops":        "運用作業",
	"learning":   "学習",
	"other":      "その他",
}

// confidenceLabels は Eval.Confidence（確信度）の表示ラベル。
var confidenceLabels = map[string]string{
	"low":    "低い",
	"medium": "中程度",
	"high":   "高い",
}

// verdictLabels は Retro.Verdict / ProjectReview.Verdict（コストに見合ったか）の表示ラベル。
var verdictLabels = map[string]string{
	"worth_it":          "コストに見合った",
	"mixed":             "一部見合った",
	"not_worth_it":      "コストに見合わなかった",
	"insufficient_data": "判断材料不足",
}

// actionStatusLabels は model.ActionStatus（改善提案の状態）の表示ラベル。
var actionStatusLabels = map[string]string{
	"open":    "未着手",
	"done":    "完了",
	"dropped": "見送り",
	"expired": "期限切れ",
}

// evalFailureKindLabels は store.EvalFailure*（評価失敗の分類）の表示ラベル。
// render は store パッケージに依存しない方針のため、値は文字列キーとして直接持つ
// （store.EvalFailureRateLimit 等の定数は参照しない）。
var evalFailureKindLabels = map[string]string{
	"rate_limit": "レート制限",
	"timeout":    "タイムアウト",
	"schema":     "スキーマ不適合",
	"save":       "結果の保存失敗",
	"other":      "その他",
}

// jpLabel は table から key の日本語表示を引く。無ければ key をそのまま返す
// （未知の値が来ても落とさない・情報を欠落させないため）。空文字はそのまま空文字。
func jpLabel(table map[string]string, key string) string {
	if key == "" {
		return ""
	}
	if v, ok := table[key]; ok {
		return v
	}
	return key
}

func outcomeJP(v string) string          { return jpLabel(outcomeLabels, v) }
func artifactValueJP(v string) string    { return jpLabel(artifactValueLabels, v) }
func interventionCostJP(v string) string { return jpLabel(interventionCostLevelLabels, v) }
func modelFitJP(v string) string         { return jpLabel(modelFitVerdictLabels, v) }
func ownershipJP(v string) string        { return jpLabel(ownershipLevelLabels, v) }
func learningValueJP(v string) string    { return jpLabel(learningValueLabels, v) }
func goalCategoryJP(v string) string     { return jpLabel(goalCategoryLabels, v) }
func confidenceJP(v string) string       { return jpLabel(confidenceLabels, v) }
func actionStatusJP(v string) string     { return jpLabel(actionStatusLabels, v) }
func evalFailureKindJP(v string) string  { return jpLabel(evalFailureKindLabels, v) }

// verdictJP は Retro.Verdict / ProjectReview.Verdict の表示ラベル。
// 空文字は「未設定」ではなく「-」として扱う（表・見出しでの空欄を分かりやすくするため）。
func verdictJP(v string) string {
	if v == "" {
		return "-"
	}
	return jpLabel(verdictLabels, v)
}
