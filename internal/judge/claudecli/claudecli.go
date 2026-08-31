// Package claudecli は judge.Judge を "claude -p" サブプロセスで実装する。
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuchigta/insights/internal/judge"
)

// defaultTimeout は Options.Timeout 未指定時の既定値。
const defaultTimeout = 180 * time.Second

// defaultBin は Options.BinPath 未指定時に PATH から解決するコマンド名。
const defaultBin = "claude"

// maxSchemaAttempts は 1 回の Evaluate 呼び出しの中で、スキーマ不一致・応答不備
// （is_error や JSON 抽出失敗を含む）に対して許す最大試行回数（初回 + 再試行 1 回 = 2）。
const maxSchemaAttempts = 2

// maxTransientAttempts はプロセス起動失敗・タイムアウト・レート制限らしき出力といった
// 一時的失敗に対する最大試行回数（初回 + 指数バックオフでの再試行 2 回 = 3）。
// スキーマ不一致の再試行（maxSchemaAttempts）とは別枠。
const maxTransientAttempts = 3

// ErrRateLimited はレート制限らしき理由で claude の実行が失敗したことを表す番兵エラー。
//
// レート制限は「このセッションの評価だけが失敗した」のではなく、アカウント全体に効いて
// いる状態を示す。文字列一致ではなく errors.Is で識別できるようにして、呼び出し側が残りの
// セッションの評価を打ち切れるようにしている（制限中に叩き続けても失敗が増えるだけで、
// 課金確認を通した意味も失われる）。
var ErrRateLimited = errors.New("claude の実行がレート制限らしきエラーで失敗しました")

// ErrTimeout / ErrSchemaMismatch も同じ理由で番兵にしている。評価の失敗は種類ごとに
// 意味（利用者が取るべき手当て）が違うため、呼び出し側が errors.Is で仕分けて記録できる
// ようにする。文字列一致で仕分けると、メッセージを直した瞬間に静かに壊れる。
var (
	ErrTimeout        = errors.New("claude の実行がタイムアウトしました")
	ErrSchemaMismatch = errors.New("有効な評価 JSON を得られませんでした")
)

// Options は Judge の構成。
type Options struct {
	// Model は空ならフラグを渡さず claude の既定モデルを使う。
	Model string
	// Timeout は 1 回の claude 実行あたりの上限。既定 180s。
	Timeout time.Duration
	// WorkDir は claude を実行する作業ディレクトリ。空なら
	// os.UserHomeDir()/.insights/judge-workspace を都度作成して使う。
	WorkDir string
	// BinPath は claude 実行ファイルのパス。空なら "claude" を PATH から解決する。
	// テストでダミー実行ファイルに差し替えるためのフック。
	BinPath string
	// MaxBudgetUSD は 1 回の claude 実行に許す API 支出の上限（USD）。
	// claude -p は対話セッションのサブスクリプション枠ではなく API 従量枠を
	// 消費するため、暴走時の被害を抑える安全装置として必ず渡す。
	// 0 以下なら defaultMaxBudgetUSD を使う。
	MaxBudgetUSD float64
}

// RunInfo は 1 回の claude 実行（session）のメタ情報。
// insights 自身の評価コストを別掲したり、評価セッション自体を集計対象から
// 除外したりするために SessionID・CostUSD を使う。
type RunInfo struct {
	SessionID  string
	CostUSD    float64
	DurationMS int64
	NumTurns   int
	Model      string
}

// Judge は judge.Judge を claude -p サブプロセスで実装する。
//
// 並行実行時の RunInfo の設計について:
// judge.Judge.Evaluate は json.RawMessage と error しか返せないため、実行メタ情報
// (session_id・コストなど) を呼び出し元に渡す手段がインターフェース上にない。
// そこで本実装は (1) runs []RunInfo をミューテックスで保護しつつ「実行するたびに
// 追記する」ログとして保持し、Runs() でスナップショットを取れるようにする。
// 加えて (2) 各 Evaluate 呼び出しとその RunInfo を確実に 1:1 対応させたい呼び出し元
// 向けに、EvaluateRun という non-interface な追加メソッドを用意し、その呼び出し
// 自身が発生させた RunInfo を戻り値として直接返す。並行に複数の Evaluate/EvaluateRun
// が走っても、runs への追記は mutex 経由でシリアライズされるだけで、各ゴルーチンの
// スタック上にある戻り値自体は他のゴルーチンと共有されないため競合しない。
// LastRun() は runs の末尾を返す簡易 API だが、並行実行下では「直近に追記された
// もの」が必ずしも「自分が呼んだ Evaluate の結果」とは限らない点に注意（コメントの
// とおり、正確な対応が要る場合は EvaluateRun を使うこと）。
type Judge struct {
	opts Options

	mu   sync.Mutex
	runs []RunInfo
}

// New は Options を既定値で補って Judge を作る。
// defaultMaxBudgetUSD は 1 回の評価に許す API 支出の既定上限。
// 実測では 1 セッションの評価が haiku で $0.025、sonnet でその約 3 倍なので、
// 1 桁以上の余裕を見つつ、暴走した 1 回が枠を食い潰さない値にしている。
const defaultMaxBudgetUSD = 1.0

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
func (j *Judge) Name() string { return "claude-cli" }

// Available は claude 実行ファイルが見つかるかを返す。doctor が使う。
func (j *Judge) Available() error {
	bin := j.opts.BinPath
	if bin == "" {
		bin = defaultBin
	}
	if strings.ContainsAny(bin, `/\`) {
		if _, err := os.Stat(bin); err != nil {
			return fmt.Errorf("claude 実行ファイルが見つかりません (%s): %w", bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("claude が PATH に見つかりません: %w", err)
	}
	return nil
}

// Evaluate は judge.Judge の実装。
func (j *Judge) Evaluate(ctx context.Context, req judge.Request) (json.RawMessage, error) {
	out, _, err := j.EvaluateRun(ctx, req)
	return out, err
}

// EvaluateRun は Evaluate と同じ評価を行い、加えてこの呼び出し自身が発生させた
// 最後の（成功した）claude 実行の RunInfo を直接返す。並行実行下で Evaluate の
// 呼び出しと RunInfo を確実に 1:1 対応させたい呼び出し元向けの追加メソッド。
func (j *Judge) EvaluateRun(ctx context.Context, req judge.Request) (json.RawMessage, RunInfo, error) {
	workDir, err := j.resolveWorkDir()
	if err != nil {
		return nil, RunInfo{}, err
	}

	systemPrompt := buildSystemPrompt(req)
	userPrompt := req.Prompt
	required := requiredFields(req.Schema)

	var lastRun RunInfo
	var lastErr error

	for attempt := 0; attempt < maxSchemaAttempts; attempt++ {
		if attempt > 0 {
			userPrompt = req.Prompt + buildRetryNote(lastErr)
		}

		cliOut, err := j.runWithBackoff(ctx, workDir, systemPrompt, userPrompt, req.Model, req.Schema)
		if err != nil {
			// プロセスレベルの失敗（起動失敗・タイムアウト等）。これはスキーマ不一致の
			// 再試行対象ではないので、有効な RunInfo も残らずそのまま返す。
			return nil, lastRun, err
		}

		run := RunInfo{
			SessionID:  cliOut.SessionID,
			CostUSD:    cliOut.TotalCostUSD,
			DurationMS: cliOut.DurationMS,
			NumTurns:   cliOut.NumTurns,
			Model:      req.Model,
		}
		j.recordRun(run)
		lastRun = run

		if cliOut.IsError {
			lastErr = fmt.Errorf("claude がエラーを報告しました (subtype=%s): %s", cliOut.Subtype, truncateForError(cliOut.Result))
			continue
		}

		// --json-schema を渡した場合は claude が検証済みの構造化出力を
		// structured_output に載せて返す。あればそれをそのまま使い、
		// result テキストからの抽出は行わない（抽出はフォーマット逸脱に弱い）。
		var extracted json.RawMessage
		if len(cliOut.StructuredOutput) > 0 && !bytes.Equal(bytes.TrimSpace(cliOut.StructuredOutput), []byte("null")) {
			extracted = cliOut.StructuredOutput
		} else {
			// 古い claude や --json-schema 非対応の経路のための後方互換。
			var exErr error
			extracted, exErr = ExtractJSON(cliOut.Result)
			if exErr != nil {
				lastErr = fmt.Errorf("応答から JSON を抽出できませんでした: %w", exErr)
				continue
			}
		}

		if err := validateRequired(extracted, required); err != nil {
			lastErr = err
			continue
		}

		return extracted, run, nil
	}

	return nil, lastRun, fmt.Errorf("%d 回試行しましたが%w: %w", maxSchemaAttempts, ErrSchemaMismatch, lastErr)
}

func buildRetryNote(prevErr error) string {
	reason := "前回の出力が JSON Schema に適合しませんでした。"
	if prevErr != nil {
		reason = fmt.Sprintf("前回の出力が JSON Schema に適合しませんでした（%s）。", prevErr.Error())
	}
	return "\n\n---\n" + reason + "説明文やコードフェンスを含めず、指定された JSON Schema に従う JSON オブジェクトのみを出力してください。"
}

// buildSystemPrompt は req.System（役割指示）を system prompt として返す。
// 内容は毎回ほぼ同一（プロンプトのバージョンが変わらない限り不変）なため、
// claude 側の prompt cache が効きやすい。
// 実際に claude -p へ渡す経路は runOnce を参照（--system-prompt-file 経由）。
//
// req.Schema はここには含めない。スキーマは runOnce が --json-schema で渡しており、
// claude 側はそれをツール定義としてモデルに見せる（構造化出力はツール経由で実装されて
// いるため、スキーマ無しの最小呼び出しに比べてツール定義ぶんの固定費が乗ることが
// 実測で確認できている）。system prompt にも同じ JSON を積むと、同一内容を 1 回の
// 評価で二重に送ることになる。
func buildSystemPrompt(req judge.Request) string {
	return strings.TrimSpace(req.System)
}

func (j *Judge) recordRun(r RunInfo) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runs = append(j.runs, r)
}

// LastRun は直近に記録された claude 実行の RunInfo を返す（未実行ならゼロ値）。
// 並行実行下では「自分が呼んだ Evaluate の結果」とは限らない点に注意。
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

// resolveWorkDir は claude を実行する作業ディレクトリを決め、存在しなければ作成する。
// 呼び出し元のカレントディレクトリで実行すると、評価対象プロジェクトの CLAUDE.md や
// スキルを読み込んでノイズと余計なコストが乗るため、専用の作業ディレクトリを必ず使う。
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

// cliOutput は `claude -p --output-format json` の出力のうち、本実装が使う項目だけを
// 抜き出したもの。実測したトップレベルキー（result, is_error, session_id,
// total_cost_usd, num_turns, duration_ms, subtype など）に対応する。
type cliOutput struct {
	Result string `json:"result"`
	// StructuredOutput は --json-schema を渡したときに claude が返す
	// 検証済みの構造化出力。これがあれば result のテキストから JSON を
	// 抜き出す必要がなく、フォーマット逸脱も起きない。
	StructuredOutput json.RawMessage `json:"structured_output"`
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	DurationMS       int64           `json:"duration_ms"`
}

// runWithBackoff は runOnce を一時的失敗に対して指数バックオフで最大
// maxTransientAttempts 回まで試みる。スキーマ不一致の再試行（EvaluateRun 側の
// maxSchemaAttempts ループ）とは独立した、別枠のリトライ。
func (j *Judge) runWithBackoff(ctx context.Context, workDir, systemPrompt, userPrompt, model string, schema json.RawMessage) (*cliOutput, error) {
	var lastErr error
	for attempt := 0; attempt < maxTransientAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second // 1s, 2s, ...
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, fmt.Errorf("claude 実行のバックオフ待機中に context が終了しました: %w", ctx.Err())
			}
		}

		out, transient, err := j.runOnce(ctx, workDir, systemPrompt, userPrompt, model, schema)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !transient {
			return nil, err
		}
	}
	return nil, fmt.Errorf("claude CLI の実行が %d 回とも一時的エラーで失敗しました: %w", maxTransientAttempts, lastErr)
}

// runOnce は claude -p を 1 回実行する。第 2 戻り値は、その失敗が一時的なもの
// （リトライして意味がある）かどうか。
func (j *Judge) runOnce(ctx context.Context, workDir, systemPrompt, userPrompt, model string, schema json.RawMessage) (*cliOutput, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, j.opts.Timeout)
	defer cancel()

	args := []string{"-p", "--output-format", "json"}
	// 評価にツールは不要。claude --help で確認できる "--tools" フラグは
	// "" を渡すとツールを全て無効化できる（"Use \"\" to disable all tools"）ため、
	// これを使う。--allowed-tools/--disallowed-tools は個別指定用で全無効化には
	// 不向きなため使わない。
	args = append(args, "--tools", "")
	if model != "" {
		args = append(args, "--model", model)
	}
	// claude -p は対話セッションのサブスクリプション枠ではなく API 従量枠を
	// 消費する。暴走した 1 回が枠を食い潰さないよう、必ず上限を渡す。
	budget := j.opts.MaxBudgetUSD
	if budget <= 0 {
		budget = defaultMaxBudgetUSD
	}
	args = append(args, "--max-budget-usd", strconv.FormatFloat(budget, 'f', -1, 64))
	if len(schema) > 0 {
		// --json-schema を渡すと claude 側が構造化出力を検証し、結果を
		// structured_output フィールドで返す。これによりモデルが説明文や
		// コードフェンスを混ぜてもパースが壊れなくなる（実レポートで
		// haiku が Markdown を返す逸脱が実際に 2 件観測されている）。
		//
		// スキーマは argv で渡すため、改行を必ず落として 1 行にする。
		// この環境では claude が mise の .cmd シム経由で起動され、cmd.exe の
		// コマンドラインは改行を保持できないため、整形済み JSON をそのまま
		// 渡すと内容が壊れる（--system-prompt で実際に踏んだ問題と同根）。
		var compact bytes.Buffer
		if err := json.Compact(&compact, schema); err != nil {
			return nil, false, fmt.Errorf("JSON Schema の圧縮に失敗しました: %w", err)
		}
		args = append(args, "--json-schema", compact.String())
	}
	if systemPrompt != "" {
		// systemPrompt（評価指示 session_eval.md + schema）を argv 経由の
		// --system-prompt <prompt> で渡すことも検討したが、実機で検証した結果
		// 採用しなかった。この環境では `claude` が PATH 上で mise の
		// claude.cmd シム（中身は `mise x -- claude %*`）として解決され、
		// Go の os/exec は .cmd 実行時に cmd.exe 経由でコマンドラインを組み立てる。
		// cmd.exe の 1 行コマンドラインは改行を保持できないため、数KB規模の
		// 改行入りテキストを --system-prompt に渡すと内容が壊れ、モデルが
		// スキーマを一切認識しない応答を返すことを実際に確認した
		// （argv 長 32KB 制限とは別の、改行破壊という問題）。
		// 対策として、claude --help に記載のある --system-prompt-file <path>
		// （--bare の説明文中に "--system-prompt[-file]" として言及されている）
		// を採用する。argv には短いファイルパスだけを渡し、本文はファイル
		// 経由で受け渡すため、この破壊の影響を受けない。実機で
		// --system-prompt-file が実際に指定ファイルの内容をシステムプロンプト
		// として読み込むことも確認済み。
		sysFile, err := os.CreateTemp(workDir, "insights-judge-system-*.txt")
		if err != nil {
			return nil, false, fmt.Errorf("system prompt 用の一時ファイルを作成できませんでした: %w", err)
		}
		sysPath := sysFile.Name()
		defer os.Remove(sysPath)
		if _, err := sysFile.WriteString(systemPrompt); err != nil {
			sysFile.Close()
			return nil, false, fmt.Errorf("system prompt 用の一時ファイルへの書き込みに失敗しました: %w", err)
		}
		if err := sysFile.Close(); err != nil {
			return nil, false, fmt.Errorf("system prompt 用の一時ファイルのクローズに失敗しました: %w", err)
		}
		args = append(args, "--system-prompt-file", sysPath)
	}

	cmd := exec.CommandContext(runCtx, j.opts.BinPath, args...)
	cmd.Dir = workDir
	// プロンプト（セッション台本）は argv ではなく stdin で渡す。実行して確認した
	// ところ、`claude -p --output-format json` はプロンプト引数を省略すると
	// 標準入力からテキストを読み取り、それをプロンプトとして扱う（--input-format
	// text が既定）。セッション台本は会話全文を含むため数十KB〜数百KBになりうり、
	// Windows の argv 長上限（約32KB）を超える恐れがある。stdin であればその
	// 制限を受けないため、こちらを採用する。
	cmd.Stdin = strings.NewReader(userPrompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, true, fmt.Errorf("%w (%s): %w", ErrTimeout, j.opts.Timeout, err)
		}
		if isRateLimitLike(stdout.String() + "\n" + stderr.String()) {
			return nil, true, fmt.Errorf("%w: %w (stderr=%s)", ErrRateLimited, err, stderr.String())
		}
		// プロセス起動失敗（実行ファイルが見つからない等）も含め、一時的失敗として
		// リトライ対象にする（要件どおり）。
		return nil, true, fmt.Errorf("claude の実行に失敗しました: %w (stderr=%s)", err, stderr.String())
	}

	var out cliOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &out); jsonErr != nil {
		return nil, true, fmt.Errorf("claude の出力を JSON として解釈できませんでした: %w (stdout=%s)", jsonErr, truncateForError(stdout.String()))
	}

	return &out, false, nil
}

// isRateLimitLike はプロセス出力にレート制限を示唆する文字列が含まれるかを見る。
func isRateLimitLike(s string) bool {
	lower := strings.ToLower(s)
	for _, needle := range []string{"rate limit", "rate_limit", "too many requests", "429", "overloaded"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
