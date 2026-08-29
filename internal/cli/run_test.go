package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/store"
)

// TestRun_EmptyDayAllStagesSucceedWithoutAI は、その日にセッションが 1 件も無く、
// ingest 元にも新規ファイルが無い場合、run が ingest/judge/daily の 3 段階すべてを
// AI を一切呼ばずに正常終了できることを検証する。
//
// judge/daily は対象セッションが 0 件のとき buildJudge（claude 実行ファイルの検出）すら
// 呼ばない設計になっているため、claude が存在しない環境でもこのテストは green になるはずで、
// それ自体が「AI 未呼び出し」の証拠になる。
func TestRun_EmptyDayAllStagesSucceedWithoutAI(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	if err := os.MkdirAll(filepath.Join(fakeClaudeHome, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	reportsDir := filepath.Join(tmp, "reports")

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	cfg.Output.Dir = reportsDir
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save() error = %v", err)
	}

	root := NewRootCommand("test")
	root.AddCommand(newRunCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "run", "--date", "2026-08-24", "--yes"})

	if err := root.Execute(); err != nil {
		t.Fatalf("run（空振り）error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}

	var result runResult
	if err := json.Unmarshal(outBuf.Bytes(), &result); err != nil {
		t.Fatalf("stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
	}

	if len(result.Stages) != 3 {
		t.Fatalf("Stages の件数 = %d, want 3: %+v", len(result.Stages), result.Stages)
	}
	for _, s := range result.Stages {
		if !s.OK {
			t.Errorf("stage %s が失敗扱い: %+v", s.Name, s)
		}
	}
	if result.Ingest == nil || result.Judge == nil || result.Daily == nil {
		t.Fatalf("各段階の結果が nil: ingest=%v judge=%v daily=%v", result.Ingest, result.Judge, result.Daily)
	}
	if !result.Daily.NoSessions {
		t.Error("Daily.NoSessions = false, want true（セッションが無いはず）")
	}
}

// TestRun_JudgeStageRequiresYesInNonInteractiveEnv は、ingest 段階が成功したあとに
// judge 段階が非対話環境・--yes 無しで課金確認に失敗した場合、
//   - ingest の結果は runResult に残ること
//   - judge が失敗段階として明示されること
//   - daily は実行されない（Stages に含まれない）こと
//   - コマンド全体はエラーを返すこと
//
// を検証する。DB には ingest を経由せず直接セッションを投入し、評価対象を発生させる
// （claude を実際に呼ぶことなく、confirmCost の --yes 必須チェックだけを検証するため）。
func TestRun_JudgeStageRequiresYesInNonInteractiveEnv(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	if err := os.MkdirAll(filepath.Join(fakeClaudeHome, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	cfg.Output.Dir = filepath.Join(tmp, "reports")
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save() error = %v", err)
	}

	// ingest を経由せず、評価対象になるセッションを直接 DB に投入しておく。
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: mustParseDay(t, "2026-08-25"), CostUSD: 0.05, CostKnown: true,
	})
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	root := NewRootCommand("test")
	root.AddCommand(newRunCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	// bytes.Reader は *os.File ではないため非対話として扱われる。--yes を付けていないので
	// judge 段階の confirmCost が「--yes を指定してください」エラーを返すはず。
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "run", "--date", "2026-08-25"})

	err = root.Execute()
	if err == nil {
		t.Fatal("run() error = nil, want error（judge 段階が --yes 無しで失敗するはず）")
	}
	if !strings.Contains(err.Error(), "judge") {
		t.Errorf("error = %q, want judge 段階での失敗を明示するメッセージ", err.Error())
	}

	var result runResult
	if jsonErr := json.Unmarshal(outBuf.Bytes(), &result); jsonErr != nil {
		t.Fatalf("stdout の JSON デコードに失敗しました: %v\nstdout=%s", jsonErr, outBuf.String())
	}

	if len(result.Stages) != 2 {
		t.Fatalf("Stages の件数 = %d, want 2（ingest, judge のみ。daily は実行されないはず）: %+v", len(result.Stages), result.Stages)
	}
	if !result.Stages[0].OK {
		t.Errorf("ingest 段階が失敗扱い: %+v", result.Stages[0])
	}
	if result.Stages[1].OK {
		t.Error("judge 段階が成功扱いになっている。--yes 無しでは失敗するはず")
	}
	if result.Ingest == nil {
		t.Error("Ingest の結果が nil。ingest は成功しているので結果が残るはず")
	}
	if result.Daily != nil {
		t.Error("Daily の結果が nil ではない。judge が失敗した場合 daily は実行されないはず")
	}
}

func mustParseDay(t *testing.T, date string) (start time.Time) {
	t.Helper()
	start, _, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange(%s) error = %v", date, err)
	}
	return start
}
