package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/claudecli"
	"github.com/fuchigta/insights/internal/judge/prompts"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/store"
	"github.com/spf13/cobra"
)

// fakeDailyJudge は judge.Judge のテスト用フェイク実装。実際の claude サブプロセスは呼ばない。
// セッション評価（Schema が prompts.SessionEvalSchema と一致）と、日報・振り返り生成
// （rollup.Synthesize が呼ぶ。Schema は非公開のため呼び出し順で判別する）の両方に応答する。
//
// runDaily の実装上、セッション評価（evaluateSessions）はすべて完了してから
// rollup.Synthesize が呼ばれ、その中で日報 -> 振り返りの順に逐次呼ばれることが保証されている
// （rollup.Synthesize のソースコード参照）。そのため「セッション評価以外の呼び出しのうち
// 1回目が日報、2回目が振り返り」という判別で安全に対応できる。
type fakeDailyJudge struct {
	mu              sync.Mutex
	sessionCalls    int
	nonSessionCalls int
}

func (f *fakeDailyJudge) Name() string     { return "fake-daily-judge" }
func (f *fakeDailyJudge) Available() error { return nil }

func (f *fakeDailyJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.Contains(req.Prompt, "## セッション基本情報") {
		f.sessionCalls++
		return validEvalJSON("achieved"), nil
	}

	f.nonSessionCalls++
	if f.nonSessionCalls == 1 {
		return validDailyJSON(), nil
	}
	return validRetroJSON(), nil
}

// fakeRateLimitDailyJudge は、セッション評価のリクエストに対して常に claudecli.ErrRateLimited
// をラップしたエラーを返す judge.Judge のフェイク実装。日報・振り返り生成（rollup.Synthesize）
// のリクエストが来た場合は nonSessionCalls を数えるだけにしておき、
// 「レート制限を検知したら Synthesize 相当の呼び出しが一度も行われない」ことを検証できるようにする。
type fakeRateLimitDailyJudge struct {
	sessionCalls    int
	nonSessionCalls int
}

func (f *fakeRateLimitDailyJudge) Name() string     { return "fake-ratelimit-daily-judge" }
func (f *fakeRateLimitDailyJudge) Available() error { return nil }

func (f *fakeRateLimitDailyJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	if strings.Contains(req.Prompt, "## セッション基本情報") {
		f.sessionCalls++
		return nil, fmt.Errorf("claude の実行がレート制限らしきエラーで失敗しました: %w", claudecli.ErrRateLimited)
	}
	// レート制限で打ち切られる想定のため、ここに到達したら回帰（バグの再発）。
	f.nonSessionCalls++
	return validDailyJSON(), nil
}

// TestDaily_RateLimitAbortsBeforeSynthesize は、評価段階でレート制限を検知した場合、
// runDaily が evalStageError で打ち切り、日報・振り返りの生成（rollup.Synthesize 相当の
// AI 呼び出し）に一度も進まないことを確認する回帰テスト。
// 進んでしまうと、レート制限中に確実に失敗する追加の AI 呼び出しが発生し、
// 中身の無い成果物と余計な課金だけが残ってしまう。
func TestDaily_RateLimitAbortsBeforeSynthesize(t *testing.T) {
	db := newTempDB(t)
	outDir := t.TempDir()
	cfg := config.Default()
	cfg.Output.Dir = outDir

	date := "2026-08-26"
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.03, CostKnown: true,
	})

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeRateLimitDailyJudge{}
	cmd := newDailyTestCmd(t)

	if _, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, false, true); err == nil {
		t.Fatal("runDaily() error = nil, want error（レート制限で打ち切るはず）")
	}

	if fj.sessionCalls == 0 {
		t.Error("sessionCalls = 0, want > 0（評価自体は試みられているはず）")
	}
	if fj.nonSessionCalls != 0 {
		t.Errorf("nonSessionCalls = %d, want 0（日報・振り返りの生成に進んではいけない）", fj.nonSessionCalls)
	}
}

func newDailyTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func TestDaily_WritesTwoMarkdownFilesAndExcludesSidechain(t *testing.T) {
	db := newTempDB(t)
	outDir := t.TempDir()
	cfg := config.Default()
	cfg.Output.Dir = outDir
	cfg.Judge.Concurrency = 2

	date := "2026-08-20"
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "parent-1", ProjectPath: "/proj-a", ProjectLabel: "proj-a",
		Title: "親セッション", FirstPrompt: "fix the bug",
		StartedAt: base, EndedAt: base.Add(10 * time.Minute), CostUSD: 0.10, CostKnown: true,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "child-1", IsSidechain: true, ParentSessionID: "parent-1",
		ProjectPath: "/proj-a", ProjectLabel: "proj-a",
		Title: "サブエージェント", FirstPrompt: "sub task",
		StartedAt: base.Add(time.Minute), EndedAt: base.Add(3 * time.Minute), CostUSD: 0.02, CostKnown: true,
	})

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("前提: rows の件数 = %d, want 2", len(rows))
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeDailyJudge{}
	cmd := newDailyTestCmd(t)

	result, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, false, true)
	if err != nil {
		t.Fatalf("runDaily() error = %v", err)
	}

	if result.NoSessions {
		t.Fatal("NoSessions = true, want false（セッションがあるはず）")
	}
	if result.JudgeEvaluated != 2 {
		t.Errorf("JudgeEvaluated = %d, want 2（親もサブエージェントも評価されるはず）", result.JudgeEvaluated)
	}
	if result.JudgeFailed != 0 {
		t.Errorf("JudgeFailed = %d, want 0", result.JudgeFailed)
	}
	if fj.sessionCalls != 2 {
		t.Errorf("fakeDailyJudge.sessionCalls = %d, want 2（サブエージェントも個別に評価されるはず）", fj.sessionCalls)
	}
	if fj.nonSessionCalls != 2 {
		t.Errorf("fakeDailyJudge.nonSessionCalls = %d, want 2（日報・振り返りで1回ずつ）", fj.nonSessionCalls)
	}

	if result.DailyPath == "" || result.RetroPath == "" {
		t.Fatal("DailyPath/RetroPath が空")
	}
	dailyBytes, err := os.ReadFile(result.DailyPath)
	if err != nil {
		t.Fatalf("日報ファイルの読み取りに失敗: %v", err)
	}
	if len(dailyBytes) == 0 {
		t.Error("日報ファイルが空")
	}
	retroBytes, err := os.ReadFile(result.RetroPath)
	if err != nil {
		t.Fatalf("振り返りファイルの読み取りに失敗: %v", err)
	}
	if len(retroBytes) == 0 {
		t.Error("振り返りファイルが空")
	}

	wantDailyPath := filepath.Join(outDir, "daily", date+".md")
	wantRetroPath := filepath.Join(outDir, "retro", date+".md")
	if filepath.Clean(result.DailyPath) != filepath.Clean(wantDailyPath) {
		t.Errorf("DailyPath = %q, want %q", result.DailyPath, wantDailyPath)
	}
	if filepath.Clean(result.RetroPath) != filepath.Clean(wantRetroPath) {
		t.Errorf("RetroPath = %q, want %q", result.RetroPath, wantRetroPath)
	}

	// SaveRollup で保存されていること（rollup 経由で再取得できる）。
	if _, ok, err := db.Rollup(date); err != nil || !ok {
		t.Errorf("db.Rollup(%s) = ok=%v err=%v, want ok=true", date, ok, err)
	}
}

func TestDaily_EvalCacheIsReusedOnSecondRun(t *testing.T) {
	db := newTempDB(t)
	outDir := t.TempDir()
	cfg := config.Default()
	cfg.Output.Dir = outDir

	date := "2026-08-21"
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.03, CostKnown: true,
	})

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeDailyJudge{}
	cmd := newDailyTestCmd(t)

	if _, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, false, true); err != nil {
		t.Fatalf("runDaily() 1回目 error = %v", err)
	}
	if fj.sessionCalls != 1 {
		t.Fatalf("1回目: sessionCalls = %d, want 1", fj.sessionCalls)
	}

	// 2回目: 同じ日をもう一度実行しても、評価キャッシュが効いてセッション評価は再実行されない。
	result2, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, false, true)
	if err != nil {
		t.Fatalf("runDaily() 2回目 error = %v", err)
	}
	if fj.sessionCalls != 1 {
		t.Errorf("2回目後: sessionCalls = %d, want 1（キャッシュが効いて再評価されないはず）", fj.sessionCalls)
	}
	if result2.JudgeEvaluated != 0 {
		t.Errorf("2回目: JudgeEvaluated = %d, want 0", result2.JudgeEvaluated)
	}
}

// TestDaily_NoSessionsSkipsAI は、その日にセッションが 1 件も無い場合、コマンド全体
// （cobra 経由のフル実行）が AI を一切呼ばずに正常終了することを検証する。
// dailyRun はセッション 0 件のとき buildJudge すら呼ばないため、claude 実行ファイルが
// 存在しない環境でもこのテストは成功するはずである（それ自体が「AI 未呼び出し」の証拠）。
func TestDaily_NoSessionsSkipsAI(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	reportsDir := filepath.Join(tmp, "reports")

	cfg := config.Default()
	cfg.Output.Dir = reportsDir
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save() error = %v", err)
	}

	root := NewRootCommand("test")
	root.AddCommand(newDailyCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "daily", "--date", "2026-08-22"})

	if err := root.Execute(); err != nil {
		t.Fatalf("daily（セッション0件）error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}

	if !strings.Contains(outBuf.String(), "セッションがありません") {
		t.Errorf("stdout に案内メッセージが無い: %s", outBuf.String())
	}

	if _, err := os.Stat(filepath.Join(reportsDir, "daily", "2026-08-22.md")); !os.IsNotExist(err) {
		t.Errorf("空の日報ファイルが生成されてしまっている（err=%v）", err)
	}
	if _, err := os.Stat(filepath.Join(reportsDir, "retro", "2026-08-22.md")); !os.IsNotExist(err) {
		t.Errorf("空の振り返りファイルが生成されてしまっている（err=%v）", err)
	}
}

func TestDaily_NoJudgeSkipsEvaluation(t *testing.T) {
	db := newTempDB(t)
	outDir := t.TempDir()
	cfg := config.Default()
	cfg.Output.Dir = outDir

	date := "2026-08-23"
	base := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.03, CostKnown: true,
	})

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeDailyJudge{}
	cmd := newDailyTestCmd(t)

	result, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, true, true)
	if err != nil {
		t.Fatalf("runDaily(noJudge=true) error = %v", err)
	}
	if !result.SkippedJudge {
		t.Error("SkippedJudge = false, want true")
	}
	if fj.sessionCalls != 0 {
		t.Errorf("sessionCalls = %d, want 0（--no-judge のはず）", fj.sessionCalls)
	}
	// --no-judge でも日報・振り返り自体は生成される（Synthesize は常に呼ばれる）。
	if fj.nonSessionCalls != 2 {
		t.Errorf("nonSessionCalls = %d, want 2", fj.nonSessionCalls)
	}
	if result.DailyPath == "" || result.RetroPath == "" {
		t.Error("--no-judge でも日報・振り返りは生成されるはず")
	}
}

// TestDaily_JudgeCostComesFromExistingEvalsWhenNotJudgedThisRun は、`insights judge` で
// 先に評価を済ませてから日報を作る経路（--no-judge で実行しても、その日のセッションが
// 既に別の実行で評価済みのケースに相当する）で meta.judge_cost_usd / meta.judge_session_ids
// が常に 0 / 空になっていた回帰の再発防止テスト。
//
// runDaily はその実行内で評価した分だけを集計するのではなく、DB に残っている
// session_evals から db.EvalRunTotals で引き直すことで、評価を先に済ませた経路でも
// 正しいコストと run_session_id を日報に載せられるようにしている。
func TestDaily_JudgeCostComesFromExistingEvalsWhenNotJudgedThisRun(t *testing.T) {
	db := newTempDB(t)
	outDir := t.TempDir()
	cfg := config.Default()
	cfg.Output.Dir = outDir

	date := "2026-08-24"
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.03, CostKnown: true,
	})

	// 「insights judge を先に実行済み」を模して、このテスト自身の runDaily 呼び出しより前に
	// 評価結果を保存しておく。prompt_version は prompts.PromptVersion と一致させないと
	// キャッシュとして扱われず、EvalRunTotals の対象からも外れてしまう点に注意。
	if err := db.SaveEval("sess-1", "claude-cli", "claude-opus-5", prompts.PromptVersion, "hash-sess-1",
		validEvalJSON("achieved"), store.EvalRun{CostUSD: 0.05, SessionID: "judge-run-1"}); err != nil {
		t.Fatalf("SaveEval() error = %v", err)
	}

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeDailyJudge{}
	cmd := newDailyTestCmd(t)

	// noJudge = true: この実行では 1 件も評価しない（sessionCalls が増えないことで確認する）。
	// dailyResult（CLI の人間向け出力用の集計値）はこの実行内で評価した分の
	// judgeCostUSD/judgeSessionIDs をそのまま使っており今回の修正対象ではないため、
	// ここでは日報（rollup.Daily）の meta 側を検証する。
	if _, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, true, true); err != nil {
		t.Fatalf("runDaily(noJudge=true) error = %v", err)
	}
	if fj.sessionCalls != 0 {
		t.Fatalf("sessionCalls = %d, want 0（この実行では評価していないはず）", fj.sessionCalls)
	}

	// DB に保存された daily rollup の meta.judge_cost_usd / meta.judge_session_ids を検証する
	// （generate 直後に render.WriteReports が書き出す front matter も同じ daily.Meta が元）。
	rawRollup, ok, err := db.Rollup(date)
	if err != nil || !ok {
		t.Fatalf("db.Rollup(%s) = ok=%v err=%v, want ok=true", date, ok, err)
	}
	var rollupDoc struct {
		Meta struct {
			JudgeCostUSD    float64  `json:"judge_cost_usd"`
			JudgeSessionIDs []string `json:"judge_session_ids"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rawRollup, &rollupDoc); err != nil {
		t.Fatalf("rollup JSON の unmarshal に失敗: %v", err)
	}
	if want := 0.05; rollupDoc.Meta.JudgeCostUSD < want-1e-9 || rollupDoc.Meta.JudgeCostUSD > want+1e-9 {
		t.Errorf("rollup.meta.judge_cost_usd = %v, want %v", rollupDoc.Meta.JudgeCostUSD, want)
	}
	found := false
	for _, id := range rollupDoc.Meta.JudgeSessionIDs {
		if id == "judge-run-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("rollup.meta.judge_session_ids = %v, want judge-run-1 を含む", rollupDoc.Meta.JudgeSessionIDs)
	}
}
