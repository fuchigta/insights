// Package rollup の純粋集計部分。AI を使わず、store から読んだ行データだけから
// Daily / Series を組み立てる。ここで決めた並び順・分類ルールは決定的でなければならない
// （同じ入力なら常に同じ出力になること）。
package rollup

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/store"
)

// isInteractiveEntrypoint は entrypoint が対話セッションかどうかを判定する。
// 判定そのものは model.IsInteractiveEntrypoint に置いてある（評価プロンプト側でも
// 同じ境界を使うため）。サブエージェント（IsSidechain）はこの判定の対象外で、
// 呼び出し側で別枠（Totals.SidechainSessions）として数えること。
func isInteractiveEntrypoint(entrypoint string) bool {
	return model.IsInteractiveEntrypoint(entrypoint)
}

// executionMode は SessionCard に載せる実行形態のラベル。日報・振り返りの
// プロンプトは "sdk-cli" が自動実行だという知識を持たないので、判定済みの値を渡す。
func executionMode(entrypoint string) string {
	if model.IsInteractiveEntrypoint(entrypoint) {
		return "interactive"
	}
	return "automated"
}

// 丸め（Highlights / RolledUp への振り分け）の既定しきい値。
//
// 方式: 「TotalCostUSD（子を含む）と DurationMinutes のどちらかが十分大きければ個別掲載」
// という要件を、絶対値ではなく「その日の総コストに対する割合」×「絶対時間」の組み合わせで
// 判定する。絶対額のドル基準だと、その日の支出規模（数十円の日と数千円の日）によって
// 「小さい」の意味が変わってしまうため、コスト側は相対値（日次総コストに対する割合）を
// 主軸にする。時間側は「軽い調査・確認」を切り出す目安として絶対分数を使う
// （時間の感覚は日々の支出規模に左右されにくいため）。
//
// 丸める条件は「コスト割合が閾値未満 かつ 時間が閾値未満」の両方を満たす場合のみ
// （どちらか一方でも大きければ個別掲載する、という要件の否定）。
//
//   - defaultRollUpCostShareThreshold = 1%: ダッシュボードの構成比表示は 1% 未満の内訳を
//     読み取っても意思決定に寄与しないことが多いという経験則から採用。
//   - defaultRollUpDurationThresholdMinutes = 10分: 10分未満は「ちょっとした確認・調査」レベルの
//     作業とみなし、成果物としての実体を伴わないことが多いという経験則から採用。
//
// 呼び出し側（設定など）から上書きしたい場合は DailyInput.RollupThreshold に非ゼロ値を
// 渡せばこの既定値より優先される。
const (
	defaultRollUpCostShareThreshold       = 0.01 // その日の総コストに対する割合（0..1）
	defaultRollUpDurationThresholdMinutes = 10.0 // 分
)

// RollupThreshold は「個別に載せる（Highlights）か丸める（RolledUp）か」を判定するしきい値。
// ゼロ値のフィールドは defaultRollUpCostShareThreshold / defaultRollUpDurationThresholdMinutes
// にフォールバックする。将来、設定ファイル経由で外部から差し込めるようにするため、
// BuildDaily の入力（DailyInput）として受け取れる形にしてある。
type RollupThreshold struct {
	// CostShare はその日の総コストに対する割合のしきい値（0..1）。0 以下なら既定値を使う。
	CostShare float64
	// DurationMinutes は分単位のしきい値。0 以下なら既定値を使う。
	DurationMinutes float64
}

// resolve は RollupThreshold のゼロ値を既定値で埋めた実効値を返す。
func (r RollupThreshold) resolve() (costShare, durationMinutes float64) {
	costShare = r.CostShare
	if costShare <= 0 {
		costShare = defaultRollUpCostShareThreshold
	}
	durationMinutes = r.DurationMinutes
	if durationMinutes <= 0 {
		durationMinutes = defaultRollUpDurationThresholdMinutes
	}
	return costShare, durationMinutes
}

// DailyInput は BuildDaily への入力。1 日分のセッションと、集計に必要な補助情報を持つ。
type DailyInput struct {
	Date          string         // YYYY-MM-DD
	Loc           *time.Location // 日付境界の判定に使う。nil なら time.Local
	Sessions      []SessionData  // その日のセッション
	Prices        *pricing.Table
	Goals         func(projectPath string) string // 設定由来。nil 可
	PromptVersion string
	// RollupThreshold はプロジェクト別集計での丸めしきい値。ゼロ値ならパッケージ既定値を使う。
	RollupThreshold RollupThreshold
}

// SessionData は BuildDaily が 1 セッションについて必要とする情報の束。
// 生のメッセージ本文は持たず、集計とカード化に必要な行データだけを渡す。
type SessionData struct {
	Row      store.SessionRow
	Usage    []store.UsageRow
	Eval     *model.Eval // 未評価なら nil
	Evidence int         // 件数だけでよい
}

// modelAgg はモデル別集計の作業用アキュムレータ。
type modelAgg struct {
	usage    ModelUsage
	sessions map[string]struct{}
	allKnown bool
}

// projectAgg はプロジェクト別集計の作業用アキュムレータ。
type projectAgg struct {
	stat ProjectStat
	// orphanSidechains / smallCount は RolledUp.Reason の文言組み立てにのみ使う内部カウンタ。
	// 親が見つからなかった sidechain の数と、しきい値未満で丸めた非 sidechain セッションの数を
	// 分けて数え、実際に発生した理由だけを Reason に繋げるために持つ。
	orphanSidechains int
	smallCount       int
}

// sessionCalc は 1 セッション分の中間計算結果。Pass 1 で全セッション分（sidechain 含む）を
// 作り、Pass 2 でカード化、Pass 3 で sidechain の親への畳み込みに使う。
type sessionCalc struct {
	row      store.SessionRow
	duration float64
	cost     float64 // このセッション自身のコスト（子を含まない）
	allKnown bool
	models   []string
	eval     *model.Eval
	evidence int
}

// newFacets は空（nil ではない）の Facets を返す。評価が 1 件も無くても
// レポート側の nil チェックを不要にするために使う。
func newFacets() Facets {
	return Facets{
		Outcome:          map[string]int{},
		ArtifactValue:    map[string]int{},
		InterventionCost: map[string]int{},
		ModelFit:         map[string]int{},
		Ownership:        map[string]int{},
		LearningValue:    map[string]int{},
		GoalCategory:     map[string]int{},
		Confidence:       map[string]int{},
	}
}

// addFacetsFromEval は 1 件の評価結果を Facets の該当フィールドへ加算する。
// Daily.Facets とプロジェクト単位の Facets は同じ形・同じロジックで埋めるため、
// 加算処理を共通化する。
func addFacetsFromEval(f *Facets, ev *model.Eval) {
	addFacet(f.Outcome, ev.Outcome)
	addFacet(f.ArtifactValue, ev.ArtifactValue)
	addFacet(f.InterventionCost, ev.InterventionCost.Level)
	addFacet(f.ModelFit, ev.ModelFit.Verdict)
	addFacet(f.Ownership, ev.Ownership.Level)
	addFacet(f.LearningValue, ev.LearningValue)
	addFacet(f.GoalCategory, ev.GoalCategory)
	addFacet(f.Confidence, ev.Confidence)
	if ev.Rework.Occurred {
		f.ReworkOccurred++
	}
}

// isHighlightWorthy は、そのセッションを個別に載せる価値があるかどうかを判定する。
// TotalCostUSD（子を含む）と DurationMinutes のどちらかが十分大きければ true。
//
// 例外: outcome が achieved かつ artifact_value が durable のセッションは、小さくても
// 価値に直結した作業なので丸めない（丸めてしまうと「価値への繋がりが弱い作業で埋まる」
// という元の課題を別の形で再現してしまう）。
func isHighlightWorthy(card *SessionCard, dayTotalCost, costShareThreshold, durationThreshold float64) bool {
	if card.Eval != nil && card.Eval.Outcome == "achieved" && card.Eval.ArtifactValue == "durable" {
		return true
	}

	costShare := 0.0
	if dayTotalCost > 0 {
		costShare = card.TotalCostUSD / dayTotalCost
	}
	smallCost := costShare < costShareThreshold
	smallDuration := card.DurationMinutes < durationThreshold

	// 「コスト・時間の両方が小さい」場合だけ丸める。どちらかが大きければ個別掲載する。
	return !(smallCost && smallDuration)
}

// BuildDaily は 1 日分のセッションと評価から Daily を組み立てる。
// AI 生成部分（Narrative / Retro）は空のまま返す（Synthesize が埋める）。
func BuildDaily(in DailyInput) (*Daily, error) {
	if in.Prices == nil {
		return nil, fmt.Errorf("BuildDaily: Prices が nil")
	}

	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	// Date の妥当性チェックに Loc を使う。値そのものは境界フィルタには使わない
	// （渡された Sessions は呼び出し側が既にその日付で抽出済みという前提のため）。
	if _, err := time.ParseInLocation("2006-01-02", in.Date, loc); err != nil {
		return nil, fmt.Errorf("BuildDaily: Date %q のパースに失敗: %w", in.Date, err)
	}

	costShareThreshold, durationThreshold := in.RollupThreshold.resolve()

	d := &Daily{
		Date:        in.Date,
		GeneratedAt: time.Now().UTC(),
		Facets:      newFacets(),
		Meta: Meta{
			PromptVersion: in.PromptVersion,
			// JudgeCostUSD / JudgeSessionIDs はここでは分からない（BuildDaily は judge を呼ばない）。
			// 評価を実行した呼び出し側が、必要ならこの後で埋める。
		},
	}

	modelAggs := map[string]*modelAgg{}
	projectAggs := map[string]*projectAgg{}
	// calcs / calcOrder: セッション ID -> 中間計算結果、および入力順序。
	// sidechain の畳み込み（Pass 2/3）で全セッション分（sidechain 含む）を参照する必要があるため、
	// 1 回目のループではカード化まで行わず、まず計算結果だけを溜める。
	calcs := make(map[string]*sessionCalc, len(in.Sessions))
	calcOrder := make([]string, 0, len(in.Sessions))

	// ---- Pass 1: Totals / ByModel / プロジェクト基礎集計 / Facets / 中間計算 ----
	for _, sd := range in.Sessions {
		row := sd.Row

		duration := row.EndedAt.Sub(row.StartedAt).Minutes()
		if duration < 0 {
			duration = 0
		}

		d.Totals.Sessions++
		switch {
		case row.IsSidechain:
			d.Totals.SidechainSessions++
		case isInteractiveEntrypoint(row.Entrypoint):
			d.Totals.InteractiveSessions++
		default:
			d.Totals.AutomatedSessions++
		}
		d.Totals.DurationMinutes += duration

		modelSet := map[string]struct{}{}
		var sessionCost float64
		sessionAllKnown := true

		for _, u := range sd.Usage {
			d.Totals.InputTokens += int64(u.InputTokens)
			d.Totals.OutputTokens += int64(u.OutputTokens)
			d.Totals.CacheReadTokens += int64(u.CacheRead)
			d.Totals.CacheWriteTokens += int64(u.CacheCreation5m) + int64(u.CacheCreation1h)

			if u.CostKnown {
				d.Totals.CostUSD += u.CostUSD
				sessionCost += u.CostUSD
			} else {
				// 未知モデルのコストを 0 として黙って合算しない。件数だけ数える。
				d.Totals.UnpricedEvents++
				sessionAllKnown = false
			}

			if u.Model != "" {
				modelSet[u.Model] = struct{}{}
			}

			ma, ok := modelAggs[u.Model]
			if !ok {
				ma = &modelAgg{sessions: map[string]struct{}{}, allKnown: true}
				ma.usage.Model = u.Model
				modelAggs[u.Model] = ma
			}
			ma.sessions[row.SessionID] = struct{}{}
			ma.usage.Responses++
			ma.usage.InputTokens += int64(u.InputTokens)
			ma.usage.OutputTokens += int64(u.OutputTokens)
			ma.usage.CacheReadTokens += int64(u.CacheRead)
			ma.usage.CacheWriteTokens += int64(u.CacheCreation5m) + int64(u.CacheCreation1h)
			if u.CostKnown {
				ma.usage.CostUSD += u.CostUSD
			} else {
				ma.allKnown = false
			}
		}

		// SessionCard.Models は重複を除き、出現順ではなくアルファベット順で固定する。
		models := make([]string, 0, len(modelSet))
		for m := range modelSet {
			models = append(models, m)
		}
		sort.Strings(models)

		pa, ok := projectAggs[row.ProjectPath]
		if !ok {
			goal := ""
			if in.Goals != nil {
				goal = in.Goals(row.ProjectPath)
			}
			pa = &projectAgg{stat: ProjectStat{
				ProjectPath:  row.ProjectPath,
				ProjectLabel: row.ProjectLabel,
				Goal:         goal,
				Facets:       newFacets(),
			}}
			projectAggs[row.ProjectPath] = pa
		}
		// プロジェクトの Sessions / DurationMinutes / CostUSD は sidechain を含む全セッションの
		// 合計（Totals と同じ考え方）。畳むのは並べ方（Highlights/RolledUp）であって集計ではない。
		pa.stat.Sessions++
		pa.stat.DurationMinutes += duration
		pa.stat.CostUSD += sessionCost

		if sd.Eval == nil {
			// 方針として評価しない sidechain は「評価に失敗・未実施」としてカウントしない。
			// 評価対象外であることと評価漏れは別の問題であり、混同するとレポートの読者を惑わす。
			// ワークツリーの sidechain は評価対象なので、未評価なら数える。
			if !row.IsSidechain || row.IsWorktreeSidechain() {
				d.Meta.UnevaluatedSessions++
			}
		} else {
			addFacetsFromEval(&d.Facets, sd.Eval)
			pa.stat.EvaluatedSessions++
			addFacetsFromEval(&pa.stat.Facets, sd.Eval)
		}

		// メッセージが 1 件も取り込めていないセッションは、本文が失われている（取り込み失敗等）
		// とみなす。厳密な判定ではないが、他に手がかりが無いための近似。
		if row.MessageCount == 0 {
			d.Meta.MissingTranscripts++
		}

		calcs[row.SessionID] = &sessionCalc{
			row:      row,
			duration: duration,
			cost:     sessionCost,
			allKnown: sessionAllKnown,
			models:   models,
			eval:     sd.Eval,
			evidence: sd.Evidence,
		}
		calcOrder = append(calcOrder, row.SessionID)
	}

	// ---- Pass 2: セッションのカード化。sidechain は独立したカードにしないが、
	// ワークツリーで動いたものだけは 1 本の作業として個別に扱う（親の「委譲 N 件」に
	// 埋めると、その日の実作業の大半が誰にも読まれないまま消えるため）。----
	cardByID := make(map[string]*SessionCard, len(calcs))
	projectCards := map[string][]*SessionCard{}
	for _, id := range calcOrder {
		c := calcs[id]
		if c.row.IsSidechain && !c.row.IsWorktreeSidechain() {
			continue
		}
		card := &SessionCard{
			SessionID:       c.row.SessionID,
			ProjectLabel:    c.row.ProjectLabel,
			Worktree:        c.row.Worktree,
			Title:           c.row.Title,
			FirstPrompt:     c.row.FirstPrompt,
			StartedAt:       c.row.StartedAt,
			DurationMinutes: c.duration,
			IsSidechain:     c.row.IsSidechain,
			Entrypoint:      c.row.Entrypoint,
			ExecutionMode:   executionMode(c.row.Entrypoint),
			Models:          c.models,
			CostUSD:         c.cost,
			Priced:          c.allKnown,
			EvidenceCount:   c.evidence,
			Eval:            c.eval,
		}
		cardByID[id] = card
		projectCards[c.row.ProjectPath] = append(projectCards[c.row.ProjectPath], card)
	}

	// ---- Pass 3: sidechain のコストを親に畳み込む。親が見つからなければ「孤児」として
	// そのプロジェクトの RolledUp に計上する（合計を失わない）。----
	for _, id := range calcOrder {
		c := calcs[id]
		if !c.row.IsSidechain || c.row.IsWorktreeSidechain() {
			// ワークツリーのものは Pass 2 でカード化済み。ここで親にも足すと
			// 同じコストを二重に数えることになる。
			continue
		}

		if c.row.ParentSessionID != "" {
			if parent, ok := cardByID[c.row.ParentSessionID]; ok {
				parent.ChildSessions++
				parent.ChildCostUSD += c.cost
				continue
			}
		}

		// 親が見つからない sidechain（親がその日の対象外、あるいは親自身も sidechain で
		// カード化されていない多段委譲など）。Meta には件数を出すのに適当なフィールドが
		// 無いため、代わりにそのセッションが属するプロジェクトの RolledUp に「孤児」として
		// 計上する。カードとしては表示しないが、合計（Sessions/Duration/CostUSD）は失わない。
		pa := projectAggs[c.row.ProjectPath]
		if pa == nil {
			// Pass 1 で必ず作られるはずだが、念のため防御的に補う。
			pa = &projectAgg{stat: ProjectStat{
				ProjectPath:  c.row.ProjectPath,
				ProjectLabel: c.row.ProjectLabel,
				Facets:       newFacets(),
			}}
			projectAggs[c.row.ProjectPath] = pa
		}
		pa.orphanSidechains++
		pa.stat.RolledUp.Sessions++
		pa.stat.RolledUp.DurationMinutes += c.duration
		pa.stat.RolledUp.CostUSD += c.cost
	}

	// ---- Pass 4: TotalCostUSD = CostUSD + ChildCostUSD を確定させる ----
	for _, card := range cardByID {
		card.TotalCostUSD = card.CostUSD + card.ChildCostUSD
	}

	// ---- Pass 5: プロジェクトごとに Highlights / RolledUp へ振り分け、CostShare / AchievedRatio を算出 ----
	dayTotalCost := d.Totals.CostUSD
	for _, pa := range projectAggs {
		cards := projectCards[pa.stat.ProjectPath]
		// Highlights は TotalCostUSD 降順、同値なら StartedAt 昇順で安定させる。
		sort.SliceStable(cards, func(i, j int) bool {
			if cards[i].TotalCostUSD != cards[j].TotalCostUSD {
				return cards[i].TotalCostUSD > cards[j].TotalCostUSD
			}
			return cards[i].StartedAt.Before(cards[j].StartedAt)
		})

		for _, card := range cards {
			if isHighlightWorthy(card, dayTotalCost, costShareThreshold, durationThreshold) {
				pa.stat.Highlights = append(pa.stat.Highlights, *card)
				continue
			}
			pa.smallCount++
			pa.stat.RolledUp.Sessions++
			pa.stat.RolledUp.DurationMinutes += card.DurationMinutes
			pa.stat.RolledUp.CostUSD += card.TotalCostUSD
		}

		// RolledUp.Reason は実際に発生した理由（しきい値未満／孤児 sidechain）だけを繋げる。
		// どちらも発生していなければ何も設定しない（RolledUp はゼロ値のまま）。
		var reasons []string
		if pa.smallCount > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"総コストの%.0f%%未満かつ%.0f分未満のセッション",
				costShareThreshold*100, durationThreshold))
		}
		if pa.orphanSidechains > 0 {
			reasons = append(reasons, "親セッションが見つからなかったサブエージェント")
		}
		if len(reasons) > 0 {
			pa.stat.RolledUp.Reason = strings.Join(reasons, "、")
		}

		if dayTotalCost > 0 {
			pa.stat.CostShare = pa.stat.CostUSD / dayTotalCost
		}
		if pa.stat.EvaluatedSessions > 0 {
			pa.stat.AchievedRatio = float64(pa.stat.Facets.Outcome["achieved"]) / float64(pa.stat.EvaluatedSessions)
		} else {
			// 評価が 0 件の日は -1 にして「0%」と「データなし」を区別する。
			pa.stat.AchievedRatio = -1
		}
	}

	for name, ma := range modelAggs {
		ma.usage.Sessions = len(ma.sessions)
		ma.usage.Priced = ma.allKnown
		d.ByModel = append(d.ByModel, ma.usage)
		if !ma.allKnown {
			d.Meta.UnknownModels = append(d.Meta.UnknownModels, name)
		}
	}
	sort.Strings(d.Meta.UnknownModels)
	// ByModel はコスト降順。同額なら実行ごとに順序が変わらないようモデル名昇順で tie-break する。
	sort.Slice(d.ByModel, func(i, j int) bool {
		if d.ByModel[i].CostUSD != d.ByModel[j].CostUSD {
			return d.ByModel[i].CostUSD > d.ByModel[j].CostUSD
		}
		return d.ByModel[i].Model < d.ByModel[j].Model
	})

	for _, pa := range projectAggs {
		d.ByProject = append(d.ByProject, pa.stat)
	}
	// ByProject もコスト降順。同額ならプロジェクトパス昇順で tie-break する。
	sort.Slice(d.ByProject, func(i, j int) bool {
		if d.ByProject[i].CostUSD != d.ByProject[j].CostUSD {
			return d.ByProject[i].CostUSD > d.ByProject[j].CostUSD
		}
		return d.ByProject[i].ProjectPath < d.ByProject[j].ProjectPath
	})

	// Daily.Sessions は sidechain を畳んだ後の親のみ（全セッションのカード）。
	// 開始時刻昇順、同時刻なら session_id 昇順で tie-break する。
	sessionCards := make([]SessionCard, 0, len(cardByID))
	for _, card := range cardByID {
		sessionCards = append(sessionCards, *card)
	}
	sort.Slice(sessionCards, func(i, j int) bool {
		if !sessionCards[i].StartedAt.Equal(sessionCards[j].StartedAt) {
			return sessionCards[i].StartedAt.Before(sessionCards[j].StartedAt)
		}
		return sessionCards[i].SessionID < sessionCards[j].SessionID
	})
	d.Sessions = sessionCards

	return d, nil
}

// addFacet は eval の 1 フィールド分を Facets の該当マップに加算する。
// 値が空文字列（未設定）のフィールドは分布を歪めるので数えない。
func addFacet(m map[string]int, value string) {
	if value == "" {
		return
	}
	m[value]++
}

// BuildSeries は日次 Daily の列から、任意期間の HTML 用 Series を組み立てる。
// [from, to] は YYYY-MM-DD の文字列比較で判定する（この形式は辞書順 = 時系列順に一致する）。
func BuildSeries(from, to string, dailies []*Daily, actions []model.Action) *Series {
	s := &Series{From: from, To: to}

	var filtered []*Daily
	for _, d := range dailies {
		if d == nil {
			continue
		}
		if d.Date < from || d.Date > to {
			continue
		}
		filtered = append(filtered, d)
	}
	// 期間内に Daily が無い日は Point を作らない（0 として捏造しない）。
	// 入力順序に依存しないよう、日付昇順で安定ソートする。
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Date < filtered[j].Date })

	modelAggs := map[string]*ModelUsage{}
	modelKnown := map[string]bool{}

	for _, d := range filtered {
		evaluated, achieved := 0, 0
		for outcome, count := range d.Facets.Outcome {
			evaluated += count
			if outcome == "achieved" {
				achieved += count
			}
		}
		ratio := -1.0
		if evaluated > 0 {
			ratio = float64(achieved) / float64(evaluated)
		}

		costByModel := make(map[string]float64, len(d.ByModel))
		for _, mu := range d.ByModel {
			costByModel[mu.Model] = mu.CostUSD

			agg, ok := modelAggs[mu.Model]
			if !ok {
				agg = &ModelUsage{Model: mu.Model}
				modelAggs[mu.Model] = agg
				modelKnown[mu.Model] = true
			}
			agg.Sessions += mu.Sessions
			agg.Responses += mu.Responses
			agg.InputTokens += mu.InputTokens
			agg.OutputTokens += mu.OutputTokens
			agg.CacheReadTokens += mu.CacheReadTokens
			agg.CacheWriteTokens += mu.CacheWriteTokens
			agg.CostUSD += mu.CostUSD
			if !mu.Priced {
				modelKnown[mu.Model] = false
			}
		}

		s.Points = append(s.Points, Point{
			Date:            d.Date,
			Sessions:        d.Totals.Sessions,
			DurationMinutes: d.Totals.DurationMinutes,
			CostUSD:         d.Totals.CostUSD,
			CostByModel:     costByModel,
			Outcome:         copyIntMap(d.Facets.Outcome),
			ModelFit:        copyIntMap(d.Facets.ModelFit),
			Ownership:       copyIntMap(d.Facets.Ownership),
			// 評価済みセッションが 0 件の日は -1（「0%」と「データなし」の区別）。
			AchievedRatio: ratio,
		})
	}

	for name, agg := range modelAggs {
		agg.Priced = modelKnown[name]
		s.ByModel = append(s.ByModel, *agg)
	}
	sort.Slice(s.ByModel, func(i, j int) bool {
		if s.ByModel[i].CostUSD != s.ByModel[j].CostUSD {
			return s.ByModel[i].CostUSD > s.ByModel[j].CostUSD
		}
		return s.ByModel[i].Model < s.ByModel[j].Model
	})

	for _, a := range actions {
		if a.CreatedOn < from || a.CreatedOn > to {
			continue
		}
		s.Actions = append(s.Actions, a)
	}
	sort.Slice(s.Actions, func(i, j int) bool {
		if s.Actions[i].CreatedOn != s.Actions[j].CreatedOn {
			return s.Actions[i].CreatedOn < s.Actions[j].CreatedOn
		}
		return s.Actions[i].ID < s.Actions[j].ID
	})

	return s
}

// copyIntMap は Point に詰める前に Facets のマップを複製する。
// Daily 側のマップと Point 側のマップが同一インスタンスを共有しないようにするため。
func copyIntMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
