package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/spf13/cobra"
)

// fakeSessionJudge は judge.Judge のテスト用フェイク実装。実際の claude サブプロセスは呼ばない。
// failMarker が req.Prompt に含まれる呼び出しだけ失敗させる（部分失敗のテスト用）。
type fakeSessionJudge struct {
	mu         sync.Mutex
	calls      int
	failMarker string
	outcome    string // 空なら "achieved"
}

func (f *fakeSessionJudge) Name() string     { return "fake-judge" }
func (f *fakeSessionJudge) Available() error { return nil }

func (f *fakeSessionJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.failMarker != "" && strings.Contains(req.Prompt, f.failMarker) {
		return nil, errors.New("boom: 意図的な失敗")
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = "achieved"
	}
	return validEvalJSON(outcome), nil
}

func (f *fakeSessionJudge) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestPrepareEvalTargets_ExcludesSidechainAndBuildsChildSummary(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "parent-1", ProjectPath: "/proj-a", Title: "親", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(10 * time.Minute), CostUSD: 0.10, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "child-1", IsSidechain: true, ParentSessionID: "parent-1", ProjectPath: "/proj-a",
		Title: "サブエージェントA", FirstPrompt: "sub task",
		StartedAt: base.Add(time.Minute), EndedAt: base.Add(3 * time.Minute), CostUSD: 0.02, CostKnown: true,
	})

	wideFrom := base.Add(-24 * time.Hour)
	wideTo := base.Add(24 * time.Hour)
	rows, err := db.SessionsInRange(wideFrom, wideTo)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usageRows, err := db.UsageInRange(wideFrom, wideTo)
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	targets, sidechainExcluded, cacheSkipped, children, costs, err := prepareEvalTargets(db, rows, usageRows, false, "v1")
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}

	if len(targets) != 1 || targets[0].SessionID != "parent-1" {
		t.Fatalf("targets = %+v, want [parent-1]（サブエージェントは対象から除外されるはず）", targets)
	}
	if sidechainExcluded != 1 {
		t.Errorf("sidechainExcluded = %d, want 1", sidechainExcluded)
	}
	if cacheSkipped != 0 {
		t.Errorf("cacheSkipped = %d, want 0", cacheSkipped)
	}

	kids, ok := children["parent-1"]
	if !ok || len(kids) != 1 {
		t.Fatalf("children[parent-1] = %+v, want 1 件", kids)
	}
	if kids[0].SessionID != "child-1" || kids[0].AgentName != "サブエージェントA" {
		t.Errorf("child summary = %+v, want session=child-1 name=サブエージェントA", kids[0])
	}
	if !kids[0].Priced || kids[0].CostUSD != 0.02 {
		t.Errorf("child cost = (priced=%v, usd=%v), want (true, 0.02)", kids[0].Priced, kids[0].CostUSD)
	}

	if agg, ok := costs["parent-1"]; !ok || agg.CostUSD != 0.10 || !agg.AllKnown {
		t.Errorf("costs[parent-1] = %+v, want CostUSD=0.10 AllKnown=true", agg)
	}
}

func TestPrepareEvalTargets_CacheSkipAndForce(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.05, CostKnown: true,
	})

	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	rows, err := db.SessionsInRange(from, to)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usageRows, err := db.UsageInRange(from, to)
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	targets, _, cacheSkipped, _, _, err := prepareEvalTargets(db, rows, usageRows, false, "v1")
	if err != nil {
		t.Fatalf("prepareEvalTargets() 1回目 error = %v", err)
	}
	if len(targets) != 1 || cacheSkipped != 0 {
		t.Fatalf("1回目: targets=%d cacheSkipped=%d, want 1, 0", len(targets), cacheSkipped)
	}

	if err := db.SaveEval("sess-1", "fake-judge", "claude-sonnet-5", "v1", targets[0].ContentHash, validEvalJSON("achieved")); err != nil {
		t.Fatalf("SaveEval() error = %v", err)
	}

	targets2, _, cacheSkipped2, _, _, err := prepareEvalTargets(db, rows, usageRows, false, "v1")
	if err != nil {
		t.Fatalf("prepareEvalTargets() 2回目 error = %v", err)
	}
	if len(targets2) != 0 || cacheSkipped2 != 1 {
		t.Fatalf("2回目（force=false）: targets=%d cacheSkipped=%d, want 0, 1（キャッシュが効くはず）", len(targets2), cacheSkipped2)
	}

	targets3, _, cacheSkipped3, _, _, err := prepareEvalTargets(db, rows, usageRows, true, "v1")
	if err != nil {
		t.Fatalf("prepareEvalTargets() --force error = %v", err)
	}
	if len(targets3) != 1 || cacheSkipped3 != 0 {
		t.Fatalf("--force: targets=%d cacheSkipped=%d, want 1, 0（force はキャッシュを無視するはず）", len(targets3), cacheSkipped3)
	}
}

func TestEvaluateSessions_PartialFailureContinuesAndSaves(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-ok", ProjectPath: "/proj-a", FirstPrompt: "please do the ok-task",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.01, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-fail", ProjectPath: "/proj-a", FirstPrompt: "please do the fail-task",
		StartedAt: base.Add(10 * time.Minute), EndedAt: base.Add(15 * time.Minute), CostUSD: 0.01, CostKnown: true,
	})

	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	rows, err := db.SessionsInRange(from, to)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	fj := &fakeSessionJudge{failMarker: "fail-task"}
	cfg := config.Default()

	result, err := evaluateSessions(context.Background(), evalDeps{
		DB: db, Judge: fj, Cfg: cfg, Model: "claude-sonnet-5", JudgeName: "fake-judge",
		PromptVersion: "v1", Concurrency: 2,
	}, rows, nil, map[string]*sessionCostAgg{}, io.Discard)
	if err != nil {
		t.Fatalf("evaluateSessions() error = %v, want nil（部分失敗では全体を止めない）", err)
	}

	if len(result.Succeeded) != 1 || result.Succeeded[0] != "sess-ok" {
		t.Errorf("Succeeded = %v, want [sess-ok]", result.Succeeded)
	}
	if len(result.Failed) != 1 || result.Failed[0].SessionID != "sess-fail" {
		t.Errorf("Failed = %+v, want 1 件 session_id=sess-fail", result.Failed)
	}
	if fj.callCount() != 2 {
		t.Errorf("callCount = %d, want 2", fj.callCount())
	}

	// 成功した分は DB に保存されていること。
	if _, ok, err := db.EvalFor("sess-ok", "v1", "hash-sess-ok"); err != nil || !ok {
		t.Errorf("EvalFor(sess-ok) = ok=%v err=%v, want ok=true", ok, err)
	}
	// 失敗した分は保存されていないこと。
	if _, ok, err := db.EvalFor("sess-fail", "v1", "hash-sess-fail"); err != nil || ok {
		t.Errorf("EvalFor(sess-fail) = ok=%v err=%v, want ok=false（失敗したので保存されないはず）", ok, err)
	}
}

func TestConfirmCost_YesSkipsPrompt(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	if err := confirmCost(cmd, "テスト", 3, 0.3, true); err != nil {
		t.Errorf("confirmCost(yes=true) error = %v, want nil", err)
	}
}

func TestConfirmCost_NonInteractiveWithoutYesErrors(t *testing.T) {
	cmd := &cobra.Command{}
	// bytes.Reader は *os.File ではないので isInteractiveStdin は必ず false になる
	// （テストが誤って対話プロンプトで停止しないようにするための設計）。
	cmd.SetIn(strings.NewReader("y\n"))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	err := confirmCost(cmd, "テスト", 3, 0.3, false)
	if err == nil {
		t.Fatal("confirmCost(非対話, yes=false) error = nil, want error（--yes を要求するはず）")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %q, want メッセージに --yes を含む", err.Error())
	}
}

func TestResolveJudgeRange(t *testing.T) {
	t.Run("date", func(t *testing.T) {
		from, to, label, err := resolveJudgeRange(judgeOptions{Date: "2026-08-20"})
		if err != nil {
			t.Fatalf("resolveJudgeRange() error = %v", err)
		}
		if label.from != "2026-08-20" || label.to != "2026-08-20" {
			t.Errorf("label = %+v, want from=to=2026-08-20", label)
		}
		if to.Before(from) {
			t.Errorf("to (%v) が from (%v) より前", to, from)
		}
	})

	t.Run("from-to", func(t *testing.T) {
		_, _, label, err := resolveJudgeRange(judgeOptions{From: "2026-08-01", To: "2026-08-20"})
		if err != nil {
			t.Fatalf("resolveJudgeRange() error = %v", err)
		}
		if label.from != "2026-08-01" || label.to != "2026-08-20" {
			t.Errorf("label = %+v, want from=2026-08-01 to=2026-08-20", label)
		}
	})

	t.Run("default is today", func(t *testing.T) {
		_, _, label, err := resolveJudgeRange(judgeOptions{})
		if err != nil {
			t.Fatalf("resolveJudgeRange() error = %v", err)
		}
		today := time.Now().Local().Format(dayLayout)
		if label.from != today || label.to != today {
			t.Errorf("label = %+v, want today (%s)", label, today)
		}
	})
}

func TestEstimateCostPerSession(t *testing.T) {
	haiku := estimateCostPerSession("claude-haiku-4-5")
	sonnet := estimateCostPerSession("claude-sonnet-5")
	if haiku != estimatedCostPerSessionHaikuUSD {
		t.Errorf("haiku 単価 = %v, want %v", haiku, estimatedCostPerSessionHaikuUSD)
	}
	if sonnet <= haiku {
		t.Errorf("sonnet 単価 (%v) は haiku (%v) より高いはず", sonnet, haiku)
	}
}

func TestJudge_NoTargetsSkipsConfirmAndBuild(t *testing.T) {
	// 対象セッションが 0 件なら、確認プロンプトも claude バックエンドの構築も一切行わずに
	// 正常終了すること（buildJudge を呼ぶと claude 実行ファイルが無い CI 環境で失敗するため、
	// このテストが green であること自体が「未呼び出し」の証拠になる）。
	tmp := t.TempDir()
	configPath := tmp + "/config.yaml"
	dbPath := tmp + "/insights.db"

	cfg := config.Default()
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save() error = %v", err)
	}

	root := NewRootCommand("test")
	root.AddCommand(newJudgeCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "judge", "--date", "2026-08-20"})

	if err := root.Execute(); err != nil {
		t.Fatalf("judge (0 件) error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}

	// --json では無いので stdout は人間向けテキスト。最低限、空振りメッセージが出ていることを確認する。
	if !strings.Contains(outBuf.String(), "評価対象がありません") {
		t.Errorf("stdout に「評価対象がありません」が無い: %s", outBuf.String())
	}
}
