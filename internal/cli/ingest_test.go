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

// --- テスト用ヘルパ ---

// runIngestCLI は NewRootCommand + newIngestCommand を組み合わせて実行する。
// root.go 自体は変更していないため、この組み立ては毎回テスト側で行う。
func runIngestCLI(t *testing.T, configPath, dbPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand("test")
	root.AddCommand(newIngestCommand())

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	fullArgs := append([]string{"--config", configPath, "--db", dbPath, "ingest"}, args...)
	root.SetArgs(fullArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// writeJSONLine は v を 1 行の JSON として f に書き込む。
func writeJSONLine(t *testing.T, f *os.File, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
}

// writeValidSession は Parse を通る最小限の Claude Code セッション jsonl を書き出す。
func writeValidSession(t *testing.T, path, sessionID, cwd string, ts time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer f.Close()

	writeJSONLine(t, f, map[string]any{
		"type":        "user",
		"timestamp":   ts.Format(time.RFC3339Nano),
		"cwd":         cwd,
		"gitBranch":   "main",
		"entrypoint":  "cli",
		"isSidechain": false,
		"sessionId":   sessionID,
		"message":     map[string]any{"role": "user", "content": "テストプロンプト"},
	})
	writeJSONLine(t, f, map[string]any{
		"type":        "assistant",
		"timestamp":   ts.Add(time.Second).Format(time.RFC3339Nano),
		"cwd":         cwd,
		"gitBranch":   "main",
		"entrypoint":  "cli",
		"effort":      "high",
		"isSidechain": false,
		"sessionId":   sessionID,
		"message": map[string]any{
			"model":   "claude-sonnet-5",
			"id":      "msg_" + sessionID,
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "応答テキスト"}},
			"usage": map[string]any{
				"input_tokens":            100,
				"output_tokens":           50,
				"cache_read_input_tokens": 10,
				"cache_creation":          map[string]any{"ephemeral_5m_input_tokens": 5, "ephemeral_1h_input_tokens": 0},
				"service_tier":            "standard",
			},
		},
	})
}

// writeBrokenSession は全行が壊れている jsonl を書き出す（Parse がエラーを返す想定）。
func writeBrokenSession(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "not a json line\n{broken\nanother bad line{{{\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// allSessionIDs は DB に保存済みの全セッション ID を返す（開始時刻の広い範囲で検索する）。
func allSessionIDs(t *testing.T, dbPath string) []string {
	t.Helper()
	d, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer d.Close()

	rows, err := d.SessionsInRange(time.Time{}, time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SessionsInRange: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	return ids
}

// testConfig は fakeHome を claude-code ソースのルートとし、成果物収集を無効化した
// 設定ファイルを configPath に書き出す。excludedProject が空でなければ除外設定に加える。
func writeTestConfig(t *testing.T, configPath, fakeHome, excludedProject string) {
	t.Helper()
	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeHome
	isolateCodexSource(t, cfg)
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	if excludedProject != "" {
		cfg.Exclude.Projects = []string{excludedProject}
	}
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
}

// --- テスト本体 ---

func TestIngest_BasicFlowExcludeAndBrokenFile(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	projA := filepath.Join(tmp, "proj-a")
	projExcluded := filepath.Join(tmp, "proj-excluded")

	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeHome, "projects", "proj-a-slug", "11111111-1111-1111-1111-111111111111.jsonl"),
		"11111111-1111-1111-1111-111111111111", projA, base)
	writeValidSession(t, filepath.Join(fakeHome, "projects", "proj-excluded-slug", "22222222-2222-2222-2222-222222222222.jsonl"),
		"22222222-2222-2222-2222-222222222222", projExcluded, base)
	writeBrokenSession(t, filepath.Join(fakeHome, "projects", "proj-broken-slug", "33333333-3333-3333-3333-333333333333.jsonl"))

	writeTestConfig(t, configPath, fakeHome, projExcluded)

	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence")
	if err != nil {
		t.Fatalf("ingest --all --no-evidence error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if stdout == "" {
		t.Error("stdout が空（人間向け出力が無い）")
	}

	ids := allSessionIDs(t, dbPath)
	if len(ids) != 1 {
		t.Fatalf("取り込まれたセッション数 = %d, want 1（除外・壊れたファイルを除く）: %v", len(ids), ids)
	}
	if ids[0] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("取り込まれたセッション ID = %q, want 11111111-1111-1111-1111-111111111111", ids[0])
	}

	// --- 冪等性: 2回目実行しても行数が増えない ---
	stdout2, stderr2, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence")
	if err != nil {
		t.Fatalf("ingest 2回目 error = %v\nstdout=%s\nstderr=%s", err, stdout2, stderr2)
	}
	ids2 := allSessionIDs(t, dbPath)
	if len(ids2) != 1 {
		t.Fatalf("2回目実行後のセッション数 = %d, want 1（冪等性が壊れている）: %v", len(ids2), ids2)
	}
}

func TestIngest_DryRunDoesNotWrite(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	projA := filepath.Join(tmp, "proj-a")
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeHome, "projects", "proj-a-slug", "44444444-4444-4444-4444-444444444444.jsonl"),
		"44444444-4444-4444-4444-444444444444", projA, base)

	writeTestConfig(t, configPath, fakeHome, "")

	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence", "--dry-run")
	if err != nil {
		t.Fatalf("ingest --dry-run error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	// dry-run では DB ファイル自体は store.Open が作るが、セッションは 1 件も保存されないはず。
	ids := allSessionIDs(t, dbPath)
	if len(ids) != 0 {
		t.Fatalf("dry-run 実行後のセッション数 = %d, want 0: %v", len(ids), ids)
	}
}

func TestIngest_SinceAndAllConflict(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	if err := os.MkdirAll(filepath.Join(fakeHome, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeTestConfig(t, configPath, fakeHome, "")

	_, _, err := runIngestCLI(t, configPath, dbPath, "--all", "--since", "2026-01-01", "--no-evidence")
	if err == nil {
		t.Fatal("--all と --since を同時指定してもエラーにならなかった")
	}
}

// TestIngest_SkipsEvalWorkspace は評価ワークスペース（~/.insights/judge-workspace）配下の
// セッションが、ユーザー設定（exclude.projects）に頼らず常に取り込みから除外されることを検証する。
// 実際の ~/.insights には触れないよう、USERPROFILE/HOME を一時ディレクトリに差し替える。
func TestIngest_SkipsEvalWorkspace(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv("HOME", fakeHome)

	fakeClaudeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	projA := filepath.Join(tmp, "proj-a")
	// judge-workspace は $USERPROFILE/.insights/judge-workspace（claudecli.Judge の既定と同じ規約）。
	judgeWorkspaceProj := filepath.Join(fakeHome, ".insights", "judge-workspace")

	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeClaudeHome, "projects", "proj-a-slug", "66666666-6666-6666-6666-666666666666.jsonl"),
		"66666666-6666-6666-6666-666666666666", projA, base)
	writeValidSession(t, filepath.Join(fakeClaudeHome, "projects", "judge-ws-slug", "77777777-7777-7777-7777-777777777777.jsonl"),
		"77777777-7777-7777-7777-777777777777", judgeWorkspaceProj, base)

	writeTestConfig(t, configPath, fakeClaudeHome, "")

	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence")
	if err != nil {
		t.Fatalf("ingest --all --no-evidence error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	ids := allSessionIDs(t, dbPath)
	if len(ids) != 1 {
		t.Fatalf("取り込まれたセッション数 = %d, want 1（評価ワークスペース配下を除く）: %v", len(ids), ids)
	}
	if ids[0] != "66666666-6666-6666-6666-666666666666" {
		t.Errorf("取り込まれたセッション ID = %q, want proj-a のセッション", ids[0])
	}

	var result ingestResult
	if !strings.Contains(stdout, "insights ingest") {
		t.Fatalf("stdout に人間向け出力が見当たらない: %s", stdout)
	}

	root := NewRootCommand("test")
	root.AddCommand(newIngestCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "ingest", "--all", "--no-evidence"})
	// 2回目実行（冪等性の確認を兼ねる）。JSON 出力で SkippedJudgeWorkspace を確認する。
	if err := root.Execute(); err != nil {
		t.Fatalf("ingest --json error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}
	if err := json.Unmarshal(outBuf.Bytes(), &result); err != nil {
		t.Fatalf("stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
	}
	if result.SkippedJudgeWorkspace != 1 {
		t.Errorf("result.SkippedJudgeWorkspace = %d, want 1", result.SkippedJudgeWorkspace)
	}
}

func TestIngest_JSONOutput(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	projA := filepath.Join(tmp, "proj-a")
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeHome, "projects", "proj-a-slug", "55555555-5555-5555-5555-555555555555.jsonl"),
		"55555555-5555-5555-5555-555555555555", projA, base)
	writeTestConfig(t, configPath, fakeHome, "")

	root := NewRootCommand("test")
	root.AddCommand(newIngestCommand())
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "ingest", "--all", "--no-evidence"})

	if err := root.Execute(); err != nil {
		t.Fatalf("ingest --json error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
	}

	var result ingestResult
	if err := json.Unmarshal(outBuf.Bytes(), &result); err != nil {
		t.Fatalf("stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
	}
	if result.Ingested != 1 {
		t.Errorf("result.Ingested = %d, want 1", result.Ingested)
	}
	if result.Discovered != 1 {
		t.Errorf("result.Discovered = %d, want 1", result.Discovered)
	}
	if result.EstimatedCostUSD <= 0 {
		t.Errorf("result.EstimatedCostUSD = %f, want > 0（claude-sonnet-5 は単価が既知）", result.EstimatedCostUSD)
	}
	if len(result.UnknownModels) != 0 {
		t.Errorf("result.UnknownModels = %v, want 空", result.UnknownModels)
	}

	// --json 指定時は標準出力が JSON のみであること（進捗ログ等が混ざっていない）。
	if errBuf.Len() == 0 {
		t.Error("stderr が空: 進捗ログが標準エラーに出ていない可能性がある")
	}
}

// writeCodexRollout は Parse を通る最小限の Codex ロールアウトを書き出す。
func writeCodexRollout(t *testing.T, codexHome, sessionID, cwd string, ts time.Time) {
	t.Helper()

	dir := filepath.Join(codexHome, "sessions", ts.Format("2006"), ts.Format("01"), ts.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "rollout-"+ts.Format("2006-01-02T15-04-05")+"-"+sessionID+".jsonl")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer f.Close()

	writeJSONLine(t, f, map[string]any{
		"timestamp": ts.Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":          sessionID,
			"session_id":  sessionID,
			"timestamp":   ts.Format(time.RFC3339Nano),
			"cwd":         cwd,
			"originator":  "codex_cli_rs",
			"cli_version": "0.128.0",
			"source":      "cli",
			"git":         map[string]any{"branch": "main"},
		},
	})
	writeJSONLine(t, f, map[string]any{
		"timestamp": ts.Add(time.Second).Format(time.RFC3339Nano),
		"type":      "turn_context",
		"payload":   map[string]any{"cwd": cwd, "model": "gpt-5.5", "effort": "high"},
	})
	writeJSONLine(t, f, map[string]any{
		"timestamp": ts.Add(2 * time.Second).Format(time.RFC3339Nano),
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": "テストプロンプト"}},
		},
	})
	writeJSONLine(t, f, map[string]any{
		"timestamp": ts.Add(3 * time.Second).Format(time.RFC3339Nano),
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "応答テキスト"}},
		},
	})
	writeJSONLine(t, f, map[string]any{
		"timestamp": ts.Add(4 * time.Second).Format(time.RFC3339Nano),
		"type":      "token_usage_record",
		"payload": map[string]any{
			"usage": map[string]any{
				"input_tokens": 100, "cached_input_tokens": 10,
				"cache_write_input_tokens": 5, "output_tokens": 50,
				"reasoning_output_tokens": 20,
			},
		},
	})
}

// TestIngest_CodexSource は Codex のロールアウトがコマンド層を通して取り込まれ、
// Claude Code のセッションと同じ DB に並ぶことを確かめる。
// パッケージ単体では通っても、設定の解釈やソースの組み立てという継ぎ目で
// 落ちることが多いため、ここはコマンドを実際に走らせて見る。
func TestIngest_CodexSource(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	fakeCodexHome := filepath.Join(tmp, "codex")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	proj := filepath.Join(tmp, "proj-a")

	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeClaudeHome, "projects", "proj-a-slug", "11111111-1111-1111-1111-111111111111.jsonl"),
		"11111111-1111-1111-1111-111111111111", proj, base)
	writeCodexRollout(t, fakeCodexHome, "99999999-9999-9999-9999-999999999999", proj, base.Add(time.Hour))

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	cfg.Sources.Codex.Root = fakeCodexHome
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence")
	if err != nil {
		t.Fatalf("ingest error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	ids := allSessionIDs(t, dbPath)
	if len(ids) != 2 {
		t.Fatalf("取り込まれたセッション数 = %d, want 2（claude-code と codex が 1 件ずつ）: %v", len(ids), ids)
	}

	d, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer d.Close()

	rows, err := d.SessionsInRange(time.Time{}, time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SessionsInRange: %v", err)
	}
	var codexRow *store.SessionRow
	for i := range rows {
		if rows[i].Source == "codex" {
			codexRow = &rows[i]
		}
	}
	if codexRow == nil {
		t.Fatalf("source=codex のセッションが DB にありません: %+v", rows)
	}
	if codexRow.SessionID != "99999999-9999-9999-9999-999999999999" {
		t.Errorf("SessionID = %q", codexRow.SessionID)
	}
	if codexRow.ProjectPath != proj {
		t.Errorf("ProjectPath = %q, want %q", codexRow.ProjectPath, proj)
	}
	if codexRow.FirstPrompt != "テストプロンプト" {
		t.Errorf("FirstPrompt = %q", codexRow.FirstPrompt)
	}
}

// TestIngest_MissingCodexRootIsSkipped は、Codex を使っていない環境（sessions/ が無い）
// でも ingest が成功することを確かめる。codex ソースは既定で有効なので、ここで
// 失敗すると Claude Code しか使っていない利用者が ingest できなくなる。
func TestIngest_MissingCodexRootIsSkipped(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	proj := filepath.Join(tmp, "proj-a")

	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeClaudeHome, "projects", "proj-a-slug", "11111111-1111-1111-1111-111111111111.jsonl"),
		"11111111-1111-1111-1111-111111111111", proj, base)

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	cfg.Sources.Codex.Root = filepath.Join(tmp, "no-such-codex-home")
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence")
	if err != nil {
		t.Fatalf("ingest error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "ログ置き場が見つからないソースを飛ばしました") {
		t.Errorf("飛ばしたソースの説明が stderr にありません:\n%s", stderr)
	}
	if ids := allSessionIDs(t, dbPath); len(ids) != 1 {
		t.Fatalf("取り込まれたセッション数 = %d, want 1: %v", len(ids), ids)
	}
}

// TestIngest_NewlyAvailableSourceBackfillsPastLogs は、claude-code だけを継続的に
// ingest してきた環境で codex を初めて使い始めたケースを再現する。
//
// 増分取り込みの基準時刻は全ソース共通で「最後に何かを取り込んだ時刻（ingest_state の
// MAX(ingested_at)）」から決まる。1 回目の実行（claude-code のみ）でこの時刻が進んだ後、
// 2 回目の実行までに codex のログ置き場が現れても、そのロールアウトの mtime は
// 1 回目の基準時刻より古い（Codex を使い始めたのは insights を使い始めるより前、という
// よくある順序）。ソース単位で「まだ一度も取り込んでいない」を見て基準時刻をゼロ値に
// 戻さないと、この codex ログは Discover の時点で黙って除外され、発見数にすら
// 現れないまま取りこぼされる。
func TestIngest_NewlyAvailableSourceBackfillsPastLogs(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	fakeCodexHome := filepath.Join(tmp, "codex")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	proj := filepath.Join(tmp, "proj-a")

	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	writeValidSession(t, filepath.Join(fakeClaudeHome, "projects", "proj-a-slug", "11111111-1111-1111-1111-111111111111.jsonl"),
		"11111111-1111-1111-1111-111111111111", proj, base)

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	cfg.Sources.Codex.Root = fakeCodexHome // まだ sessions/ を作っていないので Available() が false
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	// 1 回目: claude-code だけが ingest され、ingest_state の最終取り込み時刻が
	// 「現在時刻」に進む。この時点では codex のログ置き場自体が存在しない。
	if _, stderr, err := runIngestCLI(t, configPath, dbPath, "--no-evidence"); err != nil {
		t.Fatalf("1回目の ingest error = %v\nstderr=%s", err, stderr)
	}
	if ids := allSessionIDs(t, dbPath); len(ids) != 1 {
		t.Fatalf("1回目取り込み後のセッション数 = %d, want 1: %v", len(ids), ids)
	}

	// ここで初めて Codex を使い始めたことにする。ロールアウトの mtime は
	// 1 回目の取り込み時刻より明確に過去にする（Chtimes しないと「今作った」扱いに
	// なってしまい、再現したいずれ違いが出ない）。
	codexTS := base.Add(time.Hour)
	writeCodexRollout(t, fakeCodexHome, "99999999-9999-9999-9999-999999999999", proj, codexTS)
	rolloutPath := filepath.Join(fakeCodexHome, "sessions", codexTS.Format("2006"), codexTS.Format("01"), codexTS.Format("02"),
		"rollout-"+codexTS.Format("2006-01-02T15-04-05")+"-99999999-9999-9999-9999-999999999999.jsonl")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(rolloutPath, past, past); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	// 2 回目: --since/--all を付けない、素の増分実行。codex はここで初めて
	// Available() になるが、ingest_state に記録が無いので全件が対象になってほしい。
	stdout, stderr, err := runIngestCLI(t, configPath, dbPath, "--no-evidence")
	if err != nil {
		t.Fatalf("2回目の ingest error = %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	ids := allSessionIDs(t, dbPath)
	if len(ids) != 2 {
		t.Fatalf("2回目取り込み後のセッション数 = %d, want 2（codex の過去ログが取りこぼされています）: %v\nstdout=%s\nstderr=%s",
			len(ids), ids, stdout, stderr)
	}

	d, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer d.Close()
	rows, err := d.SessionsInRange(time.Time{}, time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SessionsInRange: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Source == "codex" && r.SessionID == "99999999-9999-9999-9999-999999999999" {
			found = true
		}
	}
	if !found {
		t.Fatalf("source=codex のセッションが DB にありません: %+v", rows)
	}
}

// TestIngest_AllSourcesMissingIsError は、有効なソースのログ置き場がどれも
// 見つからないときはエラーにすることを確かめる。全部飛ばして「0 件取り込みました」で
// 成功すると、設定ミスに気付けない。
func TestIngest_AllSourcesMissingIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = filepath.Join(tmp, "no-such-claude-home")
	cfg.Sources.Codex.Root = filepath.Join(tmp, "no-such-codex-home")
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	if _, _, err := runIngestCLI(t, configPath, dbPath, "--all", "--no-evidence"); err == nil {
		t.Fatal("ingest = nil, want error（ログ置き場がどれも無い）")
	}
}
