package cli

import (
	"context"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/store"
)

func TestActiveWindow_IsActive(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	row := func(ended time.Time) store.SessionRow { return store.SessionRow{EndedAt: ended} }
	w := activeWindow{Now: now, Grace: 30 * time.Minute}

	if !w.isActive(row(now.Add(-29 * time.Minute))) {
		t.Error("29 分前に終わったセッションは作業中のはず")
	}
	if w.isActive(row(now.Add(-30 * time.Minute))) {
		t.Error("ちょうど 30 分前は作業中ではないはず（境界は含まない）")
	}
	if w.isActive(row(time.Time{})) {
		t.Error("EndedAt 不明は作業中とみなさないはず")
	}
	if (activeWindow{Now: now}).isActive(row(now)) {
		t.Error("猶予 0 は判定を無効にするはず")
	}
}

func TestNewActiveWindow_IncludeActiveDisables(t *testing.T) {
	cfg := config.Default()
	if got := newActiveWindow(cfg, true).Grace; got != 0 {
		t.Errorf("--include-active の猶予 = %s, want 0", got)
	}
	if got := newActiveWindow(cfg, false).Grace; got != 30*time.Minute {
		t.Errorf("既定の猶予 = %s, want 30m", got)
	}
}

// 作業中のセッションは評価対象にも件数にも入らず、見送った件数として別に数える。
func TestPrepareEvalTargets_SkipsActiveSessions(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "done", ProjectPath: "/p", Title: "終わった", FirstPrompt: "a",
		StartedAt: base, EndedAt: base.Add(10 * time.Minute), CostUSD: 0.1, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "active", ProjectPath: "/p", Title: "作業中", FirstPrompt: "b",
		StartedAt: now.Add(-40 * time.Minute), EndedAt: now.Add(-5 * time.Minute), CostUSD: 0.1, CostKnown: true,
	})

	from, to := base.Add(-time.Hour), now.Add(time.Hour)
	rows, err := db.SessionsInRange(from, to)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usage, err := db.UsageInRange(from, to)
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	w := activeWindow{Now: now, Grace: 30 * time.Minute}
	plan, err := prepareEvalTargets(db, rows, usage, false, "v1", w)
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].SessionID != "done" || plan.ActiveSkipped != 1 {
		t.Errorf("targets=%+v activeSkipped=%d, want [done] / 1", plan.Targets, plan.ActiveSkipped)
	}

	// --include-active 相当（猶予なし）なら両方が対象。
	plan, err = prepareEvalTargets(db, rows, usage, false, "v1", activeWindow{})
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}
	if len(plan.Targets) != 2 || plan.ActiveSkipped != 0 {
		t.Errorf("include-active: targets=%d activeSkipped=%d, want 2 / 0", len(plan.Targets), plan.ActiveSkipped)
	}
}

// daily は作業中のセッションを評価せず、--include-active なら評価する（時刻は差し替える）。
func TestRunDaily_SkipsActiveSession(t *testing.T) {
	db := newTempDB(t)
	cfg := config.Default()
	cfg.Output.Dir = t.TempDir()
	isolateCodexSource(t, cfg)

	date := "2026-08-20"
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	saveTestSession(t, db, testSessionSpec{
		SessionID: "done", ProjectPath: "/p", ProjectLabel: "p", Title: "終わった", FirstPrompt: "a",
		StartedAt: base, EndedAt: base.Add(10 * time.Minute), CostUSD: 0.1, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "active", ProjectPath: "/p", ProjectLabel: "p", Title: "作業中", FirstPrompt: "b",
		StartedAt: base.Add(3 * time.Hour), EndedAt: base.Add(3*time.Hour + 10*time.Minute), CostUSD: 0.2, CostKnown: true,
	})

	old := nowFunc
	nowFunc = func() time.Time { return base.Add(3*time.Hour + 15*time.Minute) }
	t.Cleanup(func() { nowFunc = old })

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatal(err)
	}

	fj := &fakeDailyJudge{}
	result, err := runDaily(context.Background(), newDailyTestCmd(t), cfg, db, prices, fj, rows, 0, date, false, true, false)
	if err != nil {
		t.Fatalf("runDaily() error = %v", err)
	}
	if fj.sessionCalls != 1 || result.JudgeEvaluated != 1 || result.ActiveSkipped != 1 {
		t.Errorf("sessionCalls=%d evaluated=%d activeSkipped=%d, want 1 / 1 / 1", fj.sessionCalls, result.JudgeEvaluated, result.ActiveSkipped)
	}
	if result.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2（作業中も集計に含める）", result.TotalSessions)
	}

	// --include-active なら、残っていた作業中のセッションも評価する。
	fj2 := &fakeDailyJudge{}
	result, err = runDaily(context.Background(), newDailyTestCmd(t), cfg, db, prices, fj2, rows, 0, date, false, true, true)
	if err != nil {
		t.Fatalf("runDaily(includeActive) error = %v", err)
	}
	if fj2.sessionCalls != 1 || result.ActiveSkipped != 0 {
		t.Errorf("include-active: sessionCalls=%d activeSkipped=%d, want 1 / 0", fj2.sessionCalls, result.ActiveSkipped)
	}
}
