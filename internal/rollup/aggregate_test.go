package rollup

import (
	"reflect"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/store"
)

// testPrices は claude-sonnet-5 のみ既知、claude-unknown-model は未知という単純な価格表を返す。
func testPrices(t *testing.T) *pricing.Table {
	t.Helper()
	tbl, err := pricing.Load(map[string]pricing.Rate{
		"claude-sonnet-5": {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
	})
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}
	return tbl
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", s, err)
	}
	return tm
}

func TestBuildDaily_EntrypointClassification(t *testing.T) {
	prices := testPrices(t)

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{Row: store.SessionRow{
				SessionID: "s-interactive", ProjectPath: "/p", ProjectLabel: "p",
				Entrypoint: "cli", StartedAt: mustTime(t, "2026-08-29T01:00:00Z"), EndedAt: mustTime(t, "2026-08-29T01:10:00Z"),
			}},
			{Row: store.SessionRow{
				SessionID: "s-automated", ProjectPath: "/p", ProjectLabel: "p",
				Entrypoint: "sdk-cli", StartedAt: mustTime(t, "2026-08-29T02:00:00Z"), EndedAt: mustTime(t, "2026-08-29T02:10:00Z"),
			}},
			{Row: store.SessionRow{
				SessionID: "s-sidechain", ProjectPath: "/p", ProjectLabel: "p",
				Entrypoint: "cli", IsSidechain: true,
				StartedAt: mustTime(t, "2026-08-29T03:00:00Z"), EndedAt: mustTime(t, "2026-08-29T03:10:00Z"),
			}},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	if d.Totals.Sessions != 3 {
		t.Errorf("Totals.Sessions = %d, want 3", d.Totals.Sessions)
	}
	if d.Totals.InteractiveSessions != 1 {
		t.Errorf("Totals.InteractiveSessions = %d, want 1", d.Totals.InteractiveSessions)
	}
	if d.Totals.AutomatedSessions != 1 {
		t.Errorf("Totals.AutomatedSessions = %d, want 1", d.Totals.AutomatedSessions)
	}
	if d.Totals.SidechainSessions != 1 {
		t.Errorf("Totals.SidechainSessions = %d, want 1", d.Totals.SidechainSessions)
	}
}

func TestBuildDaily_UnknownModelNotSilentlyZero(t *testing.T) {
	prices := testPrices(t)

	base := mustTime(t, "2026-08-29T01:00:00Z")
	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{
					SessionID: "s1", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					StartedAt: base, EndedAt: base.Add(10 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s1", Model: "claude-sonnet-5", InputTokens: 1_000_000, CostUSD: 3.0, CostKnown: true},
					{SessionID: "s1", Model: "claude-unknown-model", InputTokens: 1_000_000, CostUSD: 0, CostKnown: false},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	if d.Totals.CostUSD != 3.0 {
		t.Errorf("Totals.CostUSD = %v, want 3.0 (未知モデル分は合算されない)", d.Totals.CostUSD)
	}
	if d.Totals.UnpricedEvents != 1 {
		t.Errorf("Totals.UnpricedEvents = %d, want 1", d.Totals.UnpricedEvents)
	}

	if len(d.ByModel) != 2 {
		t.Fatalf("len(ByModel) = %d, want 2", len(d.ByModel))
	}
	// ByModel はコスト降順 -> claude-sonnet-5 (3.0) が先頭。
	if d.ByModel[0].Model != "claude-sonnet-5" || !d.ByModel[0].Priced {
		t.Errorf("ByModel[0] = %+v, want priced claude-sonnet-5", d.ByModel[0])
	}
	if d.ByModel[1].Model != "claude-unknown-model" || d.ByModel[1].Priced {
		t.Errorf("ByModel[1] = %+v, want unpriced claude-unknown-model", d.ByModel[1])
	}

	if len(d.Meta.UnknownModels) != 1 || d.Meta.UnknownModels[0] != "claude-unknown-model" {
		t.Errorf("Meta.UnknownModels = %v, want [claude-unknown-model]", d.Meta.UnknownModels)
	}

	// セッションカードも未知モデル混在で Priced=false になる。
	if len(d.Sessions) != 1 || d.Sessions[0].Priced {
		t.Errorf("Sessions[0].Priced = %v, want false", d.Sessions[0].Priced)
	}
	wantModels := []string{"claude-sonnet-5", "claude-unknown-model"} // アルファベット順
	if !reflect.DeepEqual(d.Sessions[0].Models, wantModels) {
		t.Errorf("Sessions[0].Models = %v, want %v", d.Sessions[0].Models, wantModels)
	}
}

func TestBuildDaily_FacetsAggregation(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{SessionID: "s1", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base},
				Eval: &model.Eval{
					Outcome:          "achieved",
					ArtifactValue:    "durable",
					InterventionCost: model.Assessment{Level: "low"},
					ModelFit:         model.VerdictReason{Verdict: "appropriate"},
					Ownership:        model.LevelReason{Level: "understood"},
					LearningValue:    "some",
					GoalCategory:     "feature",
					Confidence:       "high",
					Rework:           model.Rework{Occurred: true},
				},
			},
			{
				Row: store.SessionRow{SessionID: "s2", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base},
				// 未評価セッション。
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	if got := d.Facets.Outcome["achieved"]; got != 1 {
		t.Errorf("Facets.Outcome[achieved] = %d, want 1", got)
	}
	if d.Facets.ReworkOccurred != 1 {
		t.Errorf("Facets.ReworkOccurred = %d, want 1", d.Facets.ReworkOccurred)
	}
	if d.Meta.UnevaluatedSessions != 1 {
		t.Errorf("Meta.UnevaluatedSessions = %d, want 1", d.Meta.UnevaluatedSessions)
	}
}

func TestBuildDaily_EmptyFacetsAreNotNil(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{Row: store.SessionRow{SessionID: "s1", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base}},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	if d.Facets.Outcome == nil {
		t.Error("Facets.Outcome は nil であってはならない")
	}
	if d.Facets.ArtifactValue == nil {
		t.Error("Facets.ArtifactValue は nil であってはならない")
	}
	if d.Facets.InterventionCost == nil {
		t.Error("Facets.InterventionCost は nil であってはならない")
	}
	if d.Facets.ModelFit == nil {
		t.Error("Facets.ModelFit は nil であってはならない")
	}
	if d.Facets.Ownership == nil {
		t.Error("Facets.Ownership は nil であってはならない")
	}
	if d.Facets.LearningValue == nil {
		t.Error("Facets.LearningValue は nil であってはならない")
	}
	if d.Facets.GoalCategory == nil {
		t.Error("Facets.GoalCategory は nil であってはならない")
	}
	if d.Facets.Confidence == nil {
		t.Error("Facets.Confidence は nil であってはならない")
	}
	if len(d.Facets.Outcome) != 0 {
		t.Errorf("Facets.Outcome の要素数 = %d, want 0", len(d.Facets.Outcome))
	}
}

func TestBuildDaily_Determinism(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	build := func() *Daily {
		in := DailyInput{
			Date:   "2026-08-29",
			Prices: prices,
			Goals: func(p string) string {
				if p == "/proj-a" {
					return "goal-a"
				}
				return ""
			},
			Sessions: []SessionData{
				{
					Row: store.SessionRow{SessionID: "s3", ProjectPath: "/proj-b", ProjectLabel: "b", Entrypoint: "cli", StartedAt: base.Add(2 * time.Minute), EndedAt: base.Add(3 * time.Minute)},
					Usage: []store.UsageRow{
						{SessionID: "s3", Model: "claude-sonnet-5", InputTokens: 1_000_000, CostUSD: 3.0, CostKnown: true},
					},
					Eval: &model.Eval{Outcome: "partial"},
				},
				{
					Row: store.SessionRow{SessionID: "s1", ProjectPath: "/proj-a", ProjectLabel: "a", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(time.Minute)},
					Usage: []store.UsageRow{
						{SessionID: "s1", Model: "claude-sonnet-5", InputTokens: 1_000_000, CostUSD: 3.0, CostKnown: true},
					},
					Eval: &model.Eval{Outcome: "achieved"},
				},
				{
					Row: store.SessionRow{SessionID: "s2", ProjectPath: "/proj-a", ProjectLabel: "a", Entrypoint: "sdk-cli", StartedAt: base.Add(time.Minute), EndedAt: base.Add(2 * time.Minute)},
					Usage: []store.UsageRow{
						{SessionID: "s2", Model: "claude-haiku-5", InputTokens: 1_000_000, CostUSD: 0, CostKnown: false},
					},
				},
			},
		}
		d, err := BuildDaily(in)
		if err != nil {
			t.Fatalf("BuildDaily() error = %v", err)
		}
		d.GeneratedAt = time.Time{} // 実行時刻は決定性の対象外なので比較から除く
		return d
	}

	d1 := build()
	d2 := build()

	if !reflect.DeepEqual(d1, d2) {
		t.Errorf("2 回の BuildDaily の結果が一致しない:\n1回目: %+v\n2回目: %+v", d1, d2)
	}

	// ByProject はコスト同額 (3.0 vs 3.0 で /proj-a は s1+s2=3.0, /proj-b は s3=3.0) の場合、
	// ProjectPath 昇順で tie-break される。
	if len(d1.ByProject) != 2 {
		t.Fatalf("len(ByProject) = %d, want 2", len(d1.ByProject))
	}
	if d1.ByProject[0].ProjectPath != "/proj-a" || d1.ByProject[1].ProjectPath != "/proj-b" {
		t.Errorf("ByProject の順序 = [%s, %s], want [/proj-a, /proj-b]（同額コストは path 昇順)",
			d1.ByProject[0].ProjectPath, d1.ByProject[1].ProjectPath)
	}
	if d1.ByProject[0].Goal != "goal-a" {
		t.Errorf("ByProject[0].Goal = %q, want %q", d1.ByProject[0].Goal, "goal-a")
	}

	// Sessions は開始時刻昇順。
	wantOrder := []string{"s1", "s2", "s3"}
	var gotOrder []string
	for _, sc := range d1.Sessions {
		gotOrder = append(gotOrder, sc.SessionID)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("Sessions の順序 = %v, want %v", gotOrder, wantOrder)
	}
}

func TestBuildDaily_InvalidDate(t *testing.T) {
	prices := testPrices(t)
	_, err := BuildDaily(DailyInput{Date: "not-a-date", Prices: prices})
	if err == nil {
		t.Error("BuildDaily() with invalid Date: error = nil, want error")
	}
}

func TestBuildSeries_MissingDaysNotFabricated(t *testing.T) {
	d1 := &Daily{Date: "2026-08-27", Totals: Totals{Sessions: 2}, Facets: Facets{Outcome: map[string]int{"achieved": 1, "partial": 1}}}
	d3 := &Daily{Date: "2026-08-29", Totals: Totals{Sessions: 1}, Facets: Facets{Outcome: map[string]int{}}}

	s := BuildSeries("2026-08-27", "2026-08-29", []*Daily{d3, d1}, nil)

	if len(s.Points) != 2 {
		t.Fatalf("len(Points) = %d, want 2 (2026-08-28 は欠損として捏造されない)", len(s.Points))
	}
	if s.Points[0].Date != "2026-08-27" || s.Points[1].Date != "2026-08-29" {
		t.Errorf("Points の日付 = [%s, %s], want [2026-08-27, 2026-08-29]", s.Points[0].Date, s.Points[1].Date)
	}
}

func TestBuildSeries_AchievedRatio(t *testing.T) {
	withEval := &Daily{
		Date:   "2026-08-29",
		Totals: Totals{Sessions: 2},
		Facets: Facets{Outcome: map[string]int{"achieved": 1, "partial": 1}},
	}
	noEval := &Daily{
		Date:   "2026-08-28",
		Totals: Totals{Sessions: 1},
		Facets: Facets{Outcome: map[string]int{}},
	}

	s := BuildSeries("2026-08-28", "2026-08-29", []*Daily{withEval, noEval}, nil)

	if len(s.Points) != 2 {
		t.Fatalf("len(Points) = %d, want 2", len(s.Points))
	}
	if s.Points[0].AchievedRatio != -1 {
		t.Errorf("noEval day の AchievedRatio = %v, want -1", s.Points[0].AchievedRatio)
	}
	if s.Points[1].AchievedRatio != 0.5 {
		t.Errorf("withEval day の AchievedRatio = %v, want 0.5", s.Points[1].AchievedRatio)
	}
}

// ---- サブエージェント（sidechain）の畳み込み ----

// ワークツリーで動いたサブエージェントは、親に畳まずそれ自体を 1 本の作業として扱う。
// 個別に評価する対象なので、カードにならないと評価結果がどこにも出てこない。
func TestBuildDaily_WorktreeSidechainIsItsOwnCard(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{
					SessionID: "s-parent", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					StartedAt: base, EndedAt: base.Add(20 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s-parent", Model: "claude-sonnet-5", InputTokens: 1, CostUSD: 1.0, CostKnown: true},
				},
			},
			{
				Row: store.SessionRow{
					SessionID: "s-wt", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					Worktree: "feat-x", IsSidechain: true, ParentSessionID: "s-parent",
					StartedAt: base.Add(time.Minute), EndedAt: base.Add(10 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s-wt", Model: "claude-sonnet-5", InputTokens: 1, CostUSD: 0.5, CostKnown: true},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	cards := map[string]*SessionCard{}
	for i := range d.Sessions {
		cards[d.Sessions[i].SessionID] = &d.Sessions[i]
	}
	if len(cards) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2（ワークツリーの子も 1 枚のカードになる）", len(d.Sessions))
	}

	wt := cards["s-wt"]
	if wt == nil {
		t.Fatal("ワークツリーのセッションがカードになっていません")
	}
	if wt.Worktree != "feat-x" {
		t.Errorf("wt.Worktree = %q, want feat-x", wt.Worktree)
	}
	if !wt.IsSidechain {
		t.Error("wt.IsSidechain = false, want true（サブエージェントであること自体は変わらない）")
	}
	if wt.TotalCostUSD != 0.5 {
		t.Errorf("wt.TotalCostUSD = %v, want 0.5", wt.TotalCostUSD)
	}

	parent := cards["s-parent"]
	if parent.ChildCostUSD != 0 || parent.ChildSessions != 0 {
		t.Errorf("親に畳み込まれています: ChildSessions=%d ChildCostUSD=%v（二重計上になる）",
			parent.ChildSessions, parent.ChildCostUSD)
	}
	if parent.TotalCostUSD != 1.0 {
		t.Errorf("parent.TotalCostUSD = %v, want 1.0", parent.TotalCostUSD)
	}

	// 日の合計は sidechain を含む全セッションの合計なので変わらない。
	if d.Totals.CostUSD != 1.5 {
		t.Errorf("Totals.CostUSD = %v, want 1.5", d.Totals.CostUSD)
	}
}

func TestBuildDaily_SidechainFoldedIntoParent(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{
					SessionID: "s-parent", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					StartedAt: base, EndedAt: base.Add(20 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s-parent", Model: "claude-sonnet-5", InputTokens: 1, CostUSD: 1.0, CostKnown: true},
				},
			},
			{
				Row: store.SessionRow{
					SessionID: "s-child", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					IsSidechain: true, ParentSessionID: "s-parent",
					StartedAt: base.Add(time.Minute), EndedAt: base.Add(2 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s-child", Model: "claude-sonnet-5", InputTokens: 1, CostUSD: 0.5, CostKnown: true},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	// sidechain は独立したカードとして Daily.Sessions に現れない。
	if len(d.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1（sidechain は親に畳まれる）", len(d.Sessions))
	}
	parent := d.Sessions[0]
	if parent.SessionID != "s-parent" {
		t.Fatalf("Sessions[0].SessionID = %q, want s-parent", parent.SessionID)
	}
	if parent.CostUSD != 1.0 {
		t.Errorf("parent.CostUSD = %v, want 1.0（自分自身のコストのみ）", parent.CostUSD)
	}
	if parent.ChildSessions != 1 {
		t.Errorf("parent.ChildSessions = %d, want 1", parent.ChildSessions)
	}
	if parent.ChildCostUSD != 0.5 {
		t.Errorf("parent.ChildCostUSD = %v, want 0.5", parent.ChildCostUSD)
	}
	if parent.TotalCostUSD != 1.5 {
		t.Errorf("parent.TotalCostUSD = %v, want 1.5", parent.TotalCostUSD)
	}

	// Totals は sidechain 込みの全セッションを数える（畳むのは並べ方であって集計ではない）。
	if d.Totals.CostUSD != 1.5 {
		t.Errorf("Totals.CostUSD = %v, want 1.5", d.Totals.CostUSD)
	}
	if d.Totals.Sessions != 2 {
		t.Errorf("Totals.Sessions = %d, want 2", d.Totals.Sessions)
	}
	if d.Totals.SidechainSessions != 1 {
		t.Errorf("Totals.SidechainSessions = %d, want 1", d.Totals.SidechainSessions)
	}

	// 親も子も未評価だが、sidechain は方針として評価対象外なので数えない。
	if d.Meta.UnevaluatedSessions != 1 {
		t.Errorf("Meta.UnevaluatedSessions = %d, want 1（sidechain は含めない）", d.Meta.UnevaluatedSessions)
	}
}

func TestBuildDaily_OrphanSidechainCostNotLost(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{
					SessionID: "s-orphan", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
					IsSidechain: true, ParentSessionID: "s-missing-parent",
					StartedAt: base, EndedAt: base.Add(3 * time.Minute),
				},
				Usage: []store.UsageRow{
					{SessionID: "s-orphan", Model: "claude-sonnet-5", InputTokens: 1, CostUSD: 0.3, CostKnown: true},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	// 親が見つからなくても Totals からコストが失われない。
	if d.Totals.CostUSD != 0.3 {
		t.Errorf("Totals.CostUSD = %v, want 0.3（孤児 sidechain のコストが失われている）", d.Totals.CostUSD)
	}
	// sidechain はカードとして表示されない。
	if len(d.Sessions) != 0 {
		t.Errorf("len(Sessions) = %d, want 0（孤児 sidechain も独立カードにしない）", len(d.Sessions))
	}

	if len(d.ByProject) != 1 {
		t.Fatalf("len(ByProject) = %d, want 1", len(d.ByProject))
	}
	proj := d.ByProject[0]
	if proj.RolledUp.Sessions != 1 {
		t.Errorf("RolledUp.Sessions = %d, want 1（孤児として RolledUp に計上される）", proj.RolledUp.Sessions)
	}
	if proj.RolledUp.CostUSD != 0.3 {
		t.Errorf("RolledUp.CostUSD = %v, want 0.3", proj.RolledUp.CostUSD)
	}
	if proj.RolledUp.Reason == "" {
		t.Error("RolledUp.Reason が空。孤児 sidechain である旨の説明が欲しい")
	}
}

// ---- プロジェクト単位の集計（Facets / AchievedRatio / CostShare） ----

func TestBuildDaily_ProjectFacetsAndAchievedRatioAndCostShare(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{SessionID: "p1-s1", ProjectPath: "/p1", ProjectLabel: "p1", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "p1-s1", Model: "claude-sonnet-5", CostUSD: 1.0, CostKnown: true},
				},
				Eval: &model.Eval{Outcome: "achieved"},
			},
			{
				Row: store.SessionRow{SessionID: "p1-s2", ProjectPath: "/p1", ProjectLabel: "p1", Entrypoint: "cli", StartedAt: base.Add(time.Minute), EndedAt: base.Add(2 * time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "p1-s2", Model: "claude-sonnet-5", CostUSD: 1.0, CostKnown: true},
				},
				Eval: &model.Eval{Outcome: "partial"},
			},
			{
				Row: store.SessionRow{SessionID: "p2-s1", ProjectPath: "/p2", ProjectLabel: "p2", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "p2-s1", Model: "claude-sonnet-5", CostUSD: 1.0, CostKnown: true},
				},
				// 未評価。
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}

	if d.Totals.CostUSD != 3.0 {
		t.Fatalf("Totals.CostUSD = %v, want 3.0", d.Totals.CostUSD)
	}

	var p1, p2 *ProjectStat
	for i := range d.ByProject {
		switch d.ByProject[i].ProjectPath {
		case "/p1":
			p1 = &d.ByProject[i]
		case "/p2":
			p2 = &d.ByProject[i]
		}
	}
	if p1 == nil || p2 == nil {
		t.Fatalf("ByProject に /p1 /p2 が両方揃っていない: %+v", d.ByProject)
	}

	if p1.EvaluatedSessions != 2 {
		t.Errorf("p1.EvaluatedSessions = %d, want 2", p1.EvaluatedSessions)
	}
	if p1.Facets.Outcome["achieved"] != 1 || p1.Facets.Outcome["partial"] != 1 {
		t.Errorf("p1.Facets.Outcome = %+v, want achieved=1 partial=1", p1.Facets.Outcome)
	}
	if p1.AchievedRatio != 0.5 {
		t.Errorf("p1.AchievedRatio = %v, want 0.5", p1.AchievedRatio)
	}
	wantShare := 2.0 / 3.0
	if p1.CostShare != wantShare {
		t.Errorf("p1.CostShare = %v, want %v", p1.CostShare, wantShare)
	}

	if p2.EvaluatedSessions != 0 {
		t.Errorf("p2.EvaluatedSessions = %d, want 0", p2.EvaluatedSessions)
	}
	if p2.AchievedRatio != -1 {
		t.Errorf("p2.AchievedRatio = %v, want -1（評価 0 件は 0%% と区別する）", p2.AchievedRatio)
	}
	if p2.Facets.Outcome == nil {
		t.Error("p2.Facets.Outcome は nil であってはならない")
	}
	if len(p2.Facets.Outcome) != 0 {
		t.Errorf("p2.Facets.Outcome の要素数 = %d, want 0", len(p2.Facets.Outcome))
	}
}

// ---- 丸め（Highlights / RolledUp） ----

func TestBuildDaily_RollupThreshold(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				// 小さい（コスト割合・時間ともに閾値未満）→ RolledUp。
				Row: store.SessionRow{SessionID: "small", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "small", Model: "claude-sonnet-5", CostUSD: 0.01, CostKnown: true},
				},
			},
			{
				// コストが大きい → Highlights。
				Row: store.SessionRow{SessionID: "big", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base.Add(2 * time.Minute), EndedAt: base.Add(3 * time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "big", Model: "claude-sonnet-5", CostUSD: 10.0, CostKnown: true},
				},
			},
			{
				// 小さいが achieved + durable → 丸めずに Highlights に残す。
				Row: store.SessionRow{SessionID: "small-durable", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base.Add(4 * time.Minute), EndedAt: base.Add(5 * time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "small-durable", Model: "claude-sonnet-5", CostUSD: 0.01, CostKnown: true},
				},
				Eval: &model.Eval{Outcome: "achieved", ArtifactValue: "durable"},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}
	if len(d.ByProject) != 1 {
		t.Fatalf("len(ByProject) = %d, want 1", len(d.ByProject))
	}
	proj := d.ByProject[0]

	if proj.RolledUp.Sessions != 1 {
		t.Fatalf("RolledUp.Sessions = %d, want 1: %+v", proj.RolledUp.Sessions, proj.RolledUp)
	}
	if proj.RolledUp.CostUSD != 0.01 {
		t.Errorf("RolledUp.CostUSD = %v, want 0.01", proj.RolledUp.CostUSD)
	}

	if len(proj.Highlights) != 2 {
		t.Fatalf("len(Highlights) = %d, want 2: %+v", len(proj.Highlights), proj.Highlights)
	}
	// TotalCostUSD 降順。
	if proj.Highlights[0].SessionID != "big" {
		t.Errorf("Highlights[0].SessionID = %q, want big", proj.Highlights[0].SessionID)
	}
	if proj.Highlights[1].SessionID != "small-durable" {
		t.Errorf("Highlights[1].SessionID = %q, want small-durable（achieved+durable は丸めない）", proj.Highlights[1].SessionID)
	}
}

func TestBuildDaily_RollupThreshold_NoneRolledUpIsZeroValue(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		Sessions: []SessionData{
			{
				Row: store.SessionRow{SessionID: "s1", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(20 * time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "s1", Model: "claude-sonnet-5", CostUSD: 5.0, CostKnown: true},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}
	proj := d.ByProject[0]
	if (proj.RolledUp != RolledUpGroup{}) {
		t.Errorf("RolledUp = %+v, want ゼロ値（丸めたセッションが無い）", proj.RolledUp)
	}
	if len(proj.Highlights) != 1 {
		t.Fatalf("len(Highlights) = %d, want 1", len(proj.Highlights))
	}
}

func TestBuildDaily_RollupThreshold_Override(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	in := DailyInput{
		Date:   "2026-08-29",
		Prices: prices,
		// 既定値よりずっと緩いしきい値にして、通常なら Highlights になるはずのセッションを
		// 明示的な RollupThreshold で丸めさせる。CostShare は「1 セッションのみの日は
		// costShare が最大でも 1.0 になる」ことを踏まえ、1.0 未満の判定に確実に入るよう
		// 1.0 より大きい値にする。
		RollupThreshold: RollupThreshold{CostShare: 2.0, DurationMinutes: 1000},
		Sessions: []SessionData{
			{
				Row: store.SessionRow{SessionID: "s1", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(20 * time.Minute)},
				Usage: []store.UsageRow{
					{SessionID: "s1", Model: "claude-sonnet-5", CostUSD: 5.0, CostKnown: true},
				},
			},
		},
	}

	d, err := BuildDaily(in)
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}
	proj := d.ByProject[0]
	if proj.RolledUp.Sessions != 1 {
		t.Errorf("RolledUp.Sessions = %d, want 1（DailyInput.RollupThreshold で緩めた閾値により丸められるはず）", proj.RolledUp.Sessions)
	}
	if len(proj.Highlights) != 0 {
		t.Errorf("len(Highlights) = %d, want 0", len(proj.Highlights))
	}
}

func TestBuildDaily_Determinism_WithSidechainAndRollup(t *testing.T) {
	prices := testPrices(t)
	base := mustTime(t, "2026-08-29T01:00:00Z")

	build := func() *Daily {
		in := DailyInput{
			Date:   "2026-08-29",
			Prices: prices,
			Sessions: []SessionData{
				{
					Row: store.SessionRow{SessionID: "parent", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base, EndedAt: base.Add(20 * time.Minute)},
					Usage: []store.UsageRow{
						{SessionID: "parent", Model: "claude-sonnet-5", CostUSD: 5.0, CostKnown: true},
					},
					Eval: &model.Eval{Outcome: "achieved", ArtifactValue: "durable"},
				},
				{
					Row: store.SessionRow{
						SessionID: "child", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
						IsSidechain: true, ParentSessionID: "parent",
						StartedAt: base.Add(time.Minute), EndedAt: base.Add(2 * time.Minute),
					},
					Usage: []store.UsageRow{
						{SessionID: "child", Model: "claude-sonnet-5", CostUSD: 0.2, CostKnown: true},
					},
				},
				{
					Row: store.SessionRow{
						SessionID: "orphan", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
						IsSidechain: true, ParentSessionID: "no-such-session",
						StartedAt: base.Add(3 * time.Minute), EndedAt: base.Add(4 * time.Minute),
					},
					Usage: []store.UsageRow{
						{SessionID: "orphan", Model: "claude-sonnet-5", CostUSD: 0.1, CostKnown: true},
					},
				},
				{
					Row: store.SessionRow{SessionID: "small", ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli", StartedAt: base.Add(5 * time.Minute), EndedAt: base.Add(6 * time.Minute)},
					Usage: []store.UsageRow{
						{SessionID: "small", Model: "claude-sonnet-5", CostUSD: 0.01, CostKnown: true},
					},
				},
			},
		}
		d, err := BuildDaily(in)
		if err != nil {
			t.Fatalf("BuildDaily() error = %v", err)
		}
		d.GeneratedAt = time.Time{}
		return d
	}

	d1 := build()
	d2 := build()

	if !reflect.DeepEqual(d1, d2) {
		t.Errorf("2 回の BuildDaily の結果が一致しない:\n1回目: %+v\n2回目: %+v", d1, d2)
	}
}
