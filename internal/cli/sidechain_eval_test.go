package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/judge/prompts"
)

// サブエージェントは、ワークツリーで動いたものもそうでないものも、すべて個別評価の
// 対象にする。実データでは全セッションのおよそ 7 割がサブエージェント実行であり、
// ここを親の「委譲 N 件」に畳んだままにすると、その日に実際に動いた作業の大半が
// 評価されないまま消える。
func TestPrepareEvalTargets_AllSidechainsAreEvaluated(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "parent", ProjectPath: "/proj", Title: "親", FirstPrompt: "頼む",
		StartedAt: base,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "wt-child", ProjectPath: "/proj", Worktree: "feat-x",
		IsSidechain: true, ParentSessionID: "parent", Title: "ワークツリーでの並行作業",
		FirstPrompt: "実装する", StartedAt: base.Add(time.Minute),
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "plain-child", ProjectPath: "/proj",
		IsSidechain: true, ParentSessionID: "parent", Title: "ふつうの委譲",
		FirstPrompt: "調べる", StartedAt: base.Add(2 * time.Minute),
	})

	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usage, err := db.UsageInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	plan, err := prepareEvalTargets(db, rows, usage, false, prompts.PromptVersion)
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}

	got := map[string]bool{}
	for _, r := range plan.Targets {
		got[r.SessionID] = true
	}
	for _, id := range []string{"parent", "wt-child", "plain-child"} {
		if !got[id] {
			t.Errorf("%s が評価対象に入っていません: targets=%v", id, got)
		}
	}
	if len(plan.Targets) != 3 {
		t.Errorf("len(Targets) = %d, want 3", len(plan.Targets))
	}
}

// 子セッションの評価結果（達成度・手戻り）を親の委譲要約に載せるには、親より先に子を
// 評価しておく必要がある。evaluateSessionsInPhases がその順序を保証していることを、
// 親に渡ったプロンプトの中身で確認する。
func TestEvaluateSessionsInPhases_ChildEvalReachesParentPrompt(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "parent", ProjectPath: "/proj", Title: "親", FirstPrompt: "頼む",
		StartedAt: base, CostUSD: 0.10, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "child", ProjectPath: "/proj",
		IsSidechain: true, ParentSessionID: "parent", Title: "調査のサブエージェント",
		FirstPrompt: "調べる", StartedAt: base.Add(time.Minute), CostUSD: 0.02, CostKnown: true,
	})

	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usage, err := db.UsageInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	plan, err := prepareEvalTargets(db, rows, usage, false, "v1")
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}

	fj := &fakeSessionJudge{outcome: "partial"}
	result, err := evaluateSessionsInPhases(context.Background(), evalDeps{
		DB: db, Judge: fj, Model: "claude-sonnet-5", JudgeName: fj.Name(),
		PromptVersion: "v1", Concurrency: 4,
	}, plan.Targets, rows, plan, nil)
	if err != nil {
		t.Fatalf("evaluateSessionsInPhases() error = %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("Succeeded = %v, want 2 件", result.Succeeded)
	}

	// 親と子の台本を取り違えないよう、親にしか出てこない発話で選ぶ
	// （"親" は子の台本にも「親セッションID」として出てくる）。
	prompt := fj.promptFor("最初のユーザー発話: 頼む")
	if prompt == "" {
		t.Fatal("親セッションの評価プロンプトが記録されていません")
	}
	// 子の達成度が委譲セクションに載っていること（未評価のままなら「未評価」と出る）。
	if !strings.Contains(prompt, "達成度内訳") || !strings.Contains(prompt, "partial") {
		t.Errorf("親のプロンプトに子の評価結果が載っていません:\n%s", prompt)
	}
	if strings.Contains(prompt, "委譲先の評価: 未評価") {
		t.Error("親のプロンプトで子が未評価扱いになっています（子を先に評価できていない）")
	}
}
