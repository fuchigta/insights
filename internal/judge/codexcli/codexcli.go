// Package codexcli は judge.Judge を "codex exec" サブプロセスで実装する。
//
// claudecli との違いは大きく 3 つある。
//
//  1. 構造化出力の渡し方。claude は --json-schema に JSON を直接渡すが、codex は
//     --output-schema にスキーマ「ファイルのパス」を渡す（codex-rs/exec の
//     load_output_schema）。そのため一時ファイル経由で渡す。
//  2. system prompt に相当するフラグが無い。codex exec は役割指示を別枠で受け取れない
//     ため、req.System を本文の先頭に連結して渡す。
//  3. 1 回あたりの支出上限（claude の --max-budget-usd 相当）が無い。暴走時の歯止めは
//     タイムアウトと実行件数（--limit）だけになる。docs/cost.md にこの差を書いている。
package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuchigta/insights/internal/judge"
)

// defaultTimeout は Options.Timeout 未指定時の既定値。
// codex は推論に時間を掛けるモデルが既定なので、claudecli より長めに取る。
const defaultTimeout = 300 * time.Second

// defaultBin は Options.BinPath 未指定時に PATH から解決するコマンド名。
const defaultBin = "codex"

// maxSchemaAttempts / maxTransientAttempts の意味は claudecli と同じ。
// スキーマ不一致の再試行と、一時的失敗（起動失敗・タイムアウト・レート制限）の
// 再試行を別枠にしている。
const (
	maxSchemaAttempts    = 2
	maxTransientAttempts = 3
)

// 番兵エラーは judge パッケージのものをそのまま使う（別名なので errors.Is の
// 判定結果は claudecli と完全に一致する）。
var (
	ErrRateLimited    = judge.ErrRateLimited
	ErrTimeout        = judge.ErrTimeout
	ErrSchemaMismatch = judge.ErrSchemaMismatch
)

// Options は Judge の構成。
type Options struct {
	// Model は空ならフラグを渡さず codex の既定モデルを使う。
	Model string
	// Timeout は 1 回の codex 実行あたりの上限。既定 300s。
	//
	// codex exec には支出上限のフラグが無いため、このタイムアウトが
	// 「1 回の暴走をどこで止めるか」の唯一の歯止めになる。
	Timeout time.Duration
	// WorkDir は codex を実行する作業ディレクトリ。空なら
	// os.UserHomeDir()/.insights/judge-workspace を都度作成して使う。
	WorkDir string
	// BinPath は codex 実行ファイルのパス。空なら "codex" を PATH から解決する。
	// テストでダミー実行ファイルに差し替えるためのフック。
	BinPath string
}

// RunInfo は 1 回の codex 実行のメタ情報。実体は judge.RunInfo。
type RunInfo = judge.RunInfo

// Judge は judge.Judge / judge.Runner を codex exec サブプロセスで実装する。
// runs の扱い（追記ログ + EvaluateRun での 1:1 対応）は claudecli と同じ設計。
type Judge struct {
	opts Options

	mu   sync.Mutex
	runs []RunInfo
}

// judge.Runner（＝ judge.Judge）を満たすことをコンパイル時に確認する。
var _ judge.Runner = (*Judge)(nil)

// New は Options を既定値で補って Judge を作る。
func New(opts Options) *Judge {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.BinPath == "" {
		opts.BinPath = defaultBin
	}
	return &Judge{opts: opts}
}

// Name は judge.Judge の実装。
func (j *Judge) Name() string { return "codex-cli" }

// Available は codex 実行ファイルが見つかるかを返す。doctor が使う。
func (j *Judge) Available() error {
	bin := j.opts.BinPath
	if bin == "" {
		bin = defaultBin
	}
	if strings.ContainsAny(bin, `/\`) {
		if _, err := os.Stat(bin); err != nil {
			return fmt.Errorf("codex 実行ファイルが見つかりません (%s): %w", bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("codex が PATH に見つかりません: %w", err)
	}
	return nil
}

// Evaluate は judge.Judge の実装。
func (j *Judge) Evaluate(ctx context.Context, req judge.Request) (json.RawMessage, error) {
	out, _, err := j.EvaluateRun(ctx, req)
	return out, err
}

// EvaluateRun は Evaluate と同じ評価を行い、加えてこの呼び出し自身が発生させた
// 最後の codex 実行の RunInfo を直接返す（judge.Runner の実装）。
func (j *Judge) EvaluateRun(ctx context.Context, req judge.Request) (json.RawMessage, RunInfo, error) {
	workDir, err := j.resolveWorkDir()
	if err != nil {
		return nil, RunInfo{}, err
	}

	prompt := buildPrompt(req)
	required := judge.RequiredFields(req.Schema)

	// リクエストにモデル指定が無ければ構成のモデルを使う。どちらも空なら
	// フラグを渡さず codex の既定モデルに委ねる。
	model := req.Model
	if model == "" {
		model = j.opts.Model
	}

	var lastRun RunInfo
	var lastErr error

	for attempt := 0; attempt < maxSchemaAttempts; attempt++ {
		attemptPrompt := prompt
		if attempt > 0 {
			attemptPrompt = prompt + buildRetryNote(lastErr)
		}

		out, err := j.runWithBackoff(ctx, workDir, attemptPrompt, model, req.Schema)
		if err != nil {
			// プロセスレベルの失敗。スキーマ不一致の再試行対象ではないのでそのまま返す。
			return nil, lastRun, err
		}

		run := RunInfo{
			SessionID: out.ThreadID,
			// codex exec は費用を報告しない（ChatGPT プランでの利用が主で、
			// USD 建ての実費を CLI が知らないため）。トークン数から金額を推定するには
			// 単価表が要るが、それは cli 層の責務なのでここでは 0 のままにする。
			DurationMS: out.DurationMS,
			NumTurns:   out.Turns,
			Model:      model,
		}
		j.recordRun(run)
		lastRun = run

		if out.ErrorMessage != "" {
			lastErr = fmt.Errorf("codex がエラーを報告しました: %s", judge.TruncateForError(out.ErrorMessage))
			continue
		}
		if strings.TrimSpace(out.AgentMessage) == "" {
			lastErr = fmt.Errorf("codex が最終メッセージを返しませんでした")
			continue
		}

		// --output-schema を渡すと agent_message の本文が JSON そのものになるが、
		// モデルがコードフェンスや前置きを付ける逸脱に備えて抽出を通す
		// （素の JSON ならそのまま返るので、正常時の挙動は変わらない）。
		extracted, exErr := judge.ExtractJSON(out.AgentMessage)
		if exErr != nil {
			lastErr = fmt.Errorf("応答から JSON を抽出できませんでした: %w", exErr)
			continue
		}
		if err := judge.ValidateRequired(extracted, required); err != nil {
			lastErr = err
			continue
		}

		return extracted, run, nil
	}

	return nil, lastRun, fmt.Errorf("%d 回試行しましたが%w: %w", maxSchemaAttempts, ErrSchemaMismatch, lastErr)
}

// buildPrompt は役割指示（req.System）と本文を 1 つのプロンプトに連結する。
// codex exec には system prompt 相当のフラグが無いため、この形でしか渡せない。
func buildPrompt(req judge.Request) string {
	system := strings.TrimSpace(req.System)
	if system == "" {
		return req.Prompt
	}
	return system + "\n\n---\n\n" + req.Prompt
}

func buildRetryNote(prevErr error) string {
	reason := "前回の出力が JSON Schema に適合しませんでした。"
	if prevErr != nil {
		reason = fmt.Sprintf("前回の出力が JSON Schema に適合しませんでした（%s）。", prevErr.Error())
	}
	return "\n\n---\n" + reason + "説明文やコードフェンスを含めず、指定された JSON Schema に従う JSON オブジェクトのみを出力してください。"
}

func (j *Judge) recordRun(r RunInfo) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runs = append(j.runs, r)
}

// LastRun は直近に記録された codex 実行の RunInfo を返す（未実行ならゼロ値）。
// 並行実行下では「自分が呼んだ Evaluate の結果」とは限らないため、
// 1:1 対応が必要な場合は EvaluateRun を使うこと。
func (j *Judge) LastRun() RunInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.runs) == 0 {
		return RunInfo{}
	}
	return j.runs[len(j.runs)-1]
}

// Runs は記録済みの全 RunInfo のスナップショット（コピー）を返す。
func (j *Judge) Runs() []RunInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]RunInfo, len(j.runs))
	copy(out, j.runs)
	return out
}

// resolveWorkDir は codex を実行する作業ディレクトリを決め、存在しなければ作成する。
// 評価対象プロジェクトのカレントディレクトリで実行すると、そのリポジトリの AGENTS.md や
// プロジェクトスコープの設定（<project>/.codex）を読み込んでノイズと余計なコストが乗るため、
// claudecli と同じく専用の作業ディレクトリを必ず使う。
func (j *Judge) resolveWorkDir() (string, error) {
	dir := j.opts.WorkDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
		}
		dir = filepath.Join(home, ".insights", "judge-workspace")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("judge の作業ディレクトリの作成に失敗しました (%s): %w", dir, err)
	}
	return dir, nil
}

// runWithBackoff は runOnce を一時的失敗に対して指数バックオフで最大
// maxTransientAttempts 回まで試みる。
func (j *Judge) runWithBackoff(ctx context.Context, workDir, prompt, model string, schema json.RawMessage) (*execOutput, error) {
	var lastErr error
	for attempt := 0; attempt < maxTransientAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second // 1s, 2s, ...
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, fmt.Errorf("codex 実行のバックオフ待機中に context が終了しました: %w", ctx.Err())
			}
		}

		out, transient, err := j.runOnce(ctx, workDir, prompt, model, schema)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !transient {
			return nil, err
		}
	}
	return nil, fmt.Errorf("codex CLI の実行が %d 回とも一時的エラーで失敗しました: %w", maxTransientAttempts, lastErr)
}

// runOnce は codex exec を 1 回実行する。第 2 戻り値は、その失敗が一時的なもの
// （リトライして意味がある）かどうか。
func (j *Judge) runOnce(ctx context.Context, workDir, prompt, model string, schema json.RawMessage) (*execOutput, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, j.opts.Timeout)
	defer cancel()

	args := []string{
		"exec",
		// イベントを JSONL で受け取る。最終メッセージとトークン使用量をここから読む。
		"--json",
		// 評価は読むだけで足りる。書き込み・ネットワークを伴う操作を封じる。
		"--sandbox", "read-only",
		// 作業ディレクトリは git リポジトリではないので、リポジトリ必須の確認を外す。
		"--skip-git-repo-check",
		// セッションファイルを残さない。残すと「評価の実行そのもの」が次回の
		// ingest で集計対象になり、自分の評価が自分の統計を汚し続ける。
		"--ephemeral",
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	if len(schema) > 0 {
		// codex は --output-schema にスキーマ「ファイルのパス」を取る。
		// 一時ファイルは作業ディレクトリ配下に作り、実行後に必ず消す。
		schemaPath, cleanup, err := writeTempFile(workDir, "insights-judge-schema-*.json", schema)
		if err != nil {
			return nil, false, err
		}
		defer cleanup()
		args = append(args, "--output-schema", schemaPath)
	}

	// プロンプトは argv ではなく stdin で渡す。セッション台本は会話全文を含み
	// 数十KB〜数百KBになりうるため、Windows の argv 長上限（約32KB）に触れる。
	// codex exec はプロンプト位置引数に "-" を渡すと標準入力から読む。
	args = append(args, "-")

	cmd := exec.CommandContext(runCtx, j.opts.BinPath, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	// 失敗時でも stdout に出たイベントは読む（error イベントに理由が入るため）。
	out := parseExecEvents(stdout.Bytes())
	out.DurationMS = elapsed.Milliseconds()

	if runErr != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, true, fmt.Errorf("%w (%s): %w", ErrTimeout, j.opts.Timeout, runErr)
		}
		combined := stdout.String() + "\n" + stderr.String()
		if judge.LooksRateLimited(combined) {
			return nil, true, fmt.Errorf("%w: %w (stderr=%s)", ErrRateLimited, runErr, judge.TruncateForError(stderr.String()))
		}
		detail := strings.TrimSpace(out.ErrorMessage)
		if detail == "" {
			detail = stderr.String()
		}
		// プロセス起動失敗も含め一時的失敗として扱う（claudecli と同じ方針）。
		return nil, true, fmt.Errorf("codex の実行に失敗しました: %w (%s)", runErr, judge.TruncateForError(detail))
	}

	// 終了コードが 0 でも、レート制限をイベントとして報告して終わる経路がありうる。
	if out.ErrorMessage != "" && judge.LooksRateLimited(out.ErrorMessage) {
		return nil, true, fmt.Errorf("%w: %s", ErrRateLimited, judge.TruncateForError(out.ErrorMessage))
	}

	return out, false, nil
}

// writeTempFile は dir 配下に一時ファイルを作って data を書き、パスと後始末関数を返す。
func writeTempFile(dir, pattern string, data []byte) (string, func(), error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, fmt.Errorf("一時ファイルの作成に失敗しました: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("一時ファイルへの書き込みに失敗しました (%s): %w", path, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("一時ファイルのクローズに失敗しました (%s): %w", path, err)
	}
	return path, cleanup, nil
}

// execOutput は `codex exec --json` のイベント列から本実装が使う情報だけを集約したもの。
type execOutput struct {
	ThreadID     string
	AgentMessage string // 最後の agent_message の本文（構造化出力なら JSON 文字列）
	ErrorMessage string
	Turns        int
	Usage        execUsage
	DurationMS   int64
}

// execUsage は turn.completed イベントが報告するトークン使用量。
// codex-rs/exec の Usage と 1:1 で対応する。
type execUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// execEvent は `codex exec --json` の 1 行。使うフィールドだけを拾う。
// イベント型は codex-rs/exec/src/exec_events.rs の ThreadEvent に対応する。
type execEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Message  string          `json:"message"`
	Usage    *execUsage      `json:"usage"`
	Item     *execThreadItem `json:"item"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// execThreadItem は item.completed の item。agent_message だけを見る。
type execThreadItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// maxEventLineBytes は 1 イベント行の最大長。構造化出力の本文がそのまま
// agent_message に載るため、既定の bufio 上限では足りないことがある。
const maxEventLineBytes = 8 * 1024 * 1024

// parseExecEvents は JSONL のイベント列を走査して execOutput を組み立てる。
// 壊れた行はスキップする（イベント列の途中に非 JSON の出力が混ざっても、
// 最終メッセージが取れていれば評価は成立するため）。
func parseExecEvents(raw []byte) *execOutput {
	out := &execOutput{}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLineBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev execEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "thread.started":
			out.ThreadID = ev.ThreadID
		case "turn.completed":
			out.Turns++
			if ev.Usage != nil {
				out.Usage = *ev.Usage
			}
		case "turn.failed":
			if ev.Error != nil && ev.Error.Message != "" {
				out.ErrorMessage = ev.Error.Message
			}
		case "error":
			if ev.Message != "" {
				out.ErrorMessage = ev.Message
			}
		case "item.completed":
			// 最後の agent_message が最終回答。途中経過（reasoning など）は使わない。
			if ev.Item != nil && ev.Item.Type == "agent_message" {
				out.AgentMessage = ev.Item.Text
			}
		}
	}

	return out
}
