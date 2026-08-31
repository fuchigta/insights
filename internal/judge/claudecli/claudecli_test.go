package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/judge"
)

// TestMain は、この go test バイナリ自身を偽の claude 実行ファイルとしても再利用できるように
// するためのフック。INSIGHTS_TEST_FAKE_CLAUDE が設定されていれば、通常のテスト実行（m.Run）
// を一切行わず、代わりに runOnce が組み立てる argv（-p --output-format json ...）をそのまま
// 受け取って偽の応答だけを返す。argv の中身は go test 自身のフラグパーサーに渡らない
// （m.Run より前に return するため flag.Parse が走らない）ので、Windows を含む全 OS で
// 実際の claude 相当のプロセス起動・失敗を再現できる。
func TestMain(m *testing.M) {
	if mode := os.Getenv("INSIGHTS_TEST_FAKE_CLAUDE"); mode != "" {
		runFakeClaudeProcess(mode)
		return
	}
	os.Exit(m.Run())
}

// runFakeClaudeProcess は INSIGHTS_TEST_FAKE_CLAUDE の値に応じた偽の claude の振る舞いをして
// 終了する。
func runFakeClaudeProcess(mode string) {
	switch mode {
	case "capture":
		// 受け取った argv と、--system-prompt-file の中身をファイルに書き出してから
		// 正常な応答を返す。スキーマがコマンドラインだけで渡っていることを、
		// 実際のプロセス起動を通して確かめるために使う。
		if path := os.Getenv("INSIGHTS_TEST_ARGV_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(strings.Join(os.Args, "\n")), 0o644)
		}
		if path := os.Getenv("INSIGHTS_TEST_SYSTEM_FILE"); path != "" {
			var sysPath string
			for i, a := range os.Args {
				if a == "--system-prompt-file" && i+1 < len(os.Args) {
					sysPath = os.Args[i+1]
				}
			}
			body, _ := os.ReadFile(sysPath)
			_ = os.WriteFile(path, body, 0o644)
		}
		fmt.Println(`{"result":"","structured_output":{"ok":"yes"},"is_error":false,"session_id":"run-1","total_cost_usd":0.01}`)
		os.Exit(0)
	case "ratelimit":
		// isRateLimitLike が拾う文字列を stderr に出し、非ゼロ終了する。
		fmt.Fprintln(os.Stderr, "Error: rate limit exceeded (429 too many requests)")
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "runFakeClaudeProcess: unknown mode "+mode)
		os.Exit(2)
	}
}

func TestName(t *testing.T) {
	j := New(Options{})
	if got := j.Name(); got != "claude-cli" {
		t.Errorf("Name() = %q, want claude-cli", got)
	}
}

func TestNew_Defaults(t *testing.T) {
	j := New(Options{})
	if j.opts.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", j.opts.Timeout, defaultTimeout)
	}
	if j.opts.BinPath != defaultBin {
		t.Errorf("BinPath = %q, want %q", j.opts.BinPath, defaultBin)
	}

	j2 := New(Options{Timeout: 5 * time.Second, BinPath: "custom-claude"})
	if j2.opts.Timeout != 5*time.Second {
		t.Errorf("Timeout override が効いていない: %v", j2.opts.Timeout)
	}
	if j2.opts.BinPath != "custom-claude" {
		t.Errorf("BinPath override が効いていない: %v", j2.opts.BinPath)
	}
}

func TestAvailable(t *testing.T) {
	t.Run("存在しないパス", func(t *testing.T) {
		j := New(Options{BinPath: filepath.Join(t.TempDir(), "does-not-exist-claude.exe")})
		if err := j.Available(); err == nil {
			t.Fatal("Available() = nil, want error")
		}
	})

	t.Run("実在するファイルパス", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "fake-claude")
		if err := os.WriteFile(f, []byte("dummy"), 0o755); err != nil {
			t.Fatalf("テスト用ファイルの作成に失敗: %v", err)
		}
		j := New(Options{BinPath: f})
		if err := j.Available(); err != nil {
			t.Errorf("Available() = %v, want nil", err)
		}
	})

	t.Run("PATHに存在しないコマンド名", func(t *testing.T) {
		j := New(Options{BinPath: "insights-definitely-not-a-real-command-xyz"})
		if err := j.Available(); err == nil {
			t.Fatal("Available() = nil, want error")
		}
	})
}

func TestLastRunAndRuns_EmptyInitially(t *testing.T) {
	j := New(Options{})
	if got := j.LastRun(); got != (RunInfo{}) {
		t.Errorf("LastRun() 初期状態 = %+v, want zero value", got)
	}
	if got := j.Runs(); len(got) != 0 {
		t.Errorf("Runs() 初期状態 = %+v, want empty", got)
	}
}

func TestRecordRun_AccumulatesAndIsConcurrencySafe(t *testing.T) {
	j := New(Options{})

	const n = 50
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			j.recordRun(RunInfo{SessionID: "s"})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	if got := len(j.Runs()); got != n {
		t.Errorf("Runs() の件数 = %d, want %d", got, n)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	req := judge.Request{
		System: "ROLE-INSTRUCTIONS",
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	got := buildSystemPrompt(req)
	if !strings.Contains(got, "ROLE-INSTRUCTIONS") {
		t.Errorf("system prompt に System が含まれていない: %q", got)
	}
	// スキーマは --json-schema でコマンドラインから渡す（runOnce）。system prompt にも
	// 積むと同じ JSON を 1 回の評価で二重に送ることになるため、ここには含めない。
	if strings.Contains(got, `"type":"object"`) {
		t.Errorf("system prompt に Schema が二重に積まれている: %q", got)
	}

	// System が空でも panic せず、空文字を返す。
	if got2 := buildSystemPrompt(judge.Request{Schema: json.RawMessage(`{}`)}); got2 != "" {
		t.Errorf("System 空のときの system prompt = %q, want 空", got2)
	}
}

// TestEvaluate_RateLimitedOutputWrapsErrRateLimited は、レート制限らしき出力（stderr に
// "rate limit" を含む）で claude 実行が失敗したとき、Evaluate の最終エラーから
// errors.Is(err, ErrRateLimited) で識別できることを確認する回帰テスト。
// runWithBackoff は一時的失敗を maxTransientAttempts(=3) 回リトライしたうえで最後にまとめて
// エラーを返すため、その「最終エラー」経由でも番兵エラーが失われない（%w の連鎖が保たれる）
// ことも合わせて検証する。
//
// maxTransientAttempts=3・バックオフ 1s+2s の設定では、このテスト 1 本で正味 3 秒程度の
// 待ち時間が発生する。値としては許容範囲だが、これ以上ケースを増やすと待ち時間が積み上がる
// ため、あえて 1 ケースに留める（testing.Short での分岐はしない）。
func TestEvaluate_RateLimitedOutputWrapsErrRateLimited(t *testing.T) {
	j := New(Options{
		BinPath: os.Args[0], // このテストバイナリ自身を偽の claude として使う（TestMain 参照）
		Timeout: 10 * time.Second,
		WorkDir: t.TempDir(),
	})

	t.Setenv("INSIGHTS_TEST_FAKE_CLAUDE", "ratelimit")

	start := time.Now()
	_, err := j.Evaluate(context.Background(), judge.Request{Prompt: "test prompt"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Evaluate() error = nil, want error")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("errors.Is(err, ErrRateLimited) = false, want true: %v", err)
	}
	// 3 回の試行 + バックオフ 1s+2s = 3s が目安。プロセス起動オーバーヘッドを見込んでも
	// 明らかに超過していればリトライ回数・バックオフの変更を疑う。
	if elapsed > 15*time.Second {
		t.Errorf("elapsed = %v, リトライ待ちが長すぎる（maxTransientAttempts/バックオフの変更を確認）", elapsed)
	}
}

// スキーマは --json-schema でコマンドラインから渡し、system prompt には積まないこと。
// 両方に積むと同じ JSON を 1 回の評価で二重に送ることになる。ここは buildSystemPrompt の
// 単体テストと違い、実際にプロセスを起動して argv と system prompt ファイルの中身を突き合わせる。
func TestEvaluate_SchemaGoesToArgvNotSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	systemFile := filepath.Join(dir, "system.txt")

	j := New(Options{
		BinPath: os.Args[0], // このテストバイナリ自身を偽の claude として使う（TestMain 参照）
		Timeout: 10 * time.Second,
		WorkDir: t.TempDir(),
	})

	t.Setenv("INSIGHTS_TEST_FAKE_CLAUDE", "capture")
	t.Setenv("INSIGHTS_TEST_ARGV_FILE", argvFile)
	t.Setenv("INSIGHTS_TEST_SYSTEM_FILE", systemFile)

	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"string"}},"required":["ok"],"additionalProperties":false}`)
	if _, err := j.Evaluate(context.Background(), judge.Request{
		System: "ROLE-INSTRUCTIONS",
		Prompt: "test prompt",
		Schema: schema,
	}); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("argv を捕捉できていません: %v", err)
	}
	if !strings.Contains(string(argv), "--json-schema") {
		t.Errorf("argv に --json-schema が無い:\n%s", argv)
	}
	if !strings.Contains(string(argv), `"required":["ok"]`) {
		t.Errorf("argv にスキーマ本体が渡っていない:\n%s", argv)
	}

	sys, err := os.ReadFile(systemFile)
	if err != nil {
		t.Fatalf("system prompt を捕捉できていません: %v", err)
	}
	if !strings.Contains(string(sys), "ROLE-INSTRUCTIONS") {
		t.Errorf("system prompt に役割指示が渡っていない: %q", sys)
	}
	if strings.Contains(string(sys), `"required"`) {
		t.Errorf("system prompt にスキーマが二重に積まれている: %q", sys)
	}
}

// TestEvaluate_RealCLI は実際に claude を起動する結合テスト。
// コストが発生するため、既定では必ずスキップする。実行するには
// INSIGHTS_TEST_REAL_CLI=1 を明示的にセットし、かつ -short を付けないこと。
// さらに claude が PATH に無ければ（CI 環境など）スキップする。
func TestEvaluate_RealCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("short モードのため実 CLI テストをスキップ")
	}
	if os.Getenv("INSIGHTS_TEST_REAL_CLI") != "1" {
		t.Skip("INSIGHTS_TEST_REAL_CLI=1 が設定されていないためスキップ（コストが発生するため既定オフ）")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude が PATH に見つからないためスキップ")
	}

	j := New(Options{Model: "claude-haiku-4-5", Timeout: 60 * time.Second})

	req := judge.Request{
		System: "You are a test responder. Reply with JSON only.",
		Prompt: `Reply with exactly this JSON object and nothing else: {"ok": true, "message": "hello"}`,
		Schema: json.RawMessage(`{"type":"object","required":["ok","message"]}`),
		Model:  "claude-haiku-4-5",
	}

	out, err := j.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("Evaluate() が有効な JSON を返さなかった: %v (%s)", err, out)
	}
	if _, ok := parsed["ok"]; !ok {
		t.Errorf("Evaluate() 結果に ok フィールドがない: %s", out)
	}

	run := j.LastRun()
	if run.SessionID == "" {
		t.Errorf("LastRun().SessionID が空: %+v", run)
	}
	t.Logf("RunInfo: %+v", run)
}
