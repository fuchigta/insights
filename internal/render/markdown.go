package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fuchigta/insights/internal/rollup"
)

// firstPromptMaxRunes / titleMaxRunes は要約列などに使う切り詰め長。
// 生ログの抜粋は載せない方針だが、ユーザー自身が書いた FirstPrompt や
// セッションタイトルは短ければ載せてよいので、長すぎる場合だけ切り詰める。
const (
	firstPromptMaxRunes = 60
	titleMaxRunes       = 60
)

// RenderDaily は日報（その日何を成し遂げたかの記録）を Markdown にする。
// 人に共有できる粒度に留め、コスト対価値の分析や反省は入れない（それは振り返り側）。
func RenderDaily(d *rollup.Daily) ([]byte, error) {
	miniFM := buildMiniFrontMatter(d)
	fmBlock, err := marshalMiniFrontMatter(miniFM)
	if err != nil {
		return nil, fmt.Errorf("日報のフロントマター生成に失敗しました: %w", err)
	}

	date := "?"
	headline := ""
	body := ""
	var highlights []string
	var byProject []rollup.ProjectStat
	if d != nil {
		if d.Date != "" {
			date = d.Date
		}
		headline = strings.TrimSpace(d.Narrative.Headline)
		body = strings.TrimRight(d.Narrative.Body, "\n")
		highlights = d.Narrative.Highlights
		byProject = d.ByProject
	}

	var b strings.Builder
	b.WriteString(fmBlock)
	b.WriteString("\n")

	if headline != "" {
		fmt.Fprintf(&b, "# %s %s\n\n", date, headline)
	} else {
		fmt.Fprintf(&b, "# %s\n\n", date)
	}

	if body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}

	b.WriteString("## ハイライト\n\n")
	if len(highlights) == 0 {
		b.WriteString("特筆すべきハイライトはありません。\n\n")
	} else {
		for _, h := range highlights {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", h)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 今日触れたもの\n\n")
	writeProjectTable(&b, byProject)

	return []byte(b.String()), nil
}

// writeProjectTable は「今日触れたもの」用のプロジェクト別要約テーブルを書く。
// CostShare（総コストに占める割合）と、RolledUp（丸めたセッション）がある場合の
// 内訳注記を添える（個別のセッションは列挙しない）。
func writeProjectTable(b *strings.Builder, projects []rollup.ProjectStat) {
	if len(projects) == 0 {
		b.WriteString("今日は記録されたセッションがありません。\n\n")
		return
	}
	sorted := sortedByCostDesc(projects)

	b.WriteString("| プロジェクト | セッション数 | 時間 | コスト | コスト比率 | 内訳 |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, p := range sorted {
		fmt.Fprintf(b, "| %s | %d | %s | %s | %s | %s |\n",
			escapeTableCell(p.ProjectLabel),
			p.Sessions,
			formatDuration(p.DurationMinutes),
			formatMoneyPlain(p.CostUSD),
			formatRatioPercent(p.CostShare),
			escapeTableCell(rolledUpNote(p.RolledUp)),
		)
	}
	b.WriteString("\n")
}

// sortedByCostDesc は ProjectStat をコスト降順（同額ならラベル昇順）に並べた新しいスライスを返す。
// 決定的な順序を保証するためのヘルパ（日報・振り返りの両方で使う）。
func sortedByCostDesc(projects []rollup.ProjectStat) []rollup.ProjectStat {
	sorted := make([]rollup.ProjectStat, len(projects))
	copy(sorted, projects)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CostUSD != sorted[j].CostUSD {
			return sorted[i].CostUSD > sorted[j].CostUSD
		}
		return sorted[i].ProjectLabel < sorted[j].ProjectLabel
	})
	return sorted
}

// rolledUpNote は RolledUpGroup を「その他 N 件」の 1 行に畳んだ表現にする。
// 個別のセッションは列挙しない（ユーザーレビュー: 価値に薄いセッションでレポートを埋めない）。
func rolledUpNote(g rollup.RolledUpGroup) string {
	if g.Sessions <= 0 {
		return ""
	}
	note := fmt.Sprintf("その他 %d 件（%s / %s）", g.Sessions, formatDuration(g.DurationMinutes), formatMoneyPlain(g.CostUSD))
	if reason := strings.TrimSpace(g.Reason); reason != "" {
		note += "：" + reason
	}
	return note
}

// RenderRetro は振り返り（金と時間の行方・やり方の改善）を Markdown にする。
//
// 構成は「結論が先、データは根拠として後」。まず Retro.Headline / Verdict で
// 今日投じたコストに見合ったかを答え、次にプロジェクト単位で定量・定性を振り返る。
// 評価軸の全体分布は末尾近くに「参考」として置く（数字の羅列を先頭に出さない）。
func RenderRetro(d *rollup.Daily) ([]byte, error) {
	miniFM := buildMiniFrontMatter(d)
	fmBlock, err := marshalMiniFrontMatter(miniFM)
	if err != nil {
		return nil, fmt.Errorf("振り返りのフロントマター生成に失敗しました: %w", err)
	}

	date := "?"
	var retro rollup.Retro
	var facets rollup.Facets
	var byProject []rollup.ProjectStat
	var meta rollup.Meta
	if d != nil {
		if d.Date != "" {
			date = d.Date
		}
		retro = d.Retro
		facets = d.Facets
		byProject = d.ByProject
		meta = d.Meta
	}

	var b strings.Builder
	b.WriteString(fmBlock)
	b.WriteString("\n")
	fmt.Fprintf(&b, "# %s 振り返り\n\n", date)

	writeRetroConclusion(&b, retro)

	b.WriteString("## プロジェクト別振り返り\n\n")
	writeProjectReviews(&b, byProject)

	b.WriteString("## 参考\n\n")
	writeOverallNote(&b, retro.Body)
	writeFacets(&b, facets)

	b.WriteString("## コストに見合わなかったもの\n\n")
	writeOutliers(&b, retro.Outliers)

	b.WriteString("## コスト対価値の所見\n\n")
	if obs := strings.TrimSpace(retro.CostObservation); obs != "" {
		b.WriteString(obs)
		b.WriteString("\n\n")
	} else {
		b.WriteString("記録なし。\n\n")
	}

	b.WriteString("## 次にやること\n\n")
	writeProposals(&b, retro.Proposals)

	b.WriteString("## 前回までの提案の検証結果\n\n")
	writeVerifications(&b, retro.Verifications)

	writeCaveats(&b, facets, meta)

	return []byte(b.String()), nil
}

// writeRetroConclusion は冒頭の「結論」セクションを書く。今日投じたコストに
// 見合ったかへの 1 行の答え（Headline）と、その区分（Verdict）を最初に出す。
func writeRetroConclusion(b *strings.Builder, retro rollup.Retro) {
	b.WriteString("## 結論\n\n")
	headline := strings.TrimSpace(retro.Headline)
	if headline == "" {
		headline = "記録なし。"
	}
	fmt.Fprintf(b, "**判定: %s**\n\n%s\n\n", verdictJP(retro.Verdict), headline)
}

// writeProjectReviews はプロジェクトごとの振り返りを、コスト降順に書く。
// 各プロジェクトについて定量（セッション数・時間・コスト・コスト比率・達成率）と
// 定性（ProjectReview）、個別に語る価値があるセッション（Highlights）、
// 丸めたセッションの合計（RolledUp）を出す。
func writeProjectReviews(b *strings.Builder, projects []rollup.ProjectStat) {
	if len(projects) == 0 {
		b.WriteString("今日は記録されたプロジェクトがありません。\n\n")
		return
	}
	sorted := sortedByCostDesc(projects)

	// 個別に載せる価値のあるセッションが 1 件も無いプロジェクト（＝全セッションが
	// 丸められた＝その日の価値に直接寄与していない）は節を立てず、末尾で 1 つに
	// まとめる。セッション単位の丸めだけでは、一時ディレクトリでの短い作業などが
	// プロジェクトの節としてレポートの大半を占めてしまうため。
	var shown, minor []rollup.ProjectStat
	for _, p := range sorted {
		if len(p.Highlights) == 0 {
			minor = append(minor, p)
			continue
		}
		shown = append(shown, p)
	}
	if len(shown) == 0 {
		b.WriteString("個別に取り上げるだけの規模のプロジェクトはありませんでした。\n\n")
		writeMinorProjects(b, minor)
		return
	}

	for _, p := range shown {
		fmt.Fprintf(b, "### %s\n\n", orDash(p.ProjectLabel))

		fmt.Fprintf(b, "- セッション数: %d 件（評価済み %d 件）\n", p.Sessions, p.EvaluatedSessions)
		fmt.Fprintf(b, "- 所要時間: %s\n", formatDuration(p.DurationMinutes))
		fmt.Fprintf(b, "- コスト: %s（総コストの %s）\n", formatMoneyPlain(p.CostUSD), formatRatioPercent(p.CostShare))
		fmt.Fprintf(b, "- 達成率: %s\n\n", formatAchievedRatio(p.AchievedRatio))

		fmt.Fprintf(b, "**評価: %s**\n\n", verdictJP(p.Review.Verdict))
		if summary := strings.TrimSpace(p.Review.Summary); summary != "" {
			b.WriteString(summary)
			b.WriteString("\n\n")
		} else {
			b.WriteString("所見は記録されていません。\n\n")
		}
		if improvement := strings.TrimSpace(p.Review.Improvement); improvement != "" {
			fmt.Fprintf(b, "次に変えること: %s\n\n", improvement)
		}

		writeProjectHighlights(b, p.Highlights)

		if note := rolledUpNote(p.RolledUp); note != "" {
			fmt.Fprintf(b, "%s\n\n", note)
		}
	}

	writeMinorProjects(b, minor)
}

// writeMinorProjects は個別に取り上げるほどの規模が無かったプロジェクトを
// 1 つの節にまとめて書く。件数と合計は必ず出す（隠すのではなく畳む）。
//
// セッション単位の丸めだけでは足りない。一時ディレクトリでの短い作業などは
// プロジェクトごと価値に寄与していないため、節を立てるとレポートの大半が
// それで埋まってしまう。
func writeMinorProjects(b *strings.Builder, minor []rollup.ProjectStat) {
	if len(minor) == 0 {
		return
	}
	var sessions int
	var minutes, cost, share float64
	for _, p := range minor {
		sessions += p.Sessions
		minutes += p.DurationMinutes
		cost += p.CostUSD
		share += p.CostShare
	}
	b.WriteString("### その他のプロジェクト\n\n")
	fmt.Fprintf(b, "個別に取り上げるだけの規模が無かったプロジェクト %d 件（セッション %d 件 / %s / %s、総コストの %s）。\n\n",
		len(minor), sessions, formatDuration(minutes), formatMoneyPlain(cost), formatRatioPercent(share))
	b.WriteString("| プロジェクト | セッション数 | 時間 | コスト |\n| --- | ---: | ---: | ---: |\n")
	for _, p := range minor {
		fmt.Fprintf(b, "| %s | %d | %s | %s |\n",
			escapeTableCell(orDash(p.ProjectLabel)), p.Sessions,
			formatDuration(p.DurationMinutes), formatMoneyPlain(p.CostUSD))
	}
	b.WriteString("\n")
}

// writeProjectHighlights はプロジェクト内で個別に語る価値があるセッションを表にする。
// ChildSessions が 1 件以上あるセッション（サブエージェントへの委譲を含む）は、
// 要約列に「委譲 N 件を含む」と分かるように注記する。金額は子を含めた TotalCostUSD を使う
// （子は独立したセッションとして計上しない方針のため）。
func writeProjectHighlights(b *strings.Builder, highlights []rollup.SessionCard) {
	if len(highlights) == 0 {
		b.WriteString("個別に取り上げるセッションはありません。\n\n")
		return
	}
	sorted := make([]rollup.SessionCard, len(highlights))
	copy(sorted, highlights)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TotalCostUSD != sorted[j].TotalCostUSD {
			return sorted[i].TotalCostUSD > sorted[j].TotalCostUSD
		}
		return sorted[i].SessionID < sorted[j].SessionID
	})

	b.WriteString("| 要約 | モデル | コスト | 達成度 |\n")
	b.WriteString("| --- | --- | ---: | --- |\n")
	for _, s := range sorted {
		summary := sessionSummary(s)
		if s.ChildSessions > 0 {
			summary = fmt.Sprintf("%s（委譲 %d 件を含む）", summary, s.ChildSessions)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n",
			escapeTableCell(summary),
			escapeTableCell(modelsJoined(s.Models)),
			formatMoney(s.TotalCostUSD, s.Priced),
			escapeTableCell(sessionOutcome(s)),
		)
	}
	b.WriteString("\n")
}

// modelsJoined はモデル名の一覧をソートして結合する。空なら「-」。
func modelsJoined(models []string) string {
	if len(models) == 0 {
		return "-"
	}
	ms := make([]string, len(models))
	copy(ms, models)
	sort.Strings(ms)
	return strings.Join(ms, ", ")
}

// writeOverallNote は日全体を横断する所見（Retro.Body）を「参考」セクションの冒頭に書く。
// プロジェクト個別の判断はプロジェクト別振り返りに書いてあるので、ここでは重複させない。
func writeOverallNote(b *strings.Builder, body string) {
	b.WriteString("### 日全体の所感\n\n")
	if s := strings.TrimRight(body, "\n"); strings.TrimSpace(s) != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	} else {
		b.WriteString("記録なし。\n\n")
	}
}

// sessionSummary はセッション一覧の要約列に使う文字列を作る。
// 生ログの抜粋は載せない方針のため、評価があれば AI 生成の OutcomeSummary を使い、
// なければユーザー自身が書いた Title / FirstPrompt を切り詰めて使う。
func sessionSummary(s rollup.SessionCard) string {
	if s.Eval != nil {
		if sum := strings.TrimSpace(s.Eval.OutcomeSummary); sum != "" {
			return sum
		}
	}
	if title := strings.TrimSpace(s.Title); title != "" {
		return truncateRunes(title, titleMaxRunes) + "（未評価）"
	}
	if fp := strings.TrimSpace(s.FirstPrompt); fp != "" {
		return truncateRunes(fp, firstPromptMaxRunes) + "（未評価）"
	}
	return "(要約なし・未評価)"
}

// sessionOutcome はセッションの達成度表示（日本語）。未評価なら「未評価」。
func sessionOutcome(s rollup.SessionCard) string {
	if s.Eval == nil || strings.TrimSpace(s.Eval.Outcome) == "" {
		return "未評価"
	}
	return outcomeJP(s.Eval.Outcome)
}

// writeOutliers は「コストに見合わなかったもの」の一覧を書く。
func writeOutliers(b *strings.Builder, outliers []rollup.OutlierFinding) {
	if len(outliers) == 0 {
		b.WriteString("該当するセッションはありません。\n\n")
		return
	}
	sorted := make([]rollup.OutlierFinding, len(outliers))
	copy(sorted, outliers)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CostUSD != sorted[j].CostUSD {
			return sorted[i].CostUSD > sorted[j].CostUSD
		}
		return sorted[i].SessionID < sorted[j].SessionID
	})
	for _, o := range sorted {
		fmt.Fprintf(b, "- `%s`（%s）: %s\n", o.SessionID, formatMoneyPlain(o.CostUSD), o.Reason)
	}
	b.WriteString("\n")
}

// facetGroup は 1 つの評価軸グループ（分布表示用）。
type facetGroup struct {
	title string
	m     map[string]int
	label func(string) string
}

// writeFacets は評価軸ごとの分布を、母数（評価済みセッション数）付きで書く。
// 単なる数字の羅列を避けるため、件数と割合を併記し、キーは日本語ラベルにする。
// 全体の分布は「参考」情報であり、この節が本文の主役にならないよう見出しは H3 に留める。
func writeFacets(b *strings.Builder, facets rollup.Facets) {
	b.WriteString("### 評価軸の分布\n\n")

	evaluated := 0
	for _, n := range facets.Outcome {
		evaluated += n
	}

	fmt.Fprintf(b, "評価済みセッション: %d 件\n\n", evaluated)

	if evaluated == 0 {
		b.WriteString("評価済みセッションがないため、分布は算出できません。\n\n")
		return
	}

	achieved := facets.Outcome["achieved"]
	fmt.Fprintf(b, "達成率: %s（%s %d / 評価済み %d 件）\n\n",
		formatPercent(achieved, evaluated), outcomeJP("achieved"), achieved, evaluated)

	groups := []facetGroup{
		{"成果", facets.Outcome, outcomeJP},
		{"成果物価値", facets.ArtifactValue, artifactValueJP},
		{"介入コスト", facets.InterventionCost, interventionCostJP},
		{"モデル適合", facets.ModelFit, modelFitJP},
		{"主体性", facets.Ownership, ownershipJP},
		{"学び", facets.LearningValue, learningValueJP},
		{"目標カテゴリ", facets.GoalCategory, goalCategoryJP},
		{"確信度", facets.Confidence, confidenceJP},
	}

	for _, g := range groups {
		fmt.Fprintf(b, "#### %s（母数 %d 件）\n\n", g.title, evaluated)
		counts := facetCounts(g.m)
		if len(counts) == 0 {
			b.WriteString("データなし\n\n")
			continue
		}
		for _, c := range counts {
			fmt.Fprintf(b, "- %s: %d 件（%s）\n", g.label(c.Key), c.Count, formatPercent(c.Count, evaluated))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "手戻り発生: %d 件（%s）\n\n",
		facets.ReworkOccurred, formatPercent(facets.ReworkOccurred, evaluated))
}

// writeProposals は次の改善提案を番号付きで書く。
func writeProposals(b *strings.Builder, proposals []rollup.Proposal) {
	if len(proposals) == 0 {
		b.WriteString("新しい提案はありません。\n\n")
		return
	}
	for i, p := range proposals {
		fmt.Fprintf(b, "%d. **%s**", i+1, orDash(p.Title))
		if cat := strings.TrimSpace(p.Category); cat != "" {
			fmt.Fprintf(b, "（%s）", cat)
		}
		if detail := strings.TrimSpace(p.Detail); detail != "" {
			fmt.Fprintf(b, ": %s", detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// writeVerifications は前回までの提案の検証結果をテーブルで書く。
func writeVerifications(b *strings.Builder, verifications []rollup.ActionVerdict) {
	if len(verifications) == 0 {
		b.WriteString("検証対象の過去提案はありません。\n\n")
		return
	}
	sorted := make([]rollup.ActionVerdict, len(verifications))
	copy(sorted, verifications)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ActionID < sorted[j].ActionID })

	b.WriteString("| 提案 | 判定 | 根拠 |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, v := range sorted {
		status := v.Status
		if s := strings.TrimSpace(v.Status); s != "" {
			status = actionStatusJP(s)
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n",
			escapeTableCell(orDash(v.Title)),
			escapeTableCell(orDash(status)),
			escapeTableCell(orDash(v.Verdict)),
		)
	}
	b.WriteString("\n")
}

// writeCaveats は末尾の但し書きを書く。該当する場合のみ出す
// （常に出すと注意書きとして読み飛ばされるため）。
func writeCaveats(b *strings.Builder, facets rollup.Facets, meta rollup.Meta) {
	evaluated := 0
	for _, n := range facets.Outcome {
		evaluated += n
	}

	var caveats []string
	if evaluated > 0 {
		caveats = append(caveats, "LLM による評価は傾向を見るための目安であり、絶対値ではありません。")
	}
	if len(meta.UnknownModels) > 0 {
		models := sortedCopy(meta.UnknownModels)
		caveats = append(caveats, fmt.Sprintf(
			"次のモデルは単価が未登録のため、この日のコストは過小評価です: %s",
			strings.Join(models, ", ")))
	}
	if meta.UnevaluatedSessions != 0 {
		caveats = append(caveats, fmt.Sprintf(
			"%d 件のセッションが未評価のため、この振り返りは全セッションを網羅していません。",
			meta.UnevaluatedSessions))
	}

	if len(caveats) == 0 {
		return
	}

	b.WriteString("## 但し書き\n\n")
	for _, c := range caveats {
		fmt.Fprintf(b, "- %s\n", c)
	}
}

// RenderMeta は rollup.Daily から、サイドカー YAML（完全な構造化データ）のバイト列を作る。
// 日報・振り返り Markdown の最小フロントマターとは別に、これだけで再集計に必要な
// 情報（rollup.Point 相当・モデル別集計・プロジェクト別集計・Meta）を復元できる。
// ParseSidecar が逆変換にあたる。
func RenderMeta(d *rollup.Daily) ([]byte, error) {
	data, err := marshalSidecarYAML(buildFrontMatter(d))
	if err != nil {
		return nil, fmt.Errorf("サイドカー YAML の生成に失敗しました: %w", err)
	}
	return data, nil
}

// WriteReports は RenderDaily / RenderRetro の結果を outDir 配下の規定パスに書き出し、
// 書いたパスを返す。
//
//	<outDir>/daily/YYYY-MM-DD.md
//	<outDir>/retro/YYYY-MM-DD.md
//
// サイドカー YAML（完全な構造化データ）も併せて書き出すが、このシグネチャは
// 既存の呼び出し元（internal/cli/daily.go）を変更せずに済むよう維持する。
// サイドカーのパスも受け取りたい場合は WriteReportsWithMeta を使うこと。
func WriteReports(outDir string, d *rollup.Daily) (dailyPath, retroPath string, err error) {
	dailyPath, retroPath, _, err = WriteReportsWithMeta(outDir, d)
	return dailyPath, retroPath, err
}

// WriteReportsWithMeta は WriteReports と同じことを行い、加えてサイドカー YAML
// （<outDir>/meta/YYYY-MM-DD.yaml）も書き出してそのパスを返す。
func WriteReportsWithMeta(outDir string, d *rollup.Daily) (dailyPath, retroPath, metaPath string, err error) {
	date := "unknown-date"
	if d != nil && strings.TrimSpace(d.Date) != "" {
		date = d.Date
	}

	dailyBytes, err := RenderDaily(d)
	if err != nil {
		return "", "", "", fmt.Errorf("日報の生成に失敗しました: %w", err)
	}
	retroBytes, err := RenderRetro(d)
	if err != nil {
		return "", "", "", fmt.Errorf("振り返りの生成に失敗しました: %w", err)
	}
	sidecarBytes, err := RenderMeta(d)
	if err != nil {
		return "", "", "", err
	}

	dailyDir := filepath.Join(outDir, "daily")
	retroDir := filepath.Join(outDir, "retro")
	metaDir := filepath.Join(outDir, metaDirName)
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("daily ディレクトリの作成に失敗しました: %w", err)
	}
	if err := os.MkdirAll(retroDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("retro ディレクトリの作成に失敗しました: %w", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("meta ディレクトリの作成に失敗しました: %w", err)
	}

	dailyPath = filepath.Join(dailyDir, date+".md")
	retroPath = filepath.Join(retroDir, date+".md")
	metaPath = filepath.Join(metaDir, date+metaFileExt)

	if err := os.WriteFile(dailyPath, dailyBytes, 0o644); err != nil {
		return "", "", "", fmt.Errorf("日報の書き込みに失敗しました: %w", err)
	}
	if err := os.WriteFile(retroPath, retroBytes, 0o644); err != nil {
		return "", "", "", fmt.Errorf("振り返りの書き込みに失敗しました: %w", err)
	}
	if err := os.WriteFile(metaPath, sidecarBytes, 0o644); err != nil {
		return "", "", "", fmt.Errorf("サイドカー YAML の書き込みに失敗しました: %w", err)
	}

	return dailyPath, retroPath, metaPath, nil
}
