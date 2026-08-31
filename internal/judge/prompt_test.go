package judge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

func baseSession() *model.Session {
	started := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	return &model.Session{
		Source:       "claude-code",
		SessionID:    "sess-1",
		ProjectPath:  `C:\proj`,
		ProjectLabel: "proj",
		GitBranch:    "main",
		Entrypoint:   "cli",
		StartedAt:    started,
		EndedAt:      started.Add(10 * time.Minute),
	}
}

func TestBuildSessionPrompt_NilSession(t *testing.T) {
	if _, err := BuildSessionPrompt(SessionPromptInput{}); err == nil {
		t.Fatal("Session が nil のとき error を返すべき")
	}
}

func TestBuildSessionPrompt_EmptySession(t *testing.T) {
	s := &model.Session{}

	var out string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("空セッションで panic した: %v", r)
			}
		}()
		out, err = BuildSessionPrompt(SessionPromptInput{Session: s})
	}()

	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v, want nil", err)
	}
	if !strings.Contains(out, "会話なし") {
		t.Errorf("空セッションの出力に「会話なし」が含まれていない: %q", out)
	}
}

func TestBuildSessionPrompt_MetaMessagesExcluded(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "VISIBLE-USER-TEXT"},
		{Seq: 2, Role: model.RoleUser, Text: "SECRET-META-TEXT", IsMeta: true},
		{Seq: 3, Role: model.RoleAssistant, Text: "VISIBLE-ASSISTANT-TEXT", Model: "claude-sonnet-5"},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{Session: s})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	if !strings.Contains(out, "VISIBLE-USER-TEXT") {
		t.Errorf("非メタのユーザー発話が含まれていない: %q", out)
	}
	if !strings.Contains(out, "VISIBLE-ASSISTANT-TEXT") {
		t.Errorf("非メタのアシスタント発話が含まれていない: %q", out)
	}
	if strings.Contains(out, "SECRET-META-TEXT") {
		t.Errorf("IsMeta なメッセージが除外されずに含まれている: %q", out)
	}
}

// TestBuildSessionPrompt_ExecutionMode は実行形態のラベルが台本に入ることを確認する。
// 評価プロンプトは非対話実行のとき評価軸を読み替える（実行中の介入も検収もできないため）。
// entrypoint の生値だけを渡していると評価側がその判別を推測に頼ることになるので、
// 解釈済みのラベルが必ず出ることを固定する。
func TestBuildSessionPrompt_ExecutionMode(t *testing.T) {
	cases := []struct {
		name       string
		entrypoint string
		want       string
	}{
		{name: "対話", entrypoint: "cli", want: "- 実行形態: 対話実行"},
		{name: "非対話", entrypoint: "sdk-cli", want: "- 実行形態: 非対話実行"},
		{name: "不明", entrypoint: "", want: "- 実行形態: 不明"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSession()
			s.Entrypoint = tc.entrypoint

			out, err := BuildSessionPrompt(SessionPromptInput{Session: s})
			if err != nil {
				t.Fatalf("BuildSessionPrompt() error = %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("出力に %q が含まれていない: %q", tc.want, out)
			}
		})
	}
}

func TestBuildSessionPrompt_EvidenceAndGoalEmbedded(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{
		Session: s,
		Evidence: []model.Evidence{
			{Kind: "commit", Ref: "abcdef1", Title: "COMMIT-TITLE-MARKER", Body: "COMMIT-BODY-MARKER"},
		},
		Goal: "GOAL-TEXT-MARKER",
	})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	for _, want := range []string{"COMMIT-TITLE-MARKER", "COMMIT-BODY-MARKER", "abcdef1", "GOAL-TEXT-MARKER"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が含まれていない: %q", want, out)
		}
	}
}

func TestBuildSessionPrompt_ModelUsageIncluded(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{
			Seq: 1, Role: model.RoleAssistant, Model: "claude-sonnet-5", Effort: "high",
			Text: "work", Usage: &model.Usage{InputTokens: 111, OutputTokens: 222},
		},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{Session: s})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}
	for _, want := range []string{"claude-sonnet-5", "high", "111", "222"} {
		if !strings.Contains(out, want) {
			t.Errorf("モデル使用量セクションに %q が含まれていない: %q", want, out)
		}
	}
}

// TestBuildConversationSection_MiddleTruncation は中盤省略ロジックを直接検証する。
// BuildSessionPrompt 経由だと固定セクション長との兼ね合いで budget の予測が難しいため、
// パッケージ内部の buildConversationSection を直接呼び出して検証する。
func TestBuildConversationSection_MiddleTruncation(t *testing.T) {
	const n = 50
	const middleUserIdx = 25
	const middleAssistantIdx = 24

	msgs := make([]model.Message, 0, n)
	for i := 0; i < n; i++ {
		role := model.RoleAssistant
		text := fmt.Sprintf("ASSISTANT-MARKER-%02d", i)
		if i == middleUserIdx {
			role = model.RoleUser
			text = "USER-MIDDLE-MARKER"
		}
		msgs = append(msgs, model.Message{Seq: i, Role: role, Text: text})
	}

	// 全文を収めるのに十分な予算を渡し、まず「切り詰めなし」を確認する。
	full := buildConversationSection(msgs, 1<<20)
	if strings.Contains(full, "省略") {
		t.Fatalf("予算が十分なのに省略が発生した: %q", full)
	}
	if !strings.Contains(full, "ASSISTANT-MARKER-00") || !strings.Contains(full, "ASSISTANT-MARKER-49") {
		t.Fatalf("全件出力のはずが先頭/末尾のメッセージが見当たらない: %q", full)
	}

	// 全文の 1/3 程度の予算にして、確実に中盤省略が起きるようにする。
	budget := len(full) / 3
	out := buildConversationSection(msgs, budget)

	if !strings.Contains(out, "ASSISTANT-MARKER-00") {
		t.Errorf("先頭のメッセージが残っていない: %q", out)
	}
	if !strings.Contains(out, "ASSISTANT-MARKER-49") {
		t.Errorf("末尾のメッセージが残っていない: %q", out)
	}
	if strings.Contains(out, fmt.Sprintf("ASSISTANT-MARKER-%02d", middleAssistantIdx)) {
		t.Errorf("中盤のアシスタント発話が省略されず残っている: %q", out)
	}
	if !strings.Contains(out, "USER-MIDDLE-MARKER") {
		t.Errorf("省略区間中のユーザー発話が抜粋されていない（ユーザー発話優先の要件）: %q", out)
	}

	m := regexp.MustCompile(`中略: (\d+) 件のメッセージを省略`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("省略件数の明示が見当たらない: %q", out)
	}
	count, err := strconv.Atoi(m[1])
	if err != nil || count <= 0 || count >= n {
		t.Errorf("省略件数が妥当な範囲ではない: %q (count=%d)", m[1], count)
	}
}

func TestBuildSessionPrompt_NoChildren_DelegationSectionOmitted(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{Session: s})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	if strings.Contains(out, "委譲") {
		t.Errorf("Children が空なのに委譲セクション（またはその痕跡）が出力に含まれている: %q", out)
	}
}

func TestBuildSessionPrompt_Children_SummaryAndParentComparison(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
	}

	children := []ChildSummary{
		{SessionID: "child-1", AgentName: "AGENT-ONE", DurationMinutes: 5, CostUSD: 0.08, Priced: true, MessageCount: 15, ToolErrorCount: 0},
		{SessionID: "child-2", AgentName: "AGENT-TWO", DurationMinutes: 4, CostUSD: 0.03, Priced: true, MessageCount: 12, ToolErrorCount: 2},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{
		Session:           s,
		Children:          children,
		SessionCostUSD:    0.50,
		SessionCostPriced: true,
	})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	if !strings.Contains(out, "委譲件数: 2 件") {
		t.Errorf("委譲件数が出力に含まれていない: %q", out)
	}
	// 合計コスト 0.08 + 0.03 = 0.11
	if !strings.Contains(out, "0.1100") {
		t.Errorf("委譲先の合計コストが出力に含まれていない: %q", out)
	}
	// 親自身のコスト
	if !strings.Contains(out, "0.5000") {
		t.Errorf("親セッション自身のコストが出力に含まれていない: %q", out)
	}
	for _, want := range []string{"AGENT-ONE", "AGENT-TWO"} {
		if !strings.Contains(out, want) {
			t.Errorf("子セッションの要約が出力に含まれていない: %q in %q", want, out)
		}
	}
}

func TestBuildSessionPrompt_Children_ExceedingLimit_ShowsRestCount(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
	}

	n := maxDelegationListed + 3
	children := make([]ChildSummary, 0, n)
	for i := 0; i < n; i++ {
		children = append(children, ChildSummary{
			SessionID:       fmt.Sprintf("child-%d", i),
			AgentName:       fmt.Sprintf("AGENT-%02d", i),
			DurationMinutes: 1,
			CostUSD:         float64(n - i), // 降順で並ぶように差をつける
			Priced:          true,
			MessageCount:    1,
		})
	}

	out, err := BuildSessionPrompt(SessionPromptInput{Session: s, Children: children})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	// 上位 maxDelegationListed 件（コストが大きい方、つまり AGENT-00..）は列挙され、
	// 残りは「他 N 件」にまとめられているはず。
	if !strings.Contains(out, "AGENT-00") {
		t.Errorf("最上位コストの子が列挙されていない: %q", out)
	}
	rest := n - maxDelegationListed
	if !strings.Contains(out, fmt.Sprintf("他 %d 件", rest)) {
		t.Errorf("「他 %d 件」の表記が出力に含まれていない: %q", rest, out)
	}
	// 一番コストの低い子（最後に列挙されるはずのもの）は列挙されていないはず。
	if strings.Contains(out, fmt.Sprintf("AGENT-%02d", n-1)) {
		t.Errorf("上限を超える件数の子が列挙されてしまっている: %q", out)
	}
}

func TestBuildSessionPrompt_Children_UnpricedNoAmountShown(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
	}

	children := []ChildSummary{
		{SessionID: "child-1", AgentName: "UNPRICED-AGENT", DurationMinutes: 3, CostUSD: 999, Priced: false, MessageCount: 5},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{Session: s, Children: children})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	if strings.Contains(out, "999") {
		t.Errorf("Priced=false の子について金額（CostUSD の値）が出力されてしまっている: %q", out)
	}
	if !strings.Contains(out, "単価未登録") {
		t.Errorf("Priced=false の子について「単価未登録」の明示がない: %q", out)
	}
	if !strings.Contains(out, "UNPRICED-AGENT") {
		t.Errorf("Priced=false でも子の要約自体（名前など）は出力されるべき: %q", out)
	}
}

// TestBuildSessionPrompt_Children_SurviveTruncation は、会話が長く中略が発生する
// ケースでも、委譲セクションが会話本文より優先して残ることを検証する。
func TestBuildSessionPrompt_Children_SurviveTruncation(t *testing.T) {
	s := baseSession()
	msgs := make([]model.Message, 0, 200)
	for i := 0; i < 200; i++ {
		msgs = append(msgs, model.Message{
			Seq:  i,
			Role: model.RoleAssistant,
			Text: fmt.Sprintf("filler text to pad out the conversation body number %d ", i) + strings.Repeat("x", 400),
		})
	}
	s.Messages = msgs

	children := []ChildSummary{
		{SessionID: "child-1", AgentName: "SURVIVING-AGENT", DurationMinutes: 2, CostUSD: 0.01, Priced: true, MessageCount: 3},
	}

	out, err := BuildSessionPrompt(SessionPromptInput{
		Session:  s,
		Children: children,
		MaxChars: 4000, // 会話全体を収めるには小さすぎる予算。中略が確実に発生する。
	})
	if err != nil {
		t.Fatalf("BuildSessionPrompt() error = %v", err)
	}

	if !strings.Contains(out, "中略") {
		t.Fatalf("この入力サイズでは中略が発生するはずだが、していない（テスト前提が崩れている）: %q", out)
	}
	if !strings.Contains(out, "SURVIVING-AGENT") {
		t.Errorf("会話が切り詰められても委譲セクションは残るはずが、失われている: %q", out)
	}
	if !strings.Contains(out, "委譲件数: 1 件") {
		t.Errorf("切り詰め後も委譲件数の要約が残るはず: %q", out)
	}
}

// TestBuildSessionPrompt_Deterministic は、同じ入力を 2 回渡したとき出力が
// 完全に一致することを検証する（マップの反復順などで非決定にならないこと）。
func TestBuildSessionPrompt_Deterministic(t *testing.T) {
	s := baseSession()
	s.Messages = []model.Message{
		{Seq: 1, Role: model.RoleUser, Text: "hello"},
		{Seq: 2, Role: model.RoleAssistant, Text: "work", Model: "claude-sonnet-5", Effort: "high"},
	}

	children := []ChildSummary{
		{SessionID: "child-1", AgentName: "AGENT-A", DurationMinutes: 3, CostUSD: 0.02, Priced: true, MessageCount: 4},
		{SessionID: "child-2", AgentName: "AGENT-B", DurationMinutes: 1, CostUSD: 0.02, Priced: true, MessageCount: 2},
		{SessionID: "child-3", AgentName: "AGENT-C", DurationMinutes: 1, CostUSD: 0, Priced: false, MessageCount: 1},
	}

	in := SessionPromptInput{
		Session:           s,
		Children:          children,
		SessionCostUSD:    0.30,
		SessionCostPriced: true,
	}

	out1, err1 := BuildSessionPrompt(in)
	if err1 != nil {
		t.Fatalf("BuildSessionPrompt() 1回目 error = %v", err1)
	}
	out2, err2 := BuildSessionPrompt(in)
	if err2 != nil {
		t.Fatalf("BuildSessionPrompt() 2回目 error = %v", err2)
	}

	if out1 != out2 {
		t.Errorf("同じ入力での2回の呼び出し結果が一致しない:\n--- 1回目 ---\n%s\n--- 2回目 ---\n%s", out1, out2)
	}
}
