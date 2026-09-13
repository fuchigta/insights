// Package gitutil は spotter が必要とする git 操作をまとめる。
//
// docs/hooks-extraction.md の「command 型の入出力契約」にある通り、ファイルリストや diff は
// ホスト（spotter 本体）が git で取得する。検査本体は check.Source インターフェース越しに
// これを参照するだけで、staged/range の違いを知らない。
package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/fuchigta/spotter/internal/check"
)

// EmptyTree は git の空ツリーの SHA。根コミットを含む範囲の起点に使う。
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Repo はリポジトリのルートディレクトリに対する git 操作をまとめる。
type Repo struct {
	Dir string
}

// New は dir をルートとする Repo を返す。
func New(dir string) *Repo {
	return &Repo{Dir: dir}
}

func (r *Repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gitutil: git %s の実行に失敗しました: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// RevListNoMerges は range 式（"a..b" や "-1 HEAD" のような複数語も許す）に一致する
// マージコミットを除くコミットの一覧を新しい順で返す。
func (r *Repo) RevListNoMerges(rangeExpr string) ([]string, error) {
	args := append([]string{"rev-list", "--no-merges"}, strings.Fields(rangeExpr)...)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	return splitNonEmptyLines(out), nil
}

// ConfigGet は git config の値を読む。未設定なら ok=false（値の有無と空文字の区別が
// 要らない呼び出し側の便宜のため、エラーではなく ok で表す）。
func (r *Repo) ConfigGet(key string) (value string, ok bool, err error) {
	out, runErr := r.run("config", "--get", key)
	if runErr != nil {
		return "", false, nil
	}
	return strings.TrimSpace(out), true, nil
}

// ConfigSet は git config の値を設定する。
func (r *Repo) ConfigSet(key, value string) error {
	_, err := r.run("config", key, value)
	return err
}

// GitPath は git rev-parse --git-path で、.git ディレクトリ配下の実際のパスを解決する
// （worktree・submodule で .git が単純なディレクトリではない場合も正しく解決するため）。
func (r *Repo) GitPath(rel string) (string, error) {
	out, err := r.run("rev-parse", "--git-path", rel)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitExists は sha がこのリポジトリに実在するコミットかどうかを返す。
// 新しいブランチの最初の push や force push 直後は CI が渡す「比較元」の SHA が
// 全ゼロ（0000...）になったり、そもそも取得されていなかったりする。そうした
// 「実在しない」は呼び出し側のフォールバック処理に委ねるための正常系なので、
// git コマンド自体の失敗（非 0 終了）はエラーにせず false として返す
// （元のシェルスクリプトの `git cat-file -e ... 2>/dev/null` と同じ割り切り）。
func (r *Repo) CommitExists(sha string) (bool, error) {
	if sha == "" {
		return false, nil
	}
	if _, err := r.run("cat-file", "-e", sha+"^{commit}"); err != nil {
		return false, nil
	}
	return true, nil
}

// ParentOrEmptyTree は sha の親コミットを返す。根コミットなら EmptyTree を返す。
func (r *Repo) ParentOrEmptyTree(sha string) (string, error) {
	out, err := r.run("rev-parse", "-q", "--verify", sha+"^")
	if err != nil {
		return EmptyTree, nil
	}
	return strings.TrimSpace(out), nil
}

// CommitMessageBody はそのコミット単体のメッセージ本文を返す。
func (r *Repo) CommitMessageBody(sha string) (string, error) {
	return r.run("log", "-1", "--format=%B", sha)
}

// RangeMessagesBody は range 式に含まれる全コミットのメッセージを連結して返す
// （squashed 粒度の免除判定用。範囲内のどれか 1 つにトレーラがあれば免除が効く）。
func (r *Repo) RangeMessagesBody(rangeExpr string) (string, error) {
	args := append([]string{"log", "--format=%B"}, strings.Fields(rangeExpr)...)
	return r.run(args...)
}

// CommitLabel は "<短い sha> <件名>" というラベルを返す。
func (r *Repo) CommitLabel(sha string) (string, error) {
	out, err := r.run("log", "-1", "--format=%h %s", sha)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// stagedSource はステージ済みの変更（commit-msg フック）を見る check.Source。
type stagedSource struct{ r *Repo }

// StagedSource はステージ済みの変更を見る check.Source を返す。
func (r *Repo) StagedSource() check.Source {
	return stagedSource{r: r}
}

func (s stagedSource) ChangedFiles() ([]string, error) {
	out, err := s.r.run("diff", "--cached", "--name-only", "--diff-filter=ACMR")
	if err != nil {
		return nil, err
	}
	return splitNonEmptyLines(out), nil
}

func (s stagedSource) DiffLines(path string) (string, error) {
	return s.r.run("diff", "--cached", "-U0", "--", path)
}

func (s stagedSource) BlobSize(path string) (int64, error) {
	return s.r.blobSize(":" + path)
}

// rangeSource は from..to の比較を見る check.Source。
type rangeSource struct {
	r        *Repo
	from, to string
}

// RangeSource は from から to までの比較を見る check.Source を返す。
func (r *Repo) RangeSource(from, to string) check.Source {
	return rangeSource{r: r, from: from, to: to}
}

func (s rangeSource) ChangedFiles() ([]string, error) {
	out, err := s.r.run("diff", "--name-only", "--diff-filter=ACMR", s.from, s.to)
	if err != nil {
		return nil, err
	}
	return splitNonEmptyLines(out), nil
}

func (s rangeSource) DiffLines(path string) (string, error) {
	return s.r.run("diff", "-U0", s.from, s.to, "--", path)
}

func (s rangeSource) BlobSize(path string) (int64, error) {
	return s.r.blobSize(s.to + ":" + path)
}

// blobSize はそのオブジェクトのバイト数を返す。存在しない（削除された等）場合は 0 を返す
// （移行元のシェルスクリプトと同じ挙動）。
func (r *Repo) blobSize(object string) (int64, error) {
	out, err := r.run("cat-file", "-s", object)
	if err != nil {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("gitutil: cat-file -s %s の出力を解析できません: %w", object, err)
	}
	return n, nil
}
