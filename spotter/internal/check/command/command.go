// Package command は types.<name>.command を持つ検査（外部コマンド検査）を実行する。
//
// docs/hooks-extraction.md の「command 型の入出力契約」の通り、ホストが渡すのは
// 「どの範囲を見るか」と「免除判定用のメッセージ」だけにし、ファイルリストや diff は
// 検査コマンド自身が git で取得する。
//
//	<command> --mode staged --message-file <path>
//	<command> --mode range  --from <sha> --to <sha> --message-file <path>
//
// 終了コード 0 = 成功、非 0 = 失敗（stderr を違反内容として表示する）。
package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/schema"
)

// Check は command 型検査の 1 インスタンス。
type Check struct {
	command     string
	granularity check.Granularity
	transport   string
	options     map[string]any
	// argsExtra/envExtra は options から一度だけ組み立てた追加引数・環境変数
	// （transport ごとに使う方だけが埋まる）。options は checks インスタンスの
	// 生存期間中変わらないため、Run のたびに作り直す必要が無い。
	argsExtra []string
	envExtra  []string
}

// New は key（checks のキー、エラーメッセージ用）・cc（checks.<key>）・
// tc（types.<type>）から Check を組み立てる。tc.Command が空なら呼び出し側の
// バグ（config.Load が検証を通しているはず）なのでエラーを返す。
func New(key string, cc config.CheckConfig, tc config.TypeConfig) (*Check, error) {
	if tc.Command == "" {
		return nil, fmt.Errorf("command: %s: types.%s に command がありません", key, cc.Type)
	}

	granularity, err := parseGranularity(tc.Default)
	if err != nil {
		return nil, fmt.Errorf("command: %s: %w", key, err)
	}

	sch, err := schema.Compile(tc.Schema)
	if err != nil {
		return nil, fmt.Errorf("command: %s: %w", key, err)
	}
	if err := sch.Validate(cc.Options); err != nil {
		return nil, fmt.Errorf("command: %s: %w", key, err)
	}

	transport := tc.Transport
	if transport == "" {
		transport = "file"
	}

	c := &Check{
		command:     tc.Command,
		granularity: granularity,
		transport:   transport,
		options:     cc.Options,
	}

	switch transport {
	case "file":
	case "args":
		args, err := buildArgsTransport(cc.Options)
		if err != nil {
			return nil, fmt.Errorf("command: %s: %w", key, err)
		}
		c.argsExtra = args
	case "env":
		env, err := buildEnvTransport(cc.Options)
		if err != nil {
			return nil, fmt.Errorf("command: %s: %w", key, err)
		}
		c.envExtra = env
	default:
		// config.Load の validateTypeConfig で弾いているはずだが、直接呼ばれた
		// 場合の保険として残す。
		return nil, fmt.Errorf("command: %s: 未対応の transport %q です（file | args | env）", key, transport)
	}

	return c, nil
}

func parseGranularity(d *config.TypeDefault) (check.Granularity, error) {
	if d == nil || d.Granularity == "" {
		return "", errors.New("types.<type>.default.granularity が必要です（squashed | per-commit）")
	}
	switch d.Granularity {
	case string(check.GranularitySquashed):
		return check.GranularitySquashed, nil
	case string(check.GranularityPerCommit):
		return check.GranularityPerCommit, nil
	default:
		return "", fmt.Errorf("granularity %q は command 型では未対応です（squashed | per-commit のみ）", d.Granularity)
	}
}

// Granularity はこの検査を範囲モードでどう起動するか（types.<type>.default.granularity 由来）。
func (c *Check) Granularity() check.Granularity {
	return c.granularity
}

// Run は検査コマンドを 1 回起動する。
func (c *Check) Run(ctx check.Context) ([]check.Violation, error) {
	var args []string
	if ctx.Range == nil {
		args = append(args, "--mode", "staged")
	} else {
		args = append(args, "--mode", "range", "--from", ctx.Range.From, "--to", ctx.Range.To)
	}

	msgFile, err := writeTempFile("spotter-message-*", []byte(ctx.Message))
	if err != nil {
		return nil, fmt.Errorf("command: メッセージファイルの作成に失敗しました: %w", err)
	}
	defer os.Remove(msgFile)
	args = append(args, "--message-file", msgFile)

	var extraEnv []string
	switch c.transport {
	case "args":
		args = append(args, c.argsExtra...)
	case "env":
		extraEnv = c.envExtra
	default: // file
		data, err := json.Marshal(c.options)
		if err != nil {
			return nil, fmt.Errorf("command: options のエンコードに失敗しました: %w", err)
		}
		optFile, err := writeTempFile("spotter-options-*.json", data)
		if err != nil {
			return nil, fmt.Errorf("command: オプションファイルの作成に失敗しました: %w", err)
		}
		defer os.Remove(optFile)
		args = append(args, "--options-file", optFile)
	}

	cmd := exec.Command(c.command, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return nil, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		summary := strings.TrimSpace(stderr.String())
		if summary == "" {
			summary = fmt.Sprintf("%s が失敗しました（終了コード %d）", c.command, exitErr.ExitCode())
		}
		return []check.Violation{{Summary: summary}}, nil
	}
	return nil, fmt.Errorf("command: %s の実行に失敗しました: %w", c.command, runErr)
}

func writeTempFile(pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// buildArgsTransport は options を `--<field> <value>` に展開する。配列は
// 同じフラグの繰り返しにする。
func buildArgsTransport(options map[string]any) ([]string, error) {
	var args []string
	for _, name := range sortedKeys(options) {
		v := options[name]
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				s, err := scalarToString(item)
				if err != nil {
					return nil, fmt.Errorf("オプション %s: %w", name, err)
				}
				args = append(args, "--"+name, s)
			}
			continue
		}
		s, err := scalarToString(v)
		if err != nil {
			return nil, fmt.Errorf("オプション %s: %w", name, err)
		}
		args = append(args, "--"+name, s)
	}
	return args, nil
}

// buildEnvTransport は options を `SPOTTER_OPT_<FIELD>=<value>` に展開する。
// スカラーのみ対応（配列を表現する安全な方法が無いため。docs/hooks-extraction.md 参照）。
func buildEnvTransport(options map[string]any) ([]string, error) {
	var env []string
	for _, name := range sortedKeys(options) {
		s, err := scalarToString(options[name])
		if err != nil {
			return nil, fmt.Errorf("transport env はスカラーのみ対応です。オプション %s: %w", name, err)
		}
		env = append(env, envKey(name)+"="+s)
	}
	return env, nil
}

func envKey(name string) string {
	return "SPOTTER_OPT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func scalarToString(v any) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case bool:
		return strconv.FormatBool(val), nil
	case int:
		return strconv.Itoa(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("スカラー値ではありません（%T）", v)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
