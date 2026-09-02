package judge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// DefaultMaxChars は BuildSessionPrompt が生成する台本全体の既定の文字数上限。
// claude 呼び出しコストを抑えつつ、通常のセッションなら中略なしで収まる程度の余裕を持たせている。
const DefaultMaxChars = 20000

// 会話 1 行あたりの文字数上限。1 発話（特にユーザーの大きな貼り付け）が予算計算を
// 壊さないよう、行単位でも上限を設ける。
const maxLineRunes = 500

// 中略時に、省略区間中から抜粋するユーザー発話の最大件数。
const maxOmittedUserExcerpt = 5

// 委譲セクションで個別に列挙する子セッションの最大件数（コスト降順）。
// これを超える分は「他 N 件」とまとめる。件数が多いセッション（実データで
// 1 親あたり多数のサブエージェントを持つケースがある）で台本が肥大化しない
// ようにするための上限。
const maxDelegationListed = 5

// ChildSummary は親セッションから委譲されたサブエージェント 1 件の要約。
// サブエージェント自身も個別に AI 評価するが、その台本（会話・ツール呼び出し）は
// 親の評価には渡さない。親に渡すのはこの要約と子の評価結果だけであり、委譲の妥当性は
// 「親がどう委譲したか」として評価する。
type ChildSummary struct {
	SessionID       string
	AgentName       string // サブエージェントの説明（.meta.json の description 由来）。空のこともある
	DurationMinutes float64
	CostUSD         float64
	Priced          bool // false ならコストは信用できない（単価未登録）
	MessageCount    int
	ToolErrorCount  int

	// Models は子セッションで使われたモデル名（利用量の多い順）。空のこともある。
	//
	// 委譲は向きによって評価基準が変わる（親より安いモデルへ下ろしたのならコストの
	// 最適化、親より高いモデルへ上げたのなら難所の外注）。モデル名が渡っていないと
	// 評価者はその向きを判別できず、どちらも同じ物差しで見てしまう。
	Models []string

	// Evaluated は子セッションの AI 評価結果が揃っているかどうか。false のときは
	// 以下の評価由来のフィールドをすべて無視する（同じ実行の中で子がまだ評価されて
	// いない、評価に失敗した、といったときに起こる）。
	Evaluated bool
	// Outcome は子セッションの達成度（achieved / partial / abandoned / exploratory）。
	// 委譲した作業が実際に完遂されたのかを、要約からの推測ではなく評価結果として渡す。
	Outcome string
	// OutcomeSummary は子セッションの短い要約（評価の outcome_summary）。
	OutcomeSummary string
	// ReworkOccurred は子セッションで手戻りが起きたかどうか。委譲の粒度や
	// 親からの指示の過不足を読むための材料になる。
	ReworkOccurred bool
}

// SessionPromptInput は BuildSessionPrompt への入力。
type SessionPromptInput struct {
	Session  *model.Session
	Evidence []model.Evidence
	Goal     string // このプロジェクトで重視する価値（設定由来）。空でもよい
	MaxChars int    // 台本全体の上限。0 なら DefaultMaxChars

	// Children は、このセッションからサブエージェントとして委譲された子セッションの
	// 要約。空なら「委譲なし」であり、台本には委譲セクション自体を出力しない。
	Children []ChildSummary
	// SessionCostUSD はこのセッション自身（子セッションを含まない）のコスト。
	// Children とあわせて「委譲にコストのどれだけを費やしたか」の対比に使う。
	// SessionCostPriced が false のときは無視される。
	SessionCostUSD float64
	// SessionCostPriced は SessionCostUSD が信用できる値かどうか
	// （false なら単価未登録などでコストが算出できていない）。
	SessionCostPriced bool
}

// BuildSessionPrompt は 1 セッションを AI 評価用のテキストに整形する。
//
// 含めるもの: セッションのメタ情報、使用モデル・effort とトークン量の内訳、
// 会話の流れ（IsMeta なメッセージは除外）、ツール呼び出しの要約、成果物 evidence、
// プロジェクトの目標。
//
// 上限を超える場合は会話部分の中盤だけを省略する（前半・後半は残す）。単純な
// 末尾切り詰めだとセッションの結論部分が失われてしまうため。省略した際は件数を
// 明示し、省略区間中のユーザー発話は数件抜粋して残す（ユーザー発話は
// アシスタント発話より優先して残す）。
func BuildSessionPrompt(in SessionPromptInput) (string, error) {
	if in.Session == nil {
		return "", fmt.Errorf("BuildSessionPrompt: session が nil です")
	}

	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}

	meta := buildMetaSection(in.Session)
	modelsSection := buildModelUsageSection(in.Session)
	delegationSection := buildDelegationSection(in.Children, parentModels(in.Session), in.SessionCostUSD, in.SessionCostPriced)
	toolsSection := buildToolSummarySection(in.Session)
	evidenceSection := buildEvidenceSection(in.Evidence)
	goalSection := buildGoalSection(in.Goal)

	// delegationSection は Children が空なら "" になる（セクション自体を出さない）。
	// 委譲はメタ情報に近く会話本文より優先して残したいので、切り詰め対象になる
	// 会話セクションではなく、常に残る fixed 側に含める。
	fixed := meta + "\n" + modelsSection + "\n"
	if delegationSection != "" {
		fixed += delegationSection + "\n"
	}
	fixed += toolsSection + "\n" + evidenceSection + "\n" + goalSection

	// 会話セクションに残せる予算。固定セクションが（evidence 過多などで）異常に
	// 大きい場合でも、会話に多少の余白は必ず与える。
	budgetForConv := maxChars - len(fixed)
	if budgetForConv < 500 {
		budgetForConv = 500
	}

	convSection := buildConversationSection(in.Session.Messages, budgetForConv)

	full := fixed + "\n" + convSection

	// 安全弁: 固定セクション自体が大きく、上の budgetForConv 下限保証によって
	// 全体が maxChars を超えてしまうことがありうる。その場合は最後に単純切り詰め。
	if len(full) > maxChars {
		full = full[:maxChars] + "\n...(文字数上限のため以降を切り詰め)\n"
	}

	return full, nil
}

func buildMetaSection(s *model.Session) string {
	var b strings.Builder
	b.WriteString("## セッション基本情報\n")
	// どのエージェントのセッションかは評価の前提に効く（entrypoint の意味も、
	// 使えるツールの種類も、モデルの選択肢も違う）。ソースを伏せると、評価者は
	// 手前にあるプロンプトの記述から Claude Code だと思い込んでしまう。
	fmt.Fprintf(&b, "- エージェント: %s\n", orDash(s.Source))
	fmt.Fprintf(&b, "- プロジェクト: %s (%s)\n", orDash(s.ProjectLabel), orDash(s.ProjectPath))
	fmt.Fprintf(&b, "- ブランチ: %s\n", orDash(s.GitBranch))
	if strings.TrimSpace(s.Worktree) != "" {
		// ワークツリーは元のプロジェクトに帰属させて集計しているので、
		// どの作業ツリーだったのかはここで明示しないと評価者に見えない。
		fmt.Fprintf(&b, "- ワークツリー: %s（元のプロジェクトでの並行作業）\n", s.Worktree)
	}
	fmt.Fprintf(&b, "- entrypoint: %s\n", orDash(s.Entrypoint))
	// 実行形態は評価軸の読み替え（非対話では実行中の介入も検収も原理的にできない）に
	// 直結するので、entrypoint の生値だけに解釈を委ねず、解釈済みのラベルも渡す。
	switch {
	case strings.TrimSpace(s.Entrypoint) == "":
		b.WriteString("- 実行形態: 不明（entrypoint が取れていない。対話実行に準じて扱う）\n")
	case model.IsInteractiveEntrypoint(s.Entrypoint):
		b.WriteString("- 実行形態: 対話実行（ユーザーが同席し、実行中に軌道修正も検収もできる）\n")
	default:
		b.WriteString("- 実行形態: 非対話実行（claude -p / codex exec などの自動実行。実行中にユーザーは介入も検収もできず、最初のプロンプトが仕様のすべて）\n")
	}
	fmt.Fprintf(&b, "- サブエージェント実行: %s\n", yesNo(s.IsSidechain))
	if s.IsSidechain {
		fmt.Fprintf(&b, "- 親セッションID: %s\n", orDash(s.ParentSessionID))
	}
	fmt.Fprintf(&b, "- 開始: %s\n", formatTime(s.StartedAt))
	fmt.Fprintf(&b, "- 終了: %s\n", formatTime(s.EndedAt))
	fmt.Fprintf(&b, "- 経過時間: %s\n", s.Duration().Round(time.Second))
	if strings.TrimSpace(s.Title) != "" {
		fmt.Fprintf(&b, "- タイトル: %s\n", s.Title)
	}
	if strings.TrimSpace(s.FirstPrompt) != "" {
		fmt.Fprintf(&b, "- 最初のユーザー発話: %s\n", s.FirstPrompt)
	}
	return b.String()
}

// modelStat は使用モデル・effort ごとの利用回数とトークン量の内訳。
type modelStat struct {
	Model, Effort                                        string
	Turns                                                int
	InputTokens, OutputTokens, ThinkingTokens, CacheRead int
	CacheCreation5m, CacheCreation1h                     int
}

// buildModelUsageSection は「モデル選択・委譲の妥当性」の評価軸に必須の材料。
// アシスタント発話ごとに Model/Effort の組で集計する。
func buildModelUsageSection(s *model.Session) string {
	var b strings.Builder
	b.WriteString("## 使用モデル・トークン量\n")

	stats := map[string]*modelStat{}
	var order []string
	for _, m := range s.Messages {
		if m.Role != model.RoleAssistant || m.Model == "" {
			continue
		}
		key := m.Model + "|" + m.Effort
		st, ok := stats[key]
		if !ok {
			st = &modelStat{Model: m.Model, Effort: m.Effort}
			stats[key] = st
			order = append(order, key)
		}
		st.Turns++
		if m.Usage != nil {
			st.InputTokens += m.Usage.InputTokens
			st.OutputTokens += m.Usage.OutputTokens
			st.ThinkingTokens += m.Usage.ThinkingTokens
			st.CacheRead += m.Usage.CacheRead
			st.CacheCreation5m += m.Usage.CacheCreation5m
			st.CacheCreation1h += m.Usage.CacheCreation1h
		}
	}

	if len(order) == 0 {
		b.WriteString("(モデル使用情報なし)\n")
		return b.String()
	}

	sort.Strings(order)
	for _, key := range order {
		st := stats[key]
		fmt.Fprintf(&b, "- model=%s effort=%s: %d ターン, input=%d output=%d thinking=%d cache_read=%d cache_creation_5m=%d cache_creation_1h=%d\n",
			st.Model, orDash(st.Effort), st.Turns, st.InputTokens, st.OutputTokens, st.ThinkingTokens, st.CacheRead, st.CacheCreation5m, st.CacheCreation1h)
	}
	return b.String()
}

// buildDelegationSection は、このセッションからサブエージェントへ委譲した内容の要約を
// 組み立てる。children が空なら "" を返し、呼び出し側はセクションごと省略する
// （「委譲なし」という行すら出さない。評価対象の大半を占めるノイズになるため）。
//
// サブエージェントは個別に AI 評価しない方針のため、ここが「委譲の妥当性」を
// 評価するための唯一の材料になる。件数・コスト・時間の合計、親自身のコストとの
// 対比、そしてコスト降順で上位 maxDelegationListed 件の個別要約を出す。
// 単価未登録（Priced == false）の子は金額を「単価未登録」と明示し、合計金額の
// 計算には含めない（過小評価を金額があるかのように見せないため）。
//
// parentModels（親が使ったモデル、利用量の多い順）を並べて出すのは、委譲の向きを
// 評価者が判別できるようにするため。安いモデルへ下ろした委譲と高いモデルへ上げた
// 委譲は狙いが違い、同じ物差しでは評価できない。
func buildDelegationSection(children []ChildSummary, parentModels []string, sessionCostUSD float64, sessionCostPriced bool) string {
	if len(children) == 0 {
		return ""
	}

	// コスト降順にソートする。単価未登録の子はコストを比較できないため、
	// 単価登録済みの子より後ろにまとめる（順序自体は安定させる）。
	sorted := make([]ChildSummary, len(children))
	copy(sorted, children)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Priced != b.Priced {
			return a.Priced // Priced な方を先に
		}
		if a.Priced {
			return a.CostUSD > b.CostUSD
		}
		return false // 両方 unpriced なら元の順序を保つ
	})

	var (
		totalDuration   float64
		totalMessages   int
		totalToolErrors int
		pricedCostUSD   float64
		pricedCount     int
		unpricedCount   int
		evaluatedCount  int
		reworkCount     int
	)
	// 子の達成度は「どの値が何件か」を出す。列挙値は固定だが、出現順を保って
	// 並べたいので map と順序の両方を持つ。
	outcomeCounts := map[string]int{}
	var outcomeOrder []string
	for _, c := range sorted {
		totalDuration += c.DurationMinutes
		totalMessages += c.MessageCount
		totalToolErrors += c.ToolErrorCount
		if c.Evaluated {
			evaluatedCount++
			if o := strings.TrimSpace(c.Outcome); o != "" {
				if _, ok := outcomeCounts[o]; !ok {
					outcomeOrder = append(outcomeOrder, o)
				}
				outcomeCounts[o]++
			}
			if c.ReworkOccurred {
				reworkCount++
			}
		}
		if c.Priced {
			pricedCostUSD += c.CostUSD
			pricedCount++
		} else {
			unpricedCount++
		}
	}

	var b strings.Builder
	b.WriteString("## 委譲（サブエージェントへの委譲）\n")
	b.WriteString("子セッションも個別に AI 評価しており、その結果（達成度・手戻り・短い要約）をここに載せている。\n")
	b.WriteString("ただし子セッションの台本（会話・個々のツール呼び出し）は評価者には見えていないので、\n")
	b.WriteString("子の内部品質を断定しないこと。\n")
	b.WriteString("**委譲するかどうか、どのサブエージェントに何を任せるかを決めたのは AI であってユーザーではない。**\n")
	b.WriteString("したがって「委譲した成果をユーザーが検収すべきだった」という指摘は書かないこと（ユーザーに\n")
	b.WriteString("打ち手が無い）。ここを材料に書けるのは、ユーザーが最初の依頼で示しておけば委譲の空回りを\n")
	b.WriteString("防げたこと（前提・完了条件・触ってよい範囲）だけである。\n")
	b.WriteString("親と子のモデルを見比べ、委譲の向き（親より安いモデルへ下ろしたのか、高いモデルへ\n")
	b.WriteString("上げたのか、同等か）を判別すること。向きによって評価基準が変わる。\n")

	if len(parentModels) > 0 {
		fmt.Fprintf(&b, "- 親セッションのモデル: %s\n", strings.Join(parentModels, ", "))
	} else {
		b.WriteString("- 親セッションのモデル: 不明\n")
	}
	fmt.Fprintf(&b, "- 委譲件数: %d 件\n", len(sorted))
	// 個別の列挙は上位 maxDelegationListed 件で打ち切るので、モデルの内訳だけは
	// 全件から集計して出す。切り捨てた側に別のモデルが混ざっていると、委譲の向きを
	// 読み違えることになるため。
	fmt.Fprintf(&b, "- 委譲先のモデル内訳: %s\n", formatChildModelBreakdown(sorted))
	fmt.Fprintf(&b, "- 委譲先の合計時間: %s\n", formatDelegationMinutes(totalDuration))
	fmt.Fprintf(&b, "- 委譲先の合計メッセージ数: %d 件\n", totalMessages)
	fmt.Fprintf(&b, "- 委譲先の合計ツールエラー数: %d 件\n", totalToolErrors)
	if evaluatedCount == 0 {
		// 子がまだ評価されていない実行（評価に失敗した、順序の都合で間に合わなかった）。
		// 「達成できなかった」と読み違えられないよう、無いことをはっきり書く。
		b.WriteString("- 委譲先の評価: 未評価（この実行では子の達成度を判断材料にできない）\n")
	} else {
		fmt.Fprintf(&b, "- 委譲先の達成度内訳: %s\n", formatChildOutcomes(outcomeCounts, outcomeOrder, len(sorted)-evaluatedCount))
		fmt.Fprintf(&b, "- 委譲先で手戻りが起きた件数: %d 件（評価済み %d 件中）\n", reworkCount, evaluatedCount)
	}

	switch {
	case unpricedCount == 0:
		fmt.Fprintf(&b, "- 委譲先の合計コスト: %s\n", formatDelegationMoney(pricedCostUSD, true))
	case pricedCount == 0:
		b.WriteString("- 委譲先の合計コスト: 単価未登録（全件）\n")
	default:
		fmt.Fprintf(&b, "- 委譲先の合計コスト: %s（単価登録済み %d 件分。単価未登録が他に %d 件あり、これらは含まれていない）\n",
			formatDelegationMoney(pricedCostUSD, true), pricedCount, unpricedCount)
	}

	if sessionCostPriced && pricedCount > 0 {
		total := sessionCostUSD + pricedCostUSD
		share := 0.0
		if total > 0 {
			share = pricedCostUSD / total * 100
		}
		fmt.Fprintf(&b, "- 親セッション自身のコスト: %s（委譲先込みの合計 %s のうち委譲が占める割合: %.1f%%）\n",
			formatDelegationMoney(sessionCostUSD, true), formatDelegationMoney(total, true), share)
	} else if sessionCostPriced {
		fmt.Fprintf(&b, "- 親セッション自身のコスト: %s\n", formatDelegationMoney(sessionCostUSD, true))
	}

	listed := sorted
	rest := 0
	if len(listed) > maxDelegationListed {
		rest = len(listed) - maxDelegationListed
		listed = listed[:maxDelegationListed]
	}

	b.WriteString("\n上位の委譲（コスト降順。単価未登録は末尾）:\n")
	for _, c := range listed {
		name := c.AgentName
		if strings.TrimSpace(name) == "" {
			name = "(説明なし)"
		}
		fmt.Fprintf(&b, "- [%s] model=%s, %s, %s, %dメッセージ, ツールエラー%d件%s\n",
			name, formatChildModels(c.Models), formatDelegationMinutes(c.DurationMinutes),
			formatDelegationMoney(c.CostUSD, c.Priced), c.MessageCount, c.ToolErrorCount,
			formatChildVerdict(c))
		if sum := strings.TrimSpace(c.OutcomeSummary); c.Evaluated && sum != "" {
			fmt.Fprintf(&b, "  要約: %s\n", sum)
		}
	}
	if rest > 0 {
		fmt.Fprintf(&b, "...(他 %d 件)\n", rest)
	}

	return b.String()
}

// formatChildOutcomes は子セッションの達成度の内訳を 1 行にまとめる。
// unevaluated が 0 より大きいときだけ末尾に付けるのは、評価済みの件数と
// 委譲件数が食い違って見えるのを防ぐため。
func formatChildOutcomes(counts map[string]int, order []string, unevaluated int) string {
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s %d 件", k, counts[k]))
	}
	out := strings.Join(parts, ", ")
	if unevaluated > 0 {
		out += fmt.Sprintf("（未評価 %d 件）", unevaluated)
	}
	return out
}

// formatChildVerdict は個別列挙の行に足す、子セッションの評価結果の短い表記。
// 未評価なら何も足さない（「不明」と書くと行が伸びるだけで情報が増えない）。
func formatChildVerdict(c ChildSummary) string {
	if !c.Evaluated {
		return ""
	}
	out := ""
	if o := strings.TrimSpace(c.Outcome); o != "" {
		out += ", 達成度=" + o
	}
	if c.ReworkOccurred {
		out += ", 手戻りあり"
	}
	return out
}

// parentModels は親セッション自身が使ったモデル名を利用量（ターン数）の多い順に返す。
// 委譲の向きは「親のモデルと子のモデルの比較」でしか決まらないため、委譲セクションに
// 親側を並べて出すのに使う。
func parentModels(s *model.Session) []string {
	if s == nil {
		return nil
	}
	turns := map[string]int{}
	var order []string
	for _, m := range s.Messages {
		if m.Role != model.RoleAssistant || m.Model == "" {
			continue
		}
		if _, ok := turns[m.Model]; !ok {
			order = append(order, m.Model)
		}
		turns[m.Model]++
	}
	sort.SliceStable(order, func(i, j int) bool {
		if turns[order[i]] != turns[order[j]] {
			return turns[order[i]] > turns[order[j]]
		}
		return order[i] < order[j]
	})
	return order
}

// formatChildModels は子セッション 1 件のモデル表示。未取得なら「不明」と明示する
// （空欄にすると、モデルが同じだったのか分からなかったのかを区別できないため）。
func formatChildModels(models []string) string {
	var kept []string
	for _, m := range models {
		if strings.TrimSpace(m) != "" {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return "不明"
	}
	return strings.Join(kept, "+")
}

// formatChildModelBreakdown は委譲先で使われたモデルを「モデル名 N件」の形にまとめる。
// 1 件の子が複数モデルを使っていればそのすべてに数える。件数降順・同数ならモデル名順。
func formatChildModelBreakdown(children []ChildSummary) string {
	counts := map[string]int{}
	var order []string
	add := func(name string) {
		if _, ok := counts[name]; !ok {
			order = append(order, name)
		}
		counts[name]++
	}
	for _, c := range children {
		seen := map[string]bool{}
		for _, m := range c.Models {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			add(m)
		}
		if len(seen) == 0 {
			add("不明")
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if counts[order[i]] != counts[order[j]] {
			return counts[order[i]] > counts[order[j]]
		}
		return order[i] < order[j]
	})

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, fmt.Sprintf("%s %d件", name, counts[name]))
	}
	return strings.Join(parts, ", ")
}

// formatDelegationMoney は委譲セクション用の金額表示。priced が false の場合は
// 「単価未登録」と明示し、0円と誤読させない。
func formatDelegationMoney(cost float64, priced bool) string {
	if !priced {
		return "単価未登録"
	}
	return fmt.Sprintf("$%.4f", cost)
}

// formatDelegationMinutes は委譲セクション用の所要時間表示。
func formatDelegationMinutes(minutes float64) string {
	return fmt.Sprintf("%.1f分", minutes)
}

type toolStat struct {
	Calls  int
	Errors int
}

// buildToolSummarySection はツール呼び出しを名前ごとに要約する（全文は含めない）。
// RoleAssistant + ToolName != "" が呼び出し、RoleTool + IsError がエラー結果。
func buildToolSummarySection(s *model.Session) string {
	var b strings.Builder
	b.WriteString("## ツール呼び出しの要約\n")

	stats := map[string]*toolStat{}
	var order []string
	for _, m := range s.Messages {
		switch {
		case m.Role == model.RoleAssistant && m.ToolName != "":
			st, ok := stats[m.ToolName]
			if !ok {
				st = &toolStat{}
				stats[m.ToolName] = st
				order = append(order, m.ToolName)
			}
			st.Calls++
		case m.Role == model.RoleTool && m.ToolName != "" && m.IsError:
			st, ok := stats[m.ToolName]
			if !ok {
				st = &toolStat{}
				stats[m.ToolName] = st
				order = append(order, m.ToolName)
			}
			st.Errors++
		}
	}

	if len(order) == 0 {
		b.WriteString("(ツール呼び出しなし)\n")
		return b.String()
	}

	sort.Strings(order)
	for _, name := range order {
		st := stats[name]
		fmt.Fprintf(&b, "- %s: %d 回呼び出し, うちエラー %d 回\n", name, st.Calls, st.Errors)
	}
	return b.String()
}

func buildEvidenceSection(evidence []model.Evidence) string {
	var b strings.Builder
	b.WriteString("## 成果物 (evidence)\n")
	if len(evidence) == 0 {
		b.WriteString("(evidence なし)\n")
		return b.String()
	}
	const bodyCap = 1500
	for _, e := range evidence {
		fmt.Fprintf(&b, "- [%s] %s (%s, %s)\n", e.Kind, orDash(e.Title), orDash(e.Ref), formatTime(e.Timestamp))
		if e.Insertions != 0 || e.Deletions != 0 || e.Files != 0 {
			fmt.Fprintf(&b, "  差分: +%d -%d, %d ファイル\n", e.Insertions, e.Deletions, e.Files)
		}
		body := strings.TrimSpace(e.Body)
		if body != "" {
			r := []rune(body)
			if len(r) > bodyCap {
				body = string(r[:bodyCap]) + "…(省略)"
			}
			fmt.Fprintf(&b, "  本文: %s\n", body)
		}
	}
	return b.String()
}

func buildGoalSection(goal string) string {
	var b strings.Builder
	b.WriteString("## このプロジェクトで重視する価値 (goal)\n")
	if strings.TrimSpace(goal) == "" {
		b.WriteString("(未設定)\n")
		return b.String()
	}
	b.WriteString(goal)
	b.WriteString("\n")
	return b.String()
}

// buildConversationSection は会話の流れを整形する。IsMeta なメッセージは除外する。
// budget（文字数）を超える場合は前半・後半を残し中盤を省略する。ユーザー発話は
// 省略区間からも数件抜粋して残す（アシスタント発話より優先する）。
func buildConversationSection(all []model.Message, budget int) string {
	header := "## 会話\n"

	filtered := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsMeta {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return header + "(会話なし)\n"
	}

	lines := make([]string, len(filtered))
	total := 0
	for i, m := range filtered {
		lines[i] = formatMessageLine(m)
		total += len(lines[i]) + 1
	}

	if total <= budget {
		return header + strings.Join(lines, "\n") + "\n"
	}

	// 前半・後半それぞれの予算。終盤（結論）を失わないよう均等割りにする。
	headBudget := budget * 3 / 5
	tailBudget := budget - headBudget

	headEnd := 0
	used := 0
	for headEnd < len(lines) {
		next := used + len(lines[headEnd]) + 1
		if next > headBudget {
			break
		}
		used = next
		headEnd++
	}

	tailStart := len(lines)
	used = 0
	for tailStart > headEnd {
		next := used + len(lines[tailStart-1]) + 1
		if next > tailBudget {
			break
		}
		used = next
		tailStart--
	}

	if headEnd >= tailStart {
		// 境界条件: 1 行ずつの積み上げで結局すべて収まった。省略なし扱い。
		return header + strings.Join(lines, "\n") + "\n"
	}

	omittedCount := tailStart - headEnd
	var excerpt []string
	for i := headEnd; i < tailStart && len(excerpt) < maxOmittedUserExcerpt; i++ {
		if filtered[i].Role == model.RoleUser {
			excerpt = append(excerpt, formatMessageLine(filtered[i]))
		}
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(strings.Join(lines[:headEnd], "\n"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "...(中略: %d 件のメッセージを省略しました。文字数上限のため中盤を省いています)...\n", omittedCount)
	if len(excerpt) > 0 {
		b.WriteString("\n省略区間中の主なユーザー発話（抜粋）:\n")
		b.WriteString(strings.Join(excerpt, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.Join(lines[tailStart:], "\n"))
	b.WriteString("\n")
	return b.String()
}

// formatMessageLine は 1 メッセージを 1 行のテキストに整形する。
// 1 行あたり maxLineRunes を超える本文は切り詰める（大きな貼り付けが
// 予算計算を壊さないようにするため）。
func formatMessageLine(m model.Message) string {
	text := strings.TrimSpace(m.Text)
	r := []rune(text)
	if len(r) > maxLineRunes {
		text = string(r[:maxLineRunes]) + "…(省略)"
	}

	switch {
	case m.Role == model.RoleAssistant && m.ToolName != "":
		return fmt.Sprintf("#%d [%s] assistant tool_call(%s): %s", m.Seq, formatClock(m.Timestamp), m.ToolName, text)
	case m.Role == model.RoleTool:
		status := "ok"
		if m.IsError {
			status = "ERROR"
		}
		return fmt.Sprintf("#%d [%s] tool_result(%s,%s): %s", m.Seq, formatClock(m.Timestamp), orDash(m.ToolName), status, text)
	case m.Role == model.RoleAssistant:
		modelInfo := ""
		if m.Model != "" {
			modelInfo = fmt.Sprintf("[%s/%s] ", m.Model, orDash(m.Effort))
		}
		return fmt.Sprintf("#%d [%s] assistant: %s%s", m.Seq, formatClock(m.Timestamp), modelInfo, text)
	default:
		return fmt.Sprintf("#%d [%s] %s: %s", m.Seq, formatClock(m.Timestamp), string(m.Role), text)
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(不明)"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "(不明)"
	}
	return t.Format("2006-01-02 15:04:05 MST")
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "??:??:??"
	}
	return t.Format("15:04:05")
}
