// Package render の HTML 描画部分。rollup.Series（任意期間の日次系列）を
// 外部リソースに一切依存しない単一の自己完結 HTML に変換する。
//
// グラフは Chart.js 等の JS ライブラリを使わず、Go 側で座標を計算したインライン SVG
// として描画する。JS は使わない（ホバーは SVG <title> によるブラウザ標準ツールチップで
// 代替する）。html/template を通すことで、日本語・<script>・Windows パスなどを含む
// 自由記述テキストのエスケープを保証する（文字列連結で HTML を組み立てない）。
package render

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/rollup"
)

// HTMLOptions は RenderHTML / WriteHTML の挙動を制御するオプション。
type HTMLOptions struct {
	Title string // 空なら期間から自動生成する
}

// --- チャート寸法（全チャート共通） ---

const (
	chartW  = 720.0
	chartH  = 260.0
	marginL = 44.0
	marginR = 12.0
	marginT = 12.0
	marginB = 26.0
	plotW   = chartW - marginL - marginR
	plotH   = chartH - marginT - marginB

	maxBarWidth  = 24.0 // marks-and-anatomy: 棒は 24px 以下
	maxXLabels   = 8
	markerRadius = 4.0 // marks-and-anatomy: マーカーは r >= 4
)

// RenderHTML は任意期間の Series を自己完結した単一 HTML にする。
// s が nil でもパニックせず、意味の通る HTML を返す。
func RenderHTML(s *rollup.Series, opt HTMLOptions) ([]byte, error) {
	data := buildPageData(s, opt)

	var buf bytes.Buffer
	if err := htmlTemplate.ExecuteTemplate(&buf, "page", data); err != nil {
		return nil, fmt.Errorf("HTML レポートのテンプレート実行に失敗しました: %w", err)
	}
	return buf.Bytes(), nil
}

// WriteHTML は生成した HTML を path に書き出す。
func WriteHTML(path string, s *rollup.Series, opt HTMLOptions) error {
	b, err := RenderHTML(s, opt)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("HTML レポートの出力先ディレクトリ作成に失敗しました: %w", err)
		}
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("HTML レポートの書き込みに失敗しました: %w", err)
	}
	return nil
}

// ============================================================
// ページ全体のビューモデル
// ============================================================

type pageData struct {
	Title       string
	PeriodLabel string

	PeriodDays     int
	SessionsLabel  string
	CostLabel      string
	DurationLabel  string
	EvaluatedLabel string

	CostTrendChart     chartBlock
	CostBreakdownChart chartBlock

	AchievedRatioChart chartBlock
	OutcomeChart       chartBlock

	ModelFitChart  chartBlock
	OwnershipChart chartBlock
	HalfCompare    halfCompareBlock

	ActionStatusChart     chartBlock
	ActionCumulativeChart chartBlock
	ActionsTable          *dataTable

	// EvalHealth は評価実行そのものの健全性。記録が無い期間は nil にして
	// セクション自体を出さない（古い DB で作った期間との後方互換のため）。
	EvalHealth *evalHealthBlock

	Caveats []string
}

// ============================================================
// チャート共通のビューモデル（SVG は Go 側で座標計算済みの図形記述）
// ============================================================

type chartBlock struct {
	Title  string
	Desc   string
	Note   string // 空でなければ SVG の代わりにこのメッセージだけを表示する
	SVG    chartSVG
	Legend []legendItem
	Table  *dataTable
}

type legendItem struct {
	Color string
	Label string
}

type dataTable struct {
	Caption string
	Headers []string
	Rows    [][]string
}

type chartSVG struct {
	ViewBox  string
	Lines    []svgLine // グリッド線・軸線（最背面）
	Rects    []svgRect
	Dividers []svgLine // 積み上げ区切り・マーカーの surface リング用（Rects の上、Circles の下）
	Paths    []svgPath
	Circles  []svgCircle
	Texts    []svgText // 最前面
}

type svgLine struct {
	X1, Y1, X2, Y2 string
	Class          string
}

type svgRect struct {
	X, Y, W, H string
	Fill       string
	Title      string
}

type svgPath struct {
	D      string
	Stroke string
	Class  string
}

type svgCircle struct {
	Cx, Cy, R string
	Fill      string
	Title     string
}

type svgText struct {
	X, Y   string
	Text   string
	Anchor string
	Class  string
}

// evalHealthBlock は評価実行の健全性セクションの表示用データ。
// 「評価そのものが失敗し続けていないか」を一目で見せるのが目的なので、
// 失敗 0 件のときも FailuresTable に「失敗なし」の行を必ず入れる。
type evalHealthBlock struct {
	TotalLabel     string
	SucceededLabel string
	FailedLabel    string // 件数と失敗率
	CostLabel      string // 失敗した試行のぶんも含む実コスト
	FailuresTable  *dataTable
}

type halfCompareBlock struct {
	Applicable bool
	Note       string
	Rows       []halfCompareRow
}

type halfCompareRow struct {
	Label     string
	FirstPct  string
	SecondPct string
	FirstN    string
	SecondN   string
}

// ============================================================
// 色割り当て
// ============================================================

// categoricalSlots は dataviz スキルの検証済みパレット（隣接ペア基準）の
// CSS カスタムプロパティ参照。積み上げ棒・折れ線での隣接視認性が検証済みの並び順。
var categoricalSlots = []string{
	"var(--s1)", "var(--s2)", "var(--s3)", "var(--s4)",
	"var(--s5)", "var(--s6)", "var(--s7)", "var(--s8)",
}

const otherColor = "var(--muted)"

// outcomeColor / modelFitColor / ownershipColor / actionStatusColor は
// 評価軸ごとに固定の色を割り当てる。チャートをまたいで同じ値が同じ色になるようにするため、
// アルファベット順ではなく意味に基づいて固定する。
var outcomeColor = map[string]string{
	"achieved":    categoricalSlots[2], // aqua
	"partial":     categoricalSlots[3], // yellow
	"abandoned":   categoricalSlots[7], // red
	"exploratory": categoricalSlots[0], // blue
}

var modelFitColor = map[string]string{
	"over":        categoricalSlots[1], // orange
	"appropriate": categoricalSlots[2], // aqua
	"under":       categoricalSlots[6], // violet
}

var ownershipColor = map[string]string{
	"understood": categoricalSlots[2], // aqua
	"partial":    categoricalSlots[3], // yellow
	"black_box":  categoricalSlots[7], // red
}

var actionStatusColor = map[string]string{
	"open":    "var(--warn)",
	"done":    "var(--good)",
	"dropped": "var(--muted)",
	"expired": "var(--serious)",
}

// actionStatusLabel の表示ラベルは labels.go の actionStatusLabels（日本語変換テーブル）を
// 一元的に参照する（ユーザー要求: 変換テーブルは internal/render に 1 箇所へ集約する）。

func colorFor(m map[string]string, key string) string {
	if c, ok := m[key]; ok {
		return c
	}
	return otherColor
}

// assignModelColors はモデル名の集合に決定的に色を割り当てる。
// コストの大きい順に上位 7 モデルへ個別色を与え、残りは「その他」に畳む
// （8 色パレットの隣接視認性検証は最大 8 スロットまでのため）。
func assignModelColors(totals map[string]float64) (order []string, color map[string]string) {
	names := make([]string, 0, len(totals))
	for k := range totals {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})

	color = make(map[string]string, len(names))
	const maxDistinct = 7
	for i, n := range names {
		if i < maxDistinct {
			color[n] = categoricalSlots[i]
		} else {
			color[n] = otherColor
		}
	}
	return names, color
}

// ============================================================
// 数値・座標フォーマット
// ============================================================

func ff(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(v))
	base := math.Pow(10, exp)
	n := v / base
	var nice float64
	switch {
	case n <= 1:
		nice = 1
	case n <= 2:
		nice = 2
	case n <= 2.5:
		nice = 2.5
	case n <= 5:
		nice = 5
	default:
		nice = 10
	}
	return nice * base
}

func shortDate(d string) string {
	if len(d) != 10 {
		return d
	}
	return d[5:7] + "/" + d[8:10]
}

func selectLabelIndices(n, max int) []int {
	if n <= 0 {
		return nil
	}
	if max < 2 {
		max = 2
	}
	if n <= max {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	step := float64(n-1) / float64(max-1)
	seen := make(map[int]bool, max)
	var out []int
	for i := 0; i < max; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx >= n {
			idx = n - 1
		}
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// commaInt は 3 桁区切りの整数表示。format.go には整数のコンマ区切りヘルパが
// 無いため、トークン件数の表示専用にここで定義する。
func commaInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if neg {
		out = "-" + out
	}
	return out
}

func seriesLegend(names []string, color map[string]string, label map[string]string) []legendItem {
	out := make([]legendItem, 0, len(names))
	for _, n := range names {
		l := n
		if label != nil {
			if v, ok := label[n]; ok {
				l = v
			}
		}
		out = append(out, legendItem{Color: colorFor(color, n), Label: l})
	}
	return out
}

// ============================================================
// フレーム（軸・グリッド）
// ============================================================

type frame struct {
	lines []svgLine
	texts []svgText
}

func (f *frame) addYGrid(ticks []float64, max float64, fmtFn func(float64) string) {
	if max <= 0 {
		max = 1
	}
	for _, t := range ticks {
		y := marginT + plotH*(1-t/max)
		class := "grid-line"
		if t == 0 {
			class = "axis-line"
		}
		f.lines = append(f.lines, svgLine{
			X1: ff(marginL), Y1: ff(y), X2: ff(marginL + plotW), Y2: ff(y), Class: class,
		})
		f.texts = append(f.texts, svgText{
			X: ff(marginL - 6), Y: ff(y + 3), Text: fmtFn(t), Anchor: "end", Class: "axis-label",
		})
	}
}

func (f *frame) addXLabels(xLabels []string, xCenter func(int) float64) {
	idxs := selectLabelIndices(len(xLabels), maxXLabels)
	baseline := marginT + plotH + 14
	for _, i := range idxs {
		f.texts = append(f.texts, svgText{
			X: ff(xCenter(i)), Y: ff(baseline), Text: xLabels[i], Anchor: "middle", Class: "axis-label",
		})
	}
}

// ============================================================
// 積み上げ棒グラフ
// ============================================================

type stackedSeries struct {
	Name       string
	Color      string
	Values     []float64 // len == len(xLabels)
	TooltipFmt func(xLabel string, v float64) string
}

type barChartOpt struct {
	Percent     bool // true なら各 x を自身の合計で正規化した構成比として描く
	YTickFmt    func(v float64) string
	NoDataTitle string // 合計が 0 の x に表示するツールチップ
}

func buildStackedBarSVG(xLabels []string, series []stackedSeries, opt barChartOpt) chartSVG {
	n := len(xLabels)
	f := &frame{}

	bandW := plotW / math.Max(float64(n), 1)
	barW := math.Min(bandW*0.6, maxBarWidth)
	xCenter := func(i int) float64 { return marginL + bandW*(float64(i)+0.5) }

	var yMax float64 = 1
	if !opt.Percent {
		max := 0.0
		for i := 0; i < n; i++ {
			total := 0.0
			for _, s := range series {
				if i < len(s.Values) {
					total += s.Values[i]
				}
			}
			if total > max {
				max = total
			}
		}
		yMax = niceCeil(max)
	}

	if opt.Percent {
		f.addYGrid([]float64{0, 0.25, 0.5, 0.75, 1}, 1, func(v float64) string {
			return fmt.Sprintf("%.0f%%", v*100)
		})
	} else {
		ticks := []float64{0, yMax * 0.25, yMax * 0.5, yMax * 0.75, yMax}
		f.addYGrid(ticks, yMax, opt.YTickFmt)
	}
	f.addXLabels(xLabels, xCenter)

	svg := chartSVG{
		ViewBox: fmt.Sprintf("0 0 %s %s", ff(chartW), ff(chartH)),
	}

	baseline := marginT + plotH
	for i := 0; i < n; i++ {
		total := 0.0
		for _, s := range series {
			if i < len(s.Values) {
				total += s.Values[i]
			}
		}
		x := xCenter(i) - barW/2

		if total <= 0 {
			// データなしを 0 として捏造せず、目立たないマーカーだけ置く。
			svg.Rects = append(svg.Rects, svgRect{
				X: ff(x), Y: ff(baseline - 2), W: ff(barW), H: "2",
				Fill: "var(--grid)", Title: opt.NoDataTitle,
			})
			continue
		}

		cum := 0.0
		var boundaries []float64
		for _, s := range series {
			v := 0.0
			if i < len(s.Values) {
				v = s.Values[i]
			}
			if v <= 0 {
				continue
			}
			frac := v
			if opt.Percent {
				frac = v / total
			}
			h := plotH * frac / yMax
			topY := baseline - cum - h
			title := ""
			if s.TooltipFmt != nil {
				title = s.TooltipFmt(xLabels[i], v)
			}
			svg.Rects = append(svg.Rects, svgRect{
				X: ff(x), Y: ff(topY), W: ff(barW), H: ff(h), Fill: s.Color, Title: title,
			})
			cum += h
			boundaries = append(boundaries, cum)
		}
		// 積み上げ区切りの 2px サーフェスギャップ（最上部の境界は不要）。
		for bi := 0; bi < len(boundaries)-1; bi++ {
			y := baseline - boundaries[bi]
			svg.Dividers = append(svg.Dividers, svgLine{
				X1: ff(x), Y1: ff(y), X2: ff(x + barW), Y2: ff(y), Class: "divider",
			})
		}
	}

	svg.Lines = f.lines
	svg.Texts = f.texts
	return svg
}

// ============================================================
// 折れ線グラフ（欠測は線を切る）
// ============================================================

type lineSeries struct {
	Name       string
	Color      string
	Values     []float64
	Valid      []bool // false の点は欠測として線を切る
	TooltipFmt func(xLabel string, v float64) string
}

type lineChartOpt struct {
	YMax     float64 // <=0 ならデータから自動計算
	YTicks   []float64
	YTickFmt func(v float64) string
}

func buildLineSVG(xLabels []string, series []lineSeries, opt lineChartOpt) chartSVG {
	n := len(xLabels)
	f := &frame{}

	xStep := plotW / math.Max(float64(n-1), 1)
	xAt := func(i int) float64 {
		if n <= 1 {
			return marginL + plotW/2
		}
		return marginL + xStep*float64(i)
	}

	yMax := opt.YMax
	if yMax <= 0 {
		max := 0.0
		for _, s := range series {
			for i, v := range s.Values {
				if i < len(s.Valid) && !s.Valid[i] {
					continue
				}
				if v > max {
					max = v
				}
			}
		}
		yMax = niceCeil(max)
	}

	ticks := opt.YTicks
	if len(ticks) == 0 {
		ticks = []float64{0, yMax * 0.25, yMax * 0.5, yMax * 0.75, yMax}
	}
	f.addYGrid(ticks, yMax, opt.YTickFmt)
	f.addXLabels(xLabels, xAt)

	svg := chartSVG{ViewBox: fmt.Sprintf("0 0 %s %s", ff(chartW), ff(chartH))}
	yAt := func(v float64) float64 { return marginT + plotH*(1-v/yMax) }

	for _, s := range series {
		var d strings.Builder
		drawing := false
		for i := 0; i < n; i++ {
			valid := i < len(s.Valid) && s.Valid[i]
			if !valid {
				drawing = false
				continue
			}
			x, y := xAt(i), yAt(s.Values[i])
			if !drawing {
				fmt.Fprintf(&d, "M%s,%s ", ff(x), ff(y))
				drawing = true
			} else {
				fmt.Fprintf(&d, "L%s,%s ", ff(x), ff(y))
			}
		}
		if strings.TrimSpace(d.String()) != "" {
			svg.Paths = append(svg.Paths, svgPath{D: strings.TrimSpace(d.String()), Stroke: s.Color, Class: "line"})
		}
		for i := 0; i < n; i++ {
			valid := i < len(s.Valid) && s.Valid[i]
			if !valid {
				continue
			}
			x, y := xAt(i), yAt(s.Values[i])
			title := ""
			if s.TooltipFmt != nil {
				title = s.TooltipFmt(xLabels[i], s.Values[i])
			}
			// surface リング（背面の大きめの円）+ 系列色の円。
			svg.Circles = append(svg.Circles, svgCircle{Cx: ff(x), Cy: ff(y), R: ff(markerRadius + 2), Fill: "var(--surface)"})
			svg.Circles = append(svg.Circles, svgCircle{Cx: ff(x), Cy: ff(y), R: ff(markerRadius), Fill: s.Color, Title: title})
		}
	}

	svg.Lines = f.lines
	svg.Texts = f.texts
	return svg
}

// ============================================================
// 構成比バー（横一本の 100% 積み上げ）
// ============================================================

type compSegment struct {
	Name  string
	Color string
	Value float64
	Sub   string // 補足テキスト（トークン件数など）
}

func buildCompositionBar(segs []compSegment) chartSVG {
	const h = 32.0
	const y = 16.0
	w := chartW - marginL - marginR

	total := 0.0
	for _, s := range segs {
		total += s.Value
	}

	svg := chartSVG{ViewBox: fmt.Sprintf("0 0 %s %s", ff(chartW), ff(h+2*y))}
	if total <= 0 {
		svg.Texts = append(svg.Texts, svgText{
			X: ff(marginL), Y: ff(y + h/2 + 4), Text: "データがありません", Anchor: "start", Class: "axis-label",
		})
		return svg
	}

	x := marginL
	var boundaries []float64
	for _, s := range segs {
		if s.Value <= 0 {
			continue
		}
		segW := w * s.Value / total
		pct := 100 * s.Value / total
		label := fmt.Sprintf("%s %.1f%%", s.Name, pct)
		if s.Sub != "" {
			label = fmt.Sprintf("%s（%s）", label, s.Sub)
		}
		svg.Rects = append(svg.Rects, svgRect{
			X: ff(x), Y: ff(y), W: ff(segW), H: ff(h), Fill: s.Color, Title: label,
		})
		if segW >= 34 {
			svg.Texts = append(svg.Texts, svgText{
				X: ff(x + segW/2), Y: ff(y + h/2 + 4), Text: fmt.Sprintf("%.0f%%", pct),
				Anchor: "middle", Class: "value-label-inv",
			})
		}
		x += segW
		boundaries = append(boundaries, x)
	}
	for i := 0; i < len(boundaries)-1; i++ {
		svg.Dividers = append(svg.Dividers, svgLine{
			X1: ff(boundaries[i]), Y1: ff(y), X2: ff(boundaries[i]), Y2: ff(y + h), Class: "divider-v",
		})
	}
	return svg
}

// ============================================================
// Series からビューモデルを組み立てる
// ============================================================

func buildPageData(s *rollup.Series, opt HTMLOptions) *pageData {
	pts := sortedPoints(s)
	var byModel []rollup.ModelUsage
	var actions []model.Action
	from, to := "", ""
	if s != nil {
		byModel = append(byModel, s.ByModel...)
		actions = append(actions, s.Actions...)
		from, to = s.From, s.To
	}
	sort.Slice(byModel, func(i, j int) bool { return byModel[i].Model < byModel[j].Model })

	title := strings.TrimSpace(opt.Title)
	if title == "" {
		title = autoTitle(from, to)
	}

	periodDays, missingDays := periodDayCount(from, to, pts)

	totalSessions := 0
	totalDuration := 0.0
	totalCost := 0.0
	evaluated := 0
	for _, p := range pts {
		totalSessions += p.Sessions
		totalDuration += p.DurationMinutes
		totalCost += p.CostUSD
		for _, n := range p.Outcome {
			evaluated += n
		}
	}
	unevaluated := totalSessions - evaluated
	if unevaluated < 0 {
		unevaluated = 0
	}

	d := &pageData{
		Title:       title,
		PeriodLabel: periodLabel(from, to, len(pts), missingDays),
		PeriodDays:  periodDays,

		SessionsLabel:  fmt.Sprintf("%d 件", totalSessions),
		CostLabel:      formatMoneyPlain(totalCost),
		DurationLabel:  formatDurationHours(totalDuration),
		EvaluatedLabel: fmt.Sprintf("%d / %d 件（未評価 %d）", evaluated, totalSessions, unevaluated),

		CostTrendChart:     buildCostTrendChart(pts, byModel),
		CostBreakdownChart: buildCostBreakdownChart(byModel),

		AchievedRatioChart: buildAchievedRatioChart(pts),
		OutcomeChart:       buildOutcomeChart(pts),

		ModelFitChart: buildFacetTrendChart(pts, "モデル適合の日次件数", func(p rollup.Point) map[string]int { return p.ModelFit },
			[]string{"under", "appropriate", "over"}, modelFitColor, modelFitVerdictLabels),
		OwnershipChart: buildFacetTrendChart(pts, "主体性の日次件数", func(p rollup.Point) map[string]int { return p.Ownership },
			[]string{"black_box", "partial", "understood"}, ownershipColor, ownershipLevelLabels),
		HalfCompare: buildHalfCompare(pts),

		ActionStatusChart:     buildActionStatusChart(actions),
		ActionCumulativeChart: buildActionCumulativeChart(actions),
		ActionsTable:          buildActionsTable(actions),

		Caveats: buildCaveats(byModel, unevaluated, missingDays),
	}

	if s != nil {
		d.EvalHealth = buildEvalHealthBlock(s.EvalHealth)
	}

	return d
}

func autoTitle(from, to string) string {
	if from == "" && to == "" {
		return "インサイトレポート"
	}
	if from == to {
		return fmt.Sprintf("%s のインサイトレポート", from)
	}
	return fmt.Sprintf("%s 〜 %s のインサイトレポート", from, to)
}

func periodLabel(from, to string, dataDays, missingDays int) string {
	label := fmt.Sprintf("期間: %s 〜 %s（データのある日数: %d 日）", orDash(from), orDash(to), dataDays)
	if missingDays > 0 {
		label += fmt.Sprintf(" ／ 欠測 %d 日", missingDays)
	}
	return label
}

// formatDurationHours は formatDuration（分表示）に時間換算を添える。
// format.go の formatDuration をそのまま再利用し、二重実装を避ける。
func formatDurationHours(minutes float64) string {
	return fmt.Sprintf("%s（%.1f時間）", formatDuration(minutes), minutes/60)
}

func sortedPoints(s *rollup.Series) []rollup.Point {
	if s == nil || len(s.Points) == 0 {
		return nil
	}
	out := make([]rollup.Point, len(s.Points))
	copy(out, s.Points)
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// periodDayCount は From/To から暦日数を数え、Points の実データ日数との差を欠測日数として返す。
// 日付が壊れている・空の場合は Points の件数をそのまま使う。
func periodDayCount(from, to string, pts []rollup.Point) (calendarDays, missingDays int) {
	if from == "" || to == "" {
		return len(pts), 0
	}
	fd, err1 := time.Parse("2006-01-02", from)
	td, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return len(pts), 0
	}
	days := int(td.Sub(fd).Hours()/24) + 1
	if days < 1 {
		days = len(pts)
	}
	missing := days - len(pts)
	if missing < 0 {
		missing = 0
	}
	return days, missing
}

// ============================================================
// 各チャートの組み立て
// ============================================================

func buildCostTrendChart(pts []rollup.Point, byModel []rollup.ModelUsage) chartBlock {
	if len(pts) == 0 {
		return chartBlock{Title: "日次コスト推移（モデル別積み上げ）", Note: "期間内にデータがありません。"}
	}

	totals := make(map[string]float64)
	for _, m := range byModel {
		totals[m.Model] += m.CostUSD
	}
	for _, p := range pts {
		for m, c := range p.CostByModel {
			if _, ok := totals[m]; !ok {
				totals[m] += c
			}
		}
	}
	order, color := assignModelColors(totals)

	xLabels := make([]string, len(pts))
	for i, p := range pts {
		xLabels[i] = shortDate(p.Date)
	}

	seriesList := make([]stackedSeries, 0, len(order))
	for _, name := range order {
		values := make([]float64, len(pts))
		for i, p := range pts {
			values[i] = p.CostByModel[name]
		}
		n := name
		seriesList = append(seriesList, stackedSeries{
			Name: n, Color: colorFor(color, n), Values: values,
			TooltipFmt: func(xl string, v float64) string {
				return fmt.Sprintf("%s %s: %s", xl, n, formatMoneyPlain(v))
			},
		})
	}

	svg := buildStackedBarSVG(xLabels, seriesList, barChartOpt{
		Percent:     false,
		YTickFmt:    func(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) },
		NoDataTitle: "この日のコストデータはありません",
	})

	table := &dataTable{
		Caption: "日次コスト（モデル別）",
		Headers: append([]string{"日付"}, append(append([]string{}, order...), "合計")...),
	}
	for _, p := range pts {
		row := []string{p.Date}
		total := 0.0
		for _, name := range order {
			v := p.CostByModel[name]
			total += v
			row = append(row, formatMoneyPlain(v))
		}
		row = append(row, formatMoneyPlain(total))
		table.Rows = append(table.Rows, row)
	}

	return chartBlock{
		Title:  "日次コスト推移（モデル別積み上げ）",
		Desc:   "各棒の高さは合計コスト。色はモデルを表す（上位 7 モデルを個別表示し、残りは「その他」に集約）。",
		SVG:    svg,
		Legend: seriesLegend(order, color, nil),
		Table:  table,
	}
}

func buildCostBreakdownChart(byModel []rollup.ModelUsage) chartBlock {
	var input, output, cacheRead, cacheWrite int64
	for _, m := range byModel {
		input += m.InputTokens
		output += m.OutputTokens
		cacheRead += m.CacheReadTokens
		cacheWrite += m.CacheWriteTokens
	}
	total := input + output + cacheRead + cacheWrite
	if total <= 0 {
		return chartBlock{
			Title: "コスト内訳（期間合計・トークン構成比）",
			Note:  "トークン使用量のデータがありません。",
		}
	}

	segs := []compSegment{
		{Name: "input", Color: categoricalSlots[0], Value: float64(input), Sub: commaInt(input) + " トークン"},
		{Name: "output", Color: categoricalSlots[2], Value: float64(output), Sub: commaInt(output) + " トークン"},
		{Name: "cache read", Color: categoricalSlots[1], Value: float64(cacheRead), Sub: commaInt(cacheRead) + " トークン"},
		{Name: "cache write", Color: categoricalSlots[3], Value: float64(cacheWrite), Sub: commaInt(cacheWrite) + " トークン"},
	}
	svg := buildCompositionBar(segs)

	legend := make([]legendItem, 0, len(segs))
	table := &dataTable{Caption: "トークン種別ごとの構成比（期間合計）", Headers: []string{"種別", "トークン数", "構成比"}}
	for _, seg := range segs {
		legend = append(legend, legendItem{Color: seg.Color, Label: seg.Name})
		table.Rows = append(table.Rows, []string{seg.Name, commaInt(int64(seg.Value)), formatPercent(int(seg.Value), int(total))})
	}

	return chartBlock{
		Title:  "コスト内訳（期間合計・トークン構成比）",
		Desc:   "日次の内訳データが無いため、期間合計のトークン構成比として表示する（$ 換算の日次内訳は捏造しない）。生成（output）以外の割合が大きい場合、費用の多くはキャッシュの読み書きに使われている。",
		SVG:    svg,
		Legend: legend,
		Table:  table,
	}
}

func buildAchievedRatioChart(pts []rollup.Point) chartBlock {
	if len(pts) == 0 {
		return chartBlock{Title: "成果の推移（達成率）", Note: "期間内にデータがありません。"}
	}

	hasAny := false
	xLabels := make([]string, len(pts))
	values := make([]float64, len(pts))
	valid := make([]bool, len(pts))
	for i, p := range pts {
		xLabels[i] = shortDate(p.Date)
		if p.AchievedRatio < 0 {
			valid[i] = false
			continue
		}
		hasAny = true
		valid[i] = true
		values[i] = p.AchievedRatio * 100
	}
	if !hasAny {
		return chartBlock{Title: "成果の推移（達成率）", Note: "評価済みセッションがある日が期間内に無いため、達成率は算出できません。"}
	}

	series := []lineSeries{{
		Name: "達成率", Color: categoricalSlots[0], Values: values, Valid: valid,
		TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s: 達成率 %.1f%%", xl, v) },
	}}
	svg := buildLineSVG(xLabels, series, lineChartOpt{
		YMax: 100, YTicks: []float64{0, 25, 50, 75, 100},
		YTickFmt: func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
	})

	table := &dataTable{Caption: fmt.Sprintf("日次の達成率（%s / 評価済みセッション数）", outcomeJP("achieved")), Headers: []string{"日付", "達成率"}}
	for _, p := range pts {
		if p.AchievedRatio < 0 {
			table.Rows = append(table.Rows, []string{p.Date, "評価データなし"})
		} else {
			table.Rows = append(table.Rows, []string{p.Date, fmt.Sprintf("%.1f%%", p.AchievedRatio*100)})
		}
	}

	return chartBlock{
		Title: "成果の推移（達成率）",
		Desc:  "評価済みセッションが 0 件の日は「データなし」として線を切っている（0% と混同しないため）。",
		SVG:   svg,
		Table: table,
	}
}

func buildOutcomeChart(pts []rollup.Point) chartBlock {
	if len(pts) == 0 {
		return chartBlock{Title: "成果の日次構成比（outcome）", Note: "期間内にデータがありません。"}
	}
	order := []string{"exploratory", "partial", "abandoned", "achieved"}
	labelJP := outcomeLabels

	xLabels := make([]string, len(pts))
	for i, p := range pts {
		xLabels[i] = shortDate(p.Date)
	}
	seriesList := make([]stackedSeries, 0, len(order))
	for _, key := range order {
		values := make([]float64, len(pts))
		for i, p := range pts {
			values[i] = float64(p.Outcome[key])
		}
		k := key
		seriesList = append(seriesList, stackedSeries{
			Name: labelJP[k], Color: colorFor(outcomeColor, k), Values: values,
			TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s %s: %d 件", xl, labelJP[k], int(v)) },
		})
	}
	svg := buildStackedBarSVG(xLabels, seriesList, barChartOpt{Percent: true, NoDataTitle: "この日は評価済みセッションがありません"})

	legend := make([]legendItem, 0, len(order))
	for _, key := range order {
		legend = append(legend, legendItem{Color: colorFor(outcomeColor, key), Label: labelJP[key]})
	}

	table := &dataTable{Caption: "日次の成果件数", Headers: []string{"日付", outcomeJP("achieved"), outcomeJP("partial"), outcomeJP("abandoned"), outcomeJP("exploratory")}}
	for _, p := range pts {
		table.Rows = append(table.Rows, []string{
			p.Date,
			strconv.Itoa(p.Outcome["achieved"]),
			strconv.Itoa(p.Outcome["partial"]),
			strconv.Itoa(p.Outcome["abandoned"]),
			strconv.Itoa(p.Outcome["exploratory"]),
		})
	}

	return chartBlock{
		Title:  "成果の日次構成比（outcome）",
		Desc:   "評価済みセッションが 0 件の日は薄いマーカーのみで「データなし」を示す。",
		SVG:    svg,
		Legend: legend,
		Table:  table,
	}
}

func buildFacetTrendChart(pts []rollup.Point, title string, get func(rollup.Point) map[string]int, order []string, color map[string]string, labelJP map[string]string) chartBlock {
	if len(pts) == 0 {
		return chartBlock{Title: title, Note: "期間内にデータがありません。"}
	}
	xLabels := make([]string, len(pts))
	for i, p := range pts {
		xLabels[i] = shortDate(p.Date)
	}
	seriesList := make([]stackedSeries, 0, len(order))
	anyData := false
	for _, key := range order {
		values := make([]float64, len(pts))
		for i, p := range pts {
			v := get(p)[key]
			values[i] = float64(v)
			if v != 0 {
				anyData = true
			}
		}
		k := key
		seriesList = append(seriesList, stackedSeries{
			Name: labelJP[k], Color: colorFor(color, k), Values: values,
			TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s %s: %d 件", xl, labelJP[k], int(v)) },
		})
	}
	if !anyData {
		return chartBlock{Title: title, Note: "評価済みセッションが期間内に無いため、データがありません。"}
	}
	svg := buildStackedBarSVG(xLabels, seriesList, barChartOpt{Percent: false, YTickFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) }, NoDataTitle: "この日は評価済みセッションがありません"})

	legend := make([]legendItem, 0, len(order))
	for _, key := range order {
		legend = append(legend, legendItem{Color: colorFor(color, key), Label: labelJP[key]})
	}

	table := &dataTable{Caption: title + "（日次件数）", Headers: append([]string{"日付"}, jpLabels(order, labelJP)...)}
	for _, p := range pts {
		row := []string{p.Date}
		for _, key := range order {
			row = append(row, strconv.Itoa(get(p)[key]))
		}
		table.Rows = append(table.Rows, row)
	}

	return chartBlock{Title: title, SVG: svg, Legend: legend, Table: table}
}

func jpLabels(order []string, labelJP map[string]string) []string {
	out := make([]string, len(order))
	for i, k := range order {
		out[i] = labelJP[k]
	}
	return out
}

// buildHalfCompare は評価軸の前半/後半比較を作る。データのある日数が少なすぎる場合は
// 「比較しない」旨を明示する（無意味な比較を数値だけ出さない）。
func buildHalfCompare(pts []rollup.Point) halfCompareBlock {
	const minDaysForCompare = 4
	if len(pts) < minDaysForCompare {
		return halfCompareBlock{
			Applicable: false,
			Note:       fmt.Sprintf("データのある日数が %d 日と少ないため、前半/後半の比較は行いません（目安: %d 日以上）。", len(pts), minDaysForCompare),
		}
	}

	mid := len(pts) / 2
	first := pts[:mid]
	second := pts[mid:]

	rateOf := func(group []rollup.Point, get func(rollup.Point) map[string]int, key string) (n, denom int) {
		for _, p := range group {
			m := get(p)
			denom += sumMap(m)
			n += m[key]
		}
		return
	}

	modelFitGet := func(p rollup.Point) map[string]int { return p.ModelFit }
	ownershipGet := func(p rollup.Point) map[string]int { return p.Ownership }

	rows := []halfCompareRow{
		newHalfRow("モデル過剰率", first, second, modelFitGet, "over", rateOf),
		newHalfRow("モデル力不足率", first, second, modelFitGet, "under", rateOf),
		newHalfRow("理解して検収した割合", first, second, ownershipGet, "understood", rateOf),
		newHalfRow("理解せず委譲した割合", first, second, ownershipGet, "black_box", rateOf),
	}

	return halfCompareBlock{Applicable: true, Rows: rows}
}

func newHalfRow(label string, first, second []rollup.Point, get func(rollup.Point) map[string]int, key string,
	rateOf func([]rollup.Point, func(rollup.Point) map[string]int, string) (int, int)) halfCompareRow {
	n1, d1 := rateOf(first, get, key)
	n2, d2 := rateOf(second, get, key)
	return halfCompareRow{
		Label:     label,
		FirstPct:  formatPercent(n1, d1),
		SecondPct: formatPercent(n2, d2),
		FirstN:    fmt.Sprintf("%d/%d 件", n1, d1),
		SecondN:   fmt.Sprintf("%d/%d 件", n2, d2),
	}
}

func sumMap(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

func buildActionStatusChart(actions []model.Action) chartBlock {
	if len(actions) == 0 {
		return chartBlock{Title: "改善提案の状態別件数", Note: "期間内に改善提案がありません。"}
	}
	order := []string{"open", "done", "dropped", "expired"}
	counts := make(map[string]int, 4)
	for _, a := range actions {
		counts[string(a.Status)]++
	}

	xLabels := make([]string, len(order))
	for i, k := range order {
		xLabels[i] = actionStatusJP(k)
	}
	seriesList := make([]stackedSeries, len(order))
	for i, key := range order {
		values := make([]float64, len(order))
		values[i] = float64(counts[key])
		k := key
		seriesList[i] = stackedSeries{
			Name: actionStatusJP(k), Color: colorFor(actionStatusColor, k), Values: values,
			TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s: %d 件", xl, int(v)) },
		}
	}
	svg := buildStackedBarSVG(xLabels, seriesList, barChartOpt{Percent: false, YTickFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) }})

	table := &dataTable{Caption: "状態別件数", Headers: []string{"状態", "件数"}}
	for _, key := range order {
		table.Rows = append(table.Rows, []string{actionStatusJP(key), strconv.Itoa(counts[key])})
	}

	return chartBlock{Title: "改善提案の状態別件数", SVG: svg, Table: table}
}

// buildActionCumulativeChart は「提案は増え続けているが完了が増えていない」状態を
// 一目で分かるようにする、提案累計 vs 完了累計の折れ線。
func buildActionCumulativeChart(actions []model.Action) chartBlock {
	if len(actions) == 0 {
		return chartBlock{Title: "提案累計 vs 完了累計", Note: "期間内に改善提案がありません。"}
	}

	dateSet := make(map[string]struct{})
	for _, a := range actions {
		if a.CreatedOn != "" {
			dateSet[a.CreatedOn] = struct{}{}
		}
		if a.Status == model.ActionDone && a.VerifiedOn != "" {
			dateSet[a.VerifiedOn] = struct{}{}
		}
	}
	if len(dateSet) == 0 {
		return chartBlock{Title: "提案累計 vs 完了累計", Note: "提案日・検証日のデータがありません。"}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	proposed := make([]float64, len(dates))
	done := make([]float64, len(dates))
	valid := make([]bool, len(dates))
	for i, d := range dates {
		p, dn := 0, 0
		for _, a := range actions {
			if a.CreatedOn != "" && a.CreatedOn <= d {
				p++
			}
			if a.Status == model.ActionDone && a.VerifiedOn != "" && a.VerifiedOn <= d {
				dn++
			}
		}
		proposed[i] = float64(p)
		done[i] = float64(dn)
		valid[i] = true
	}

	xLabels := make([]string, len(dates))
	for i, d := range dates {
		xLabels[i] = shortDate(d)
	}

	series := []lineSeries{
		{Name: "提案累計", Color: categoricalSlots[0], Values: proposed, Valid: valid,
			TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s: 提案累計 %d 件", xl, int(v)) }},
		{Name: "完了累計", Color: categoricalSlots[2], Values: done, Valid: valid,
			TooltipFmt: func(xl string, v float64) string { return fmt.Sprintf("%s: 完了累計 %d 件", xl, int(v)) }},
	}
	svg := buildLineSVG(xLabels, series, lineChartOpt{YTickFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) }})

	gap := proposed[len(proposed)-1] - done[len(done)-1]
	desc := fmt.Sprintf("期間末時点で提案累計 %d 件に対し完了累計 %d 件（差 %d 件）。", int(proposed[len(proposed)-1]), int(done[len(done)-1]), int(gap))
	if gap >= 3 && done[len(done)-1] < proposed[len(proposed)-1]*0.34 {
		desc += " 提案に対して完了が大きく遅れており、振り返りが実行に結びついていない可能性がある。"
	}

	return chartBlock{
		Title:  "提案累計 vs 完了累計",
		Desc:   desc,
		SVG:    svg,
		Legend: seriesLegend([]string{"提案累計", "完了累計"}, map[string]string{"提案累計": categoricalSlots[0], "完了累計": categoricalSlots[2]}, nil),
	}
}

func buildActionsTable(actions []model.Action) *dataTable {
	if len(actions) == 0 {
		return &dataTable{Caption: "改善提案一覧"}
	}
	sorted := make([]model.Action, len(actions))
	copy(sorted, actions)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedOn != sorted[j].CreatedOn {
			return sorted[i].CreatedOn < sorted[j].CreatedOn
		}
		return sorted[i].ID < sorted[j].ID
	})

	table := &dataTable{
		Caption: "改善提案一覧（提案日順）",
		Headers: []string{"提案日", "タイトル", "状態", "検証日", "検証所見"},
	}
	for _, a := range sorted {
		status := actionStatusJP(string(a.Status))
		table.Rows = append(table.Rows, []string{
			orDash(a.CreatedOn),
			truncateRunes(orDash(a.Title), 60),
			status,
			orDash(a.VerifiedOn),
			truncateRunes(orDash(a.Verdict), 80),
		})
	}
	return table
}

// buildEvalHealthBlock は評価実行の実測値を表示用データに組み立てる。
// eh が nil（記録が無い期間）ならセクション自体を出さないため nil を返す。
func buildEvalHealthBlock(eh *rollup.EvalHealth) *evalHealthBlock {
	if eh == nil {
		return nil
	}
	failureRate := 0.0
	if eh.Total > 0 {
		failureRate = float64(eh.Failed) / float64(eh.Total) * 100
	}
	return &evalHealthBlock{
		TotalLabel:     fmt.Sprintf("%d 件", eh.Total),
		SucceededLabel: fmt.Sprintf("%d 件", eh.Succeeded),
		FailedLabel:    fmt.Sprintf("%d 件（失敗率 %.1f%%）", eh.Failed, failureRate),
		CostLabel:      formatMoneyPlain(eh.CostUSD),
		FailuresTable:  buildEvalFailuresTable(eh),
	}
}

// buildEvalFailuresTable は失敗種別ごとの内訳表を作る。
// 失敗が 0 件のときも「失敗なし」の行を出す（健全であること自体が見えるようにするため）。
func buildEvalFailuresTable(eh *rollup.EvalHealth) *dataTable {
	table := &dataTable{
		Caption: "評価失敗の内訳",
		Headers: []string{"種類", "件数"},
	}
	if eh.Failed == 0 {
		table.Rows = [][]string{{"失敗なし", "0 件"}}
		return table
	}
	keys := make([]string, 0, len(eh.FailuresByKind))
	for k := range eh.FailuresByKind {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		table.Rows = append(table.Rows, []string{evalFailureKindJP(k), fmt.Sprintf("%d 件", eh.FailuresByKind[k])})
	}
	return table
}

func buildCaveats(byModel []rollup.ModelUsage, unevaluated, missingDays int) []string {
	var caveats []string

	caveats = append(caveats, "LLM による評価は傾向を見るための目安であり、絶対値ではありません。")

	var unpriced []string
	for _, m := range byModel {
		if !m.Priced {
			unpriced = append(unpriced, m.Model)
		}
	}
	if len(unpriced) > 0 {
		sort.Strings(unpriced)
		caveats = append(caveats, fmt.Sprintf("次のモデルは単価が未登録のため、コストは過小評価です: %s", strings.Join(unpriced, ", ")))
	}

	if missingDays > 0 {
		caveats = append(caveats, fmt.Sprintf("期間内に %d 日分のデータ欠測があります（集計対象外の日は 0 として扱っていません）。", missingDays))
	}

	if unevaluated > 0 {
		caveats = append(caveats, fmt.Sprintf("未評価のセッションが %d 件あり、成果・やり方の分布はそれらを含みません。", unevaluated))
	}

	return caveats
}
