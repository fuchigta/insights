package evidence

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout は外部コマンド1回あたりの既定タイムアウト。
// ネットワーク越しの gh/glab が固まって全体の収集を止めるのを防ぐ。
const DefaultTimeout = 15 * time.Second

// DefaultMaxItems は gh/glab の PR/Issue/MR 取得件数の既定上限。
const DefaultMaxItems = 20

// truncatedMark は Body 切り詰け時に末尾へ付ける印。remaining は削った文字数。
func truncatedMark(remaining int) string {
	return fmt.Sprintf("\n…（以降 %d 文字を切り詰め）", remaining)
}

func (c *Collector) timeoutOrDefault() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Collector) maxItemsOrDefault() int {
	if c.MaxItems > 0 {
		return c.MaxItems
	}
	return DefaultMaxItems
}

// runCommand は path を args で起動し、標準出力の文字列を返す。
// exec.CommandContext に引数を配列で渡すため、シェルを一切経由しない
// （ProjectPath や GitBranch のような外部由来の文字列が混じっていても
// シェルインジェクションの余地は無い）。標準エラー出力は握り潰さず、
// 失敗時のエラーメッセージに含める。
func (c *Collector) runCommand(ctx context.Context, path, dir string, args ...string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("実行ファイルが見つかりません")
	}

	cctx, cancel := context.WithTimeout(ctx, c.timeoutOrDefault())
	defer cancel()

	cmd := exec.CommandContext(cctx, path, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s の実行に失敗しました: %w (stderr: %s)",
			path, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runGit は `git -C <dir> <args...>` を実行する。
func (c *Collector) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	return c.runCommand(ctx, c.gitPath, "", full...)
}

// runGh は `gh <args...>` を実行する。対象リポジトリは呼び出し側が --repo で明示する。
func (c *Collector) runGh(ctx context.Context, args ...string) (string, error) {
	return c.runCommand(ctx, c.ghPath, "", args...)
}

// runGlab は `glab <args...>` を実行する。対象リポジトリは呼び出し側が --repo で明示する。
func (c *Collector) runGlab(ctx context.Context, args ...string) (string, error) {
	return c.runCommand(ctx, c.glabPath, "", args...)
}
