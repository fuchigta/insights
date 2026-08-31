package evidence

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// gitLogFormat は `git log --format` に渡すフォーマット文字列。
//
// フィールド区切りに \x1f（Unit Separator）、レコード（コミット）区切りに
// \x1e（Record Separator）という「本文に絶対現れない制御文字」を使う。
// コミット本文（%b）には改行やタブ、さらには別コミットの引用や任意の記号列が
// 含まれ得るため、改行やタブ、カンマのような「よくある文字」を区切りに使うと
// 素朴なパースは簡単に壊れる。0x1e / 0x1f は人間やツールがテキストとして
// 打ち込むことがまず無い制御文字なので、区切り専用として安全に使える。
//
// %aI は author date を ISO 8601 strict 形式（例 2024-01-02T03:04:05+09:00）で
// 出す。--date=iso-strict + %ad と等価だが、--date オプションを別途指定する
// 必要が無く常に安定した形式になるためこちらを使う。
const gitLogFormat = "%x1e%h%x1f%H%x1f%aI%x1f%s%x1f%b"

// gitAllRefs は `git log` に渡すと全ての ref（ローカル・リモート追跡・タグ）を
// 辿らせるオプション。セッション当時のブランチが既に消えているときの
// フォールバックに使う。
const gitAllRefs = "--all"

// gitShortstatRe は `git log --shortstat` が本文の後ろに付け足す統計行にマッチする。
// 例:
//
//	" 3 files changed, 12 insertions(+), 4 deletions(-)"
//	" 1 file changed, 1 insertion(+)"
//	" 1 file changed, 3 deletions(-)"
var gitShortstatRe = regexp.MustCompile(`(?m)^\s*(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?\s*$`)

// isInsideWorkTree は path が git ワークツリー内かを判定する。
func (c *Collector) isInsideWorkTree(ctx context.Context, path string) (bool, error) {
	out, err := c.runGit(ctx, path, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// gitRemoteURL は origin リモートの URL を返す。
func (c *Collector) gitRemoteURL(ctx context.Context, path string) (string, error) {
	out, err := c.runGit(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// collectGitCommits はセッションの時間帯 [q.From, q.To] に author date が入る
// コミットを集める。ボット・自動コミットも区別せず全て対象にする
// （「意味のあるコミットだけ拾う」ような取捨選択は AI 評価器側の仕事であり、
// このパッケージは一次資料をありのまま渡すことに徹する）。
// 時間帯に該当するコミットが無ければ nil を返す（エラーではない）。
func (c *Collector) collectGitCommits(ctx context.Context, q Query) []model.Evidence {
	args := []string{"log"}
	if rev := c.resolveBranchRev(ctx, q); rev != "" {
		args = append(args, rev)
	}

	if !q.From.IsZero() {
		args = append(args, "--since="+q.From.Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		args = append(args, "--until="+q.To.Format(time.RFC3339))
	}
	// "--" でオプション/リビジョンの解釈をここで打ち切り、以降をパススペック
	// （今回は指定なし = 全ファイル対象）として明示する。
	args = append(args, "--shortstat", "--format="+gitLogFormat, "--")

	out, err := c.runGit(ctx, q.ProjectPath, args...)
	if err != nil {
		slog.Warn("evidence: git log の取得に失敗しました", "session", q.SessionID, "path", q.ProjectPath, "error", err)
		return nil
	}

	return parseGitLog(q.SessionID, out)
}

// resolveBranchRev はセッションが記録していたブランチを、いま実際に辿れる
// リビジョン指定へ解決する。戻り値が空文字なら「何も指定しない」＝現在の
// ブランチ（HEAD）を意味する。
//
// セッション当時のブランチは、その後 MR/PR がマージされて削除されていること
// が多い。そのまま `git log <branch>` を実行すると unknown revision で失敗し、
// そのセッションのコミットが丸ごと取れなくなる。そこで次の順で辿る。
//
//  1. ローカルブランチがまだあればそれを使う（一番正確）
//  2. 無ければリモート追跡ブランチ（origin/<branch>）を使う。ローカルだけ
//     削除された直後はこちらが残っている
//  3. どちらも無ければ gitAllRefs（--all）に切り替える。マージ済みのコミットは
//     マージ先（origin/main など）から辿れるため、ここで拾える。他ブランチの
//     コミットも混ざるが、時間帯で絞り込むため実害は小さく、「取れない」より
//     「少し多めに取る」方を選ぶ
func (c *Collector) resolveBranchRev(ctx context.Context, q Query) string {
	branch := strings.TrimSpace(q.GitBranch)
	if branch == "" {
		// 何も指定しない = 現在のブランチ（HEAD）。
		return ""
	}
	if strings.HasPrefix(branch, "-") {
		// 外部由来の文字列。ハイフンで始まる値をそのまま渡すと git に
		// オプションと誤認されかねないため、現在のブランチにフォールバックする。
		slog.Warn("evidence: 不正なブランチ名のため現在のブランチを使用します", "branch", branch)
		return ""
	}

	if c.revExists(ctx, q.ProjectPath, branch) {
		return branch
	}

	remoteBranch := "origin/" + branch
	if c.revExists(ctx, q.ProjectPath, remoteBranch) {
		slog.Warn("evidence: ローカルブランチが見つからないためリモート追跡ブランチを使用します",
			"session", q.SessionID, "branch", branch, "rev", remoteBranch)
		return remoteBranch
	}

	slog.Warn("evidence: ブランチが見つかりません（マージ後に削除された可能性）。全ての ref からコミットを探します",
		"session", q.SessionID, "branch", branch)
	return gitAllRefs
}

// revExists は rev が解決可能なコミットを指すかを判定する。
// `^{commit}` を付けて、タグやツリーではなくコミットに辿り着けることまで確かめる。
func (c *Collector) revExists(ctx context.Context, path, rev string) bool {
	_, err := c.runGit(ctx, path, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// parseGitLog は gitLogFormat + --shortstat の出力を model.Evidence の列へ変換する。
func parseGitLog(sessionID, raw string) []model.Evidence {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var out []model.Evidence
	for _, chunk := range strings.Split(raw, "\x1e") {
		if strings.TrimSpace(chunk) == "" {
			// 先頭の \x1e より前（常に空）や、末尾の余りを読み飛ばす。
			continue
		}

		fields := strings.SplitN(chunk, "\x1f", 5)
		if len(fields) < 5 {
			slog.Warn("evidence: git log の1件を解釈できませんでした（フィールド数不足）")
			continue
		}
		shortSHA, dateStr, subject, tail := fields[0], fields[2], fields[3], fields[4]

		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(dateStr))
		if err != nil {
			slog.Warn("evidence: コミット日時の解析に失敗しました", "sha", shortSHA, "raw", dateStr, "error", err)
		}

		body, insertions, deletions, files := splitBodyAndShortstat(tail)

		out = append(out, model.Evidence{
			SessionID:  sessionID,
			Kind:       "commit",
			Ref:        shortSHA,
			Timestamp:  ts,
			Title:      subject,
			Body:       body,
			Insertions: insertions,
			Deletions:  deletions,
			Files:      files,
		})
	}
	return out
}

// splitBodyAndShortstat は本文の末尾に付いた `git log --shortstat` の統計行を
// 取り除き、本文と (insertions, deletions, files) に分ける。統計行が
// 見つからなければ（変更ゼロのコミットなど）本文をそのまま返す。
func splitBodyAndShortstat(tail string) (body string, insertions, deletions, files int) {
	tail = strings.TrimRight(tail, "\n")
	lines := strings.Split(tail, "\n")

	last := len(lines) - 1
	for last >= 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if last >= 0 {
		if m := gitShortstatRe.FindStringSubmatch(lines[last]); m != nil {
			files, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				insertions, _ = strconv.Atoi(m[2])
			}
			if m[3] != "" {
				deletions, _ = strconv.Atoi(m[3])
			}
			lines = lines[:last]
		}
	}

	body = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	return body, insertions, deletions, files
}
