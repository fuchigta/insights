package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/judge"
)

// TestMain は、この go test バイナリ自身を偽の codex 実行ファイルとしても再利用する
// ためのフック（claudecli のテストと同じ仕組み）。INSIGHTS_TEST_FAKE_CODEX が
// 設定されていれば m.Run を呼ばずに偽の応答だけを返すので、argv が go test の
// フラグパーサーに渡らない。実際のプロセス起動を通した検証を全 OS で行える。
func TestMain(m *testing.M) {
	if mode := os.Getenv("INSIGHTS_TEST_FAKE_CODEX"); mode != "" {
		runFakeCodexProcess(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeCodexProcess(mode string) {
	switch mode {
	case "capture":
		// argv と --output-schema が指すファイルの中身、stdin で渡されたプロンプトを
		// 書き出してから、構造化出力らしい JSONL イベント列を返す。
		if path := os.Getenv("INSIGHTS_TEST_ARGV_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(strings.Join(os.Args, "\n")), 0o644)
		}
		if path := os.Getenv("INSIGHTS_TEST_SCHEMA_FILE"); path != "" {
			var schemaPath string
			for i, a := range os.Args {
				if a == "--output-schema" && i+1 < len(os.Args) {
					schemaPath = os.Args[i+1]
				}
			}
			body, _ := os.ReadFile(schemaPath)
			_ = os.WriteFile(path, body, 0o644)
		}
		if path := os.Getenv("INSIGHTS_TEST_PROMPT_FILE"); path != "" {
			body := make([]byte, 0, 4096)
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				body = append(body, buf[:n]...)
				if err != nil {
					break
				}
			}
			_ = os.WriteFile(path, body, 0o644)
		}
		fmt.Println(`{"type":"thread.started","thread_id":"thread-1"}`)
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item-1","type":"reasoning","text":"考えた"}}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"{\"ok\":\"yes\"}"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"cache_write_input_tokens":5,"output_tokens":30,"reasoning_output_tokens":10}}`)
		os.Exit(0)
	case "ratelimit":
		fmt.Fprintln(os.Stderr, "stream error: rate limit exceeded (429)")
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "runFakeCodexProcess: unknown mode "+mode)
		os.Exit(2)
	}
}

func TestName(t *testing.T) {
	if got := New(Options{}).Name(); got != "codex-cli" {
		t.Errorf("Name() = %q, want codex-cli", got)
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
}

func TestAvailable(t *testing.T) {
	t.Run("存在しないパス", func(t *testing.T) {
		j := New(Options{BinPath: filepath.Join(t.TempDir(), "does-not-exist-codex.exe")})
		if err := j.Available(); err == nil {
			t.Fatal("Available() = nil, want error")
		}
	})

	t.Run("実在するファイルパス", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "fake-codex")
		if err := os.WriteFile(f, []byte("dummy"), 0o755); err != nil {
			t.Fatalf("テスト用ファイルの作成に失敗: %v", err)
		}
		if err := New(Options{BinPath: f}).Available(); err != nil {
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

// TestBuildPrompt は、system prompt 相当のフラグを持たない codex exec 向けに
// 役割指示を本文の先頭へ連結することを確かめる。
func TestBuildPrompt(t *testing.T) {
	got := buildPrompt(judge.Request{System: "  あなたは評価者です  ", Prompt: "台本"})
	if !strings.HasPrefix(got, "あなたは評価者です") {
		t.Errorf("buildPrompt() = %q, want 役割指示が先頭", got)
	}
	if !strings.HasSuffix(got, "台本") {
		t.Errorf("buildPrompt() = %q, want 本文が末尾", got)
	}

	if got := buildPrompt(judge.Request{Prompt: "台本のみ"}); got != "台本のみ" {
		t.Errorf("buildPrompt() = %q, want 台本のみ", got)
	}
}

// TestParseExecEvents は codex exec --json のイベント列から必要な情報だけを
// 拾えることを確かめる。最終メッセージは最後の agent_message で、途中の
// reasoning は使わない。
func TestParseExecEvents(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"thread.started","thread_id":"th-9"}`,
		`not json`,
		`{"type":"item.completed","item":{"id":"i1","type":"reasoning","text":"考えた"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"最初の答え"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"最後の答え"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3,"reasoning_output_tokens":1}}`,
	}, "\n")

	out := parseExecEvents([]byte(raw))
	if out.ThreadID != "th-9" {
		t.Errorf("ThreadID = %q, want th-9", out.ThreadID)
	}
	if out.AgentMessage != "最後の答え" {
		t.Errorf("AgentMessage = %q, want 最後の答え", out.AgentMessage)
	}
	if out.Turns != 1 {
		t.Errorf("Turns = %d, want 1", out.Turns)
	}
	if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 3 {
		t.Errorf("Usage = %+v", out.Usage)
	}
	if out.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want 空", out.ErrorMessage)
	}
}

func TestParseExecEvents_Errors(t *testing.T) {
	out := parseExecEvents([]byte(`{"type":"error","message":"boom"}`))
	if out.ErrorMessage != "boom" {
		t.Errorf("ErrorMessage = %q, want boom", out.ErrorMessage)
	}

	out = parseExecEvents([]byte(`{"type":"turn.failed","error":{"message":"turn broke"}}`))
	if out.ErrorMessage != "turn broke" {
		t.Errorf("ErrorMessage = %q, want turn broke", out.ErrorMessage)
	}
}

// TestEvaluate_PassesSchemaAsFile は、スキーマがファイル経由で渡ること
// （codex は --output-schema にパスを取る）と、評価に不要な副作用を封じる
// フラグが必ず付くことを、実際のプロセス起動を通して確かめる。
func TestEvaluate_PassesSchemaAsFile(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	schemaFile := filepath.Join(dir, "schema.json")
	promptFile := filepath.Join(dir, "prompt.txt")

	t.Setenv("INSIGHTS_TEST_FAKE_CODEX", "capture")
	t.Setenv("INSIGHTS_TEST_ARGV_FILE", argvFile)
	t.Setenv("INSIGHTS_TEST_SCHEMA_FILE", schemaFile)
	t.Setenv("INSIGHTS_TEST_PROMPT_FILE", promptFile)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}

	j := New(Options{BinPath: self, WorkDir: dir, Timeout: 30 * time.Second, Model: "gpt-5.5"})
	schema := json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"string"}}}`)

	out, run, err := j.EvaluateRun(context.Background(), judge.Request{
		System: "評価者として振る舞う",
		Prompt: "セッション台本",
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("EvaluateRun() error = %v", err)
	}
	if string(out) != `{"ok":"yes"}` {
		t.Errorf("出力 = %s, want {\"ok\":\"yes\"}", out)
	}
	if run.SessionID != "thread-1" {
		t.Errorf("RunInfo.SessionID = %q, want thread-1", run.SessionID)
	}
	if run.CostUSD != 0 {
		t.Errorf("RunInfo.CostUSD = %v, want 0（codex は実費を報告しない）", run.CostUSD)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("argv の読み取りに失敗: %v", err)
	}
	argv := string(argvBytes)
	for _, want := range []string{"exec", "--json", "--sandbox\nread-only", "--skip-git-repo-check", "--ephemeral", "--model\ngpt-5.5", "--output-schema"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv に %q が含まれていません:\n%s", want, argv)
		}
	}
	// スキーマ本体は argv ではなくファイルで渡す。
	if strings.Contains(argv, `"required"`) {
		t.Errorf("スキーマ本体が argv に載っています:\n%s", argv)
	}

	schemaBody, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("スキーマファイルの読み取りに失敗: %v", err)
	}
	if !json.Valid(schemaBody) || !strings.Contains(string(schemaBody), `"required"`) {
		t.Errorf("--output-schema の中身が想定と違います: %s", schemaBody)
	}

	// プロンプトは argv ではなく stdin で渡す（台本は数十KBになりうるため）。
	promptBody, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("プロンプトの読み取りに失敗: %v", err)
	}
	if !strings.Contains(string(promptBody), "セッション台本") || !strings.Contains(string(promptBody), "評価者として振る舞う") {
		t.Errorf("stdin に渡ったプロンプトが想定と違います: %s", promptBody)
	}
}

// TestEvaluate_SchemaTempFileIsRemoved は、スキーマの一時ファイルを残さないことを
// 確かめる。作業ディレクトリに溜まると、次に何が本物か分からなくなる。
func TestEvaluate_SchemaTempFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INSIGHTS_TEST_FAKE_CODEX", "capture")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	j := New(Options{BinPath: self, WorkDir: dir, Timeout: 30 * time.Second})
	if _, _, err := j.EvaluateRun(context.Background(), judge.Request{
		Prompt: "台本",
		Schema: json.RawMessage(`{"type":"object","required":["ok"]}`),
	}); err != nil {
		t.Fatalf("EvaluateRun() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "insights-judge-schema-") {
			t.Errorf("スキーマの一時ファイルが残っています: %s", e.Name())
		}
	}
}

// TestEvaluate_RateLimitedWrapsSentinel は、レート制限らしき失敗が
// judge.ErrRateLimited で識別できることを確かめる。呼び出し側はこれを見て
// 残りのセッションの評価を打ち切る。
func TestEvaluate_RateLimitedWrapsSentinel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INSIGHTS_TEST_FAKE_CODEX", "ratelimit")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	j := New(Options{BinPath: self, WorkDir: dir, Timeout: 30 * time.Second})

	_, _, evalErr := j.EvaluateRun(context.Background(), judge.Request{Prompt: "台本"})
	if evalErr == nil {
		t.Fatal("EvaluateRun() = nil, want error")
	}
	if !errors.Is(evalErr, judge.ErrRateLimited) {
		t.Errorf("error = %v, want judge.ErrRateLimited でラップされていること", evalErr)
	}
}

func TestLastRunAndRuns(t *testing.T) {
	j := New(Options{})
	if got := j.LastRun(); got != (RunInfo{}) {
		t.Errorf("LastRun() 初期状態 = %+v, want zero value", got)
	}
	if got := j.Runs(); len(got) != 0 {
		t.Errorf("Runs() 初期状態 = %+v, want 空", got)
	}

	j.recordRun(RunInfo{SessionID: "a"})
	j.recordRun(RunInfo{SessionID: "b"})
	if got := j.LastRun().SessionID; got != "b" {
		t.Errorf("LastRun().SessionID = %q, want b", got)
	}
	if got := j.Runs(); len(got) != 2 {
		t.Errorf("len(Runs()) = %d, want 2", len(got))
	}
}
