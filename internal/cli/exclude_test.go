package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/store"
)

func TestFilterExcludedSessions(t *testing.T) {
	cfg := config.Default()
	cfg.Exclude.Projects = []string{"/proj-secret"}
	cfg.Exclude.Entrypoints = []string{"sdk-cli"}

	rows := []store.SessionRow{
		{SessionID: "keep", ProjectPath: "/proj-a", Entrypoint: "cli"},
		{SessionID: "drop-project", ProjectPath: "/proj-secret", Entrypoint: "cli"},
		{SessionID: "drop-entrypoint", ProjectPath: "/proj-a", Entrypoint: "sdk-cli"},
		// 除外設定の比較は大文字小文字とパス区切りの差を吸収する。
		{SessionID: "drop-case", ProjectPath: "/PROJ-Secret", Entrypoint: "SDK-CLI"},
	}

	kept, excluded := filterExcludedSessions(cfg, rows)
	if excluded != 3 {
		t.Errorf("excluded = %d, want 3", excluded)
	}
	if len(kept) != 1 || kept[0].SessionID != "keep" {
		t.Errorf("kept = %+v, want [keep]", kept)
	}
}

func TestFilterExcludedSessionsNoConfig(t *testing.T) {
	cfg := config.Default() // 除外設定なし
	rows := []store.SessionRow{
		{SessionID: "a", ProjectPath: "/proj-a", Entrypoint: "cli"},
		{SessionID: "b", ProjectPath: "/proj-b", Entrypoint: "cli"},
	}
	kept, excluded := filterExcludedSessions(cfg, rows)
	if excluded != 0 || len(kept) != 2 {
		t.Errorf("除外設定が無いのに落ちました: kept=%d excluded=%d", len(kept), excluded)
	}
}

// 取り込んだ後に除外設定を足した場合でも、そのプロジェクトが評価されないこと。
// 除外に気づくのはたいてい一度取り込んだ後なので、ここが効かないと設定を足す意味が無い。
//
// 評価対象が 0 件なら claude バックエンドを構築しないため、claude が無い環境でも
// このテストが green であること自体が「評価されなかった」証拠になる。
func TestJudge_ExcludedProjectIsNotEvaluated(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	// 除外設定が無かった時期に取り込まれた、という状況を作る。
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	saveTestSession(t, db, testSessionSpec{
		SessionID: "excluded-1", ProjectPath: "/proj-secret", Title: "秘密のプロジェクト",
		FirstPrompt: "何かする", StartedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	})
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	cfg := config.Default()
	cfg.Exclude.Projects = []string{"/proj-secret"}
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
		t.Fatalf("judge error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}

	out := outBuf.String()
	if !strings.Contains(out, "評価対象がありません") {
		t.Errorf("除外したプロジェクトが評価対象に残っています: %s", out)
	}
	if !strings.Contains(out, "除外（設定 exclude）: 1 件") {
		t.Errorf("除外件数が出力されていません: %s", out)
	}
}

// daily も同じく、除外したプロジェクトしかない日は「セッションが無い日」として扱う
// （評価もレポート生成も走らせない）。
func TestDaily_ExcludedProjectIsNotReported(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	saveTestSession(t, db, testSessionSpec{
		SessionID: "excluded-1", ProjectPath: "/proj-secret", Title: "秘密のプロジェクト",
		FirstPrompt: "何かする", StartedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	})
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	cfg := config.Default()
	cfg.Exclude.Projects = []string{"/proj-secret"}
	cfg.Output.Dir = filepath.Join(tmp, "reports")
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save() error = %v", err)
	}

	root := NewRootCommand("test")
	root.AddCommand(newDailyCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "daily", "--date", "2026-08-20", "--yes"})

	if err := root.Execute(); err != nil {
		t.Fatalf("daily error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "その日のセッションがありません") {
		t.Errorf("除外したプロジェクトが日報の対象に残っています: %s", outBuf.String())
	}
}
