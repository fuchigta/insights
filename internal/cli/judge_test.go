package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/claudecli"
	"github.com/fuchigta/insights/internal/store"
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
	// 委譲の向き（親より安いモデルへ下ろしたのか、高いモデルへ上げたのか）を評価者が
	// 判別できるよう、子が動いたモデルも要約に含める。
	if len(kids[0].Models) != 1 || kids[0].Models[0] != "claude-sonnet-5" {
		t.Errorf("child models = %v, want [claude-sonnet-5]", kids[0].Models)
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

	if err := db.SaveEval("sess-1", "fake-judge", "claude-sonnet-5", "v1", targets[0].ContentHash, validEvalJSON("achieved"), store.EvalRun{}); err != nil {
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

// TestEvaluateSessions_RecordsEvalRuns は、成功も失敗も実行記録として残ることを確かめる。
// 成功した評価だけを見ていると「特定の形のセッションで失敗し続けている」ことに気づけない、
// というのがこの記録を入れた動機なので、失敗が残ることが要点。
func TestEvaluateSessions_RecordsEvalRuns(t *testing.T) {
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

	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	result, err := evaluateSessions(context.Background(), evalDeps{
		DB: db, Judge: &fakeSessionJudge{failMarker: "fail-task"}, Cfg: config.Default(),
		Model: "claude-sonnet-5", JudgeName: "fake-judge", PromptVersion: "v1", Concurrency: 1,
	}, rows, nil, map[string]*sessionCostAgg{}, io.Discard)
	if err != nil {
		t.Fatalf("evaluateSessions() error = %v", err)
	}
	if len(result.Succeeded) != 1 || len(result.Failed) != 1 {
		t.Fatalf("前提: 成功 %d 件 / 失敗 %d 件, want 1 件ずつ", len(result.Succeeded), len(result.Failed))
	}

	stats, err := db.EvalRunStatsInRange(base.Add(-24*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("EvalRunStatsInRange() error = %v", err)
	}
	if stats.Total != 2 || stats.Succeeded != 1 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want Total=2 Succeeded=1 Failed=1", stats)
	}
	// フェイクは番兵エラーを返さないので「その他」に分類される。
	if got := stats.FailuresByKind[store.EvalFailureOther]; got != 1 {
		t.Errorf("FailuresByKind[%s] = %d, want 1（内訳 = %v）", store.EvalFailureOther, got, stats.FailuresByKind)
	}
}

// TestEvaluateSessions_RateLimitRunRecords は、レート制限で打ち切ったときの記録を確かめる。
// 実際に評価を試した 1 件だけが記録され、打ち切って評価しなかったぶんは記録されない
// （試していない評価を失敗として数えると、失敗率が実態より悪く見えるため）。
func TestEvaluateSessions_RateLimitRunRecords(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	const n = 4
	for i := 0; i < n; i++ {
		saveTestSession(t, db, testSessionSpec{
			SessionID: fmt.Sprintf("sess-%d", i), ProjectPath: "/proj-a", FirstPrompt: "task",
			StartedAt: base.Add(time.Duration(i) * time.Minute), CostUSD: 0.01, CostKnown: true,
		})
	}
	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	if _, err := evaluateSessions(context.Background(), evalDeps{
		DB: db, Judge: &fakeRateLimitJudge{}, Cfg: config.Default(),
		Model: "claude-sonnet-5", JudgeName: "fake-judge", PromptVersion: "v1", Concurrency: 1,
	}, rows, nil, map[string]*sessionCostAgg{}, io.Discard); err != nil {
		t.Fatalf("evaluateSessions() error = %v", err)
	}

	stats, err := db.EvalRunStatsInRange(base.Add(-24*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("EvalRunStatsInRange() error = %v", err)
	}
	if stats.Total != 1 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want Total=1 Failed=1（打ち切ったぶんは記録しない）", stats)
	}
	if got := stats.FailuresByKind[store.EvalFailureRateLimit]; got != 1 {
		t.Errorf("FailuresByKind[%s] = %d, want 1（内訳 = %v）", store.EvalFailureRateLimit, got, stats.FailuresByKind)
	}
}

// TestClassifyEvalFailure は失敗の分類が番兵エラー経由で効くことを確かめる。
// ラップされたエラーでも errors.Is で辿れることが要点（メッセージ一致に戻ると静かに壊れる）。
func TestClassifyEvalFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"レート制限", fmt.Errorf("AI 評価の実行に失敗しました: %w", claudecli.ErrRateLimited), store.EvalFailureRateLimit},
		{"タイムアウト", fmt.Errorf("AI 評価の実行に失敗しました: %w", claudecli.ErrTimeout), store.EvalFailureTimeout},
		{"スキーマ不適合", fmt.Errorf("AI 評価の実行に失敗しました: %w", claudecli.ErrSchemaMismatch), store.EvalFailureSchema},
		{"それ以外", errors.New("boom"), store.EvalFailureOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyEvalFailure(tt.err); got != tt.want {
				t.Errorf("classifyEvalFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeRateLimitJudge は、どのセッションを評価してもレート制限らしきエラー
// （claudecli.ErrRateLimited）でラップして返す judge.Judge のフェイク実装。
// evaluateSessions がレート制限検知で残りの評価を打ち切ることを検証するために使う。
type fakeRateLimitJudge struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeRateLimitJudge) Name() string     { return "fake-ratelimit-judge" }
func (f *fakeRateLimitJudge) Available() error { return nil }

func (f *fakeRateLimitJudge) Evaluate(_ context.Context, _ judge.Request) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil, fmt.Errorf("claude の実行がレート制限らしきエラーで失敗しました: %w", claudecli.ErrRateLimited)
}

func (f *fakeRateLimitJudge) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestEvaluateSessions_RateLimitAbortsRemainingTargets は、いずれかのセッションの評価が
// claudecli.ErrRateLimited を含むエラーで失敗したら、残りの未着手セッションは評価せずに
// 打ち切ることを確認する回帰テスト。
//
// 並行度に依存して不安定にならないよう Concurrency: 1 で実行する（それでも worker が
// 次のジョブを取り出す判定と、メインループが abortEval を呼ぶタイミングは厳密には
// レースしうるため、呼び出し回数はターゲット数「未満」であることだけを確認する）。
func TestEvaluateSessions_RateLimitAbortsRemainingTargets(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	const n = 5
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sess-%d", i)
		saveTestSession(t, db, testSessionSpec{
			SessionID: id, ProjectPath: "/proj-a", FirstPrompt: "task",
			StartedAt: base.Add(time.Duration(i) * time.Hour),
			EndedAt:   base.Add(time.Duration(i)*time.Hour + 5*time.Minute),
			CostUSD:   0.01, CostKnown: true,
		})
	}

	from, to := base.Add(-time.Hour), base.Add(24*time.Hour)
	rows, err := db.SessionsInRange(from, to)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	if len(rows) != n {
		t.Fatalf("前提: rows の件数 = %d, want %d", len(rows), n)
	}

	fj := &fakeRateLimitJudge{}
	cfg := config.Default()

	result, err := evaluateSessions(context.Background(), evalDeps{
		DB: db, Judge: fj, Cfg: cfg, Model: "claude-sonnet-5", JudgeName: "fake-judge",
		PromptVersion: "v1", Concurrency: 1,
	}, rows, nil, map[string]*sessionCostAgg{}, io.Discard)
	if err != nil {
		t.Fatalf("evaluateSessions() error = %v, want nil（レート制限は ctx キャンセルではないので error を返さない）", err)
	}

	if !result.RateLimited {
		t.Error("RateLimited = false, want true")
	}
	if len(result.Succeeded) != 0 {
		t.Errorf("Succeeded = %v, want 空（レート制限で全滅するはず）", result.Succeeded)
	}
	if len(result.Failed) != n {
		t.Errorf("Failed の件数 = %d, want %d（打ち切り分も Failed に記録されるはず）", len(result.Failed), n)
	}
	// Concurrency=1 なので、レート制限を踏んだ 1 件目でワーカーが打ち切り、残りは
	// 評価に入らない。件数を厳密に見ることで「集約が追いつけば止まる」程度の
	// 実行速度まかせの打ち切りに戻ったら気づける（実際 Linux CI で踏んだ）。
	if got := fj.callCount(); got != 1 {
		t.Errorf("callCount = %d, want 1（1 件目で打ち切り、残りは評価しないはず）", got)
	}
}

// TestEvalStageError は evalStageError の判定表を確認する回帰テスト。
// レート制限、全滅、部分失敗、全件成功の 4 パターンで「後続の AI 処理に進んでよいか」の
// 判定が変わることを検証する。
func TestEvalStageError(t *testing.T) {
	cases := []struct {
		name    string
		result  *evalRunResult
		wantErr bool
	}{
		{
			name:    "nil result はエラーにしない",
			result:  nil,
			wantErr: false,
		},
		{
			name: "レート制限で打ち切ったときはエラー",
			result: &evalRunResult{
				RateLimited: true,
				Succeeded:   []string{"sess-ok"},
				Failed:      []evalFailure{{SessionID: "sess-ng", Reason: "boom"}},
			},
			wantErr: true,
		},
		{
			name: "成功0件・失敗1件以上（全滅）はエラー",
			result: &evalRunResult{
				Failed: []evalFailure{{SessionID: "sess-ng", Reason: "boom"}},
			},
			wantErr: true,
		},
		{
			name: "部分失敗（成功1件以上）はエラーにしない",
			result: &evalRunResult{
				Succeeded: []string{"sess-ok"},
				Failed:    []evalFailure{{SessionID: "sess-ng", Reason: "boom"}},
			},
			wantErr: false,
		},
		{
			name: "全件成功はエラーにしない",
			result: &evalRunResult{
				Succeeded: []string{"sess-ok-1", "sess-ok-2"},
			},
			wantErr: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := evalStageError(c.result)
			if (err != nil) != c.wantErr {
				t.Errorf("evalStageError(%+v) error = %v, wantErr %v", c.result, err, c.wantErr)
			}
		})
	}
}

func TestConfirmCost_YesSkipsPrompt(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	if err := confirmCost(cmd, "テスト", 3, 0.3, "", true); err != nil {
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

	err := confirmCost(cmd, "テスト", 3, 0.3, "", false)
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

// TestAggregateSessionCosts_Models は、セッションで使われたモデルが利用量の多い順に
// 集計され、API 呼び出しを伴わない擬似モデルが除外されることを検証する。委譲の要約に
// 載せて「どのモデルへ委譲したのか」を評価者に伝えるための材料になる。
func TestAggregateSessionCosts_Models(t *testing.T) {
	ts := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	rows := []store.UsageRow{
		{SessionID: "s1", Timestamp: ts, Model: "claude-opus-5", CostUSD: 0.10, CostKnown: true},
		{SessionID: "s1", Timestamp: ts, Model: "claude-haiku-4-5", CostUSD: 0.01, CostKnown: true},
		{SessionID: "s1", Timestamp: ts, Model: "claude-haiku-4-5", CostUSD: 0.01, CostKnown: true},
		{SessionID: "s1", Timestamp: ts, Model: "<synthetic>", CostKnown: true},
		{SessionID: "s2", Timestamp: ts, Model: "", CostKnown: false},
	}

	got := aggregateSessionCosts(rows)

	models := got["s1"].models()
	want := []string{"claude-haiku-4-5", "claude-opus-5"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v（擬似モデルは除外し、利用量の多い順）", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models = %v, want %v（擬似モデルは除外し、利用量の多い順）", models, want)
		}
	}

	// モデル名が取れない usage しか無いセッションでは、空の一覧を返す
	// （委譲セクション側で「不明」と明示させるため）。
	if m := got["s2"].models(); len(m) != 0 {
		t.Errorf("models = %v, want 空（モデル名が取れていない）", m)
	}
}
