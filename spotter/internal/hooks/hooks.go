// Package hooks は commit-msg フックの設置（spotter install）と状態確認
// （spotter doctor）を扱う。
//
// docs/hooks-extraction.md §2 の通り、フックランナーそのものは作らない。
// core.hooksPath は 1 つしか持てないため、既に lefthook 等で設定済みなら
// それを尊重し、そこにある commit-msg フックへ追記するだけにとどめる
// （新規に決め打ちのディレクトリへ差し替えたりはしない）。
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuchigta/spotter/internal/gitutil"
)

// InvocationLine は他のフックランナー（lefthook / husky など）に貼り付けるための、
// spotter を呼び出す最小の 1 行。`spotter install --print` はこれだけを出力する。
const InvocationLine = `spotter check --message "$1"`

const beginMarker = "# --- spotter (managed) begin ---"
const endMarker = "# --- spotter (managed) end ---"

// managedBlock は commit-msg フックに追記する本体。spotter が手元に無い場合は
// 警告して素通りする（docs/hooks-extraction.md §5「無ければ警告して素通り」）。
// すり抜けは CI（`spotter check --range "$(spotter range)"`）が最後の歯止めになる。
const managedBlock = beginMarker + "\n" +
	"if command -v spotter >/dev/null 2>&1; then\n" +
	"  spotter check --message \"$1\" || exit 1\n" +
	"else\n" +
	"  echo \"spotter: コマンドが見つからないため検査をスキップします\" >&2\n" +
	"fi\n" +
	endMarker + "\n"

// freshHookTemplate は commit-msg フックが存在しない場所に新規作成する内容。
const freshHookTemplate = "#!/bin/sh\n" +
	"# spotter install が生成した commit-msg フック。\n" +
	managedBlock

// Outcome は Install が実際に何をしたか。
type Outcome string

const (
	OutcomeCreated  Outcome = "created"  // フックファイルを新規作成した
	OutcomeAppended Outcome = "appended" // 既存のフックファイルに追記した
	OutcomeAlready  Outcome = "already"  // 既に spotter を呼び出す設定になっていた
)

// Status は現在のフック設置状況（doctor 用）。
type Status struct {
	// HooksPath は git config core.hooksPath の現在値。未設定なら空文字。
	HooksPath string
	// HookFile は commit-msg フックの実際のパス（HooksPath が空なら既定の hooks ディレクトリ配下）。
	HookFile string
	// HookFileExists は HookFile が存在するか。
	HookFileExists bool
	// Managed は HookFile が spotter の管理ブロックを含むか。
	Managed bool
}

// Result は Install の結果。
type Result struct {
	Status
	Outcome Outcome
	// HooksPathChanged は core.hooksPath を新たに設定したか
	// （既に設定済みだった場合は false のまま尊重して変更しない）。
	HooksPathChanged bool
}

// Inspect は現在のフック設置状況を調べる（変更は一切しない）。
func Inspect(repo *gitutil.Repo) (Status, error) {
	hooksPath, hooksPathSet, err := repo.ConfigGet("core.hooksPath")
	if err != nil {
		return Status{}, err
	}

	hookFile, err := resolveHookFile(repo, hooksPath, hooksPathSet)
	if err != nil {
		return Status{}, err
	}

	managed := false
	if data, err := os.ReadFile(hookFile); err == nil {
		managed = strings.Contains(string(data), beginMarker)
	} else if !os.IsNotExist(err) {
		return Status{}, fmt.Errorf("hooks: %s の読み込みに失敗しました: %w", hookFile, err)
	}

	return Status{
		HooksPath:      hooksPath,
		HookFile:       hookFile,
		HookFileExists: fileExists(hookFile),
		Managed:        managed,
	}, nil
}

// Install は commit-msg フックを設置する。
//
//   - core.hooksPath が未設定なら hooksDirDefault を設定してそこに新規作成する
//   - 既に設定済みならそれを尊重し、そこにある commit-msg フックへ追記する
//     （無ければ新規作成する）
//   - 既に spotter の管理ブロックが入っていれば何もしない（べき等）
func Install(repo *gitutil.Repo, hooksDirDefault string) (Result, error) {
	before, err := Inspect(repo)
	if err != nil {
		return Result{}, err
	}

	if before.Managed {
		return Result{Status: before, Outcome: OutcomeAlready}, nil
	}

	hooksPathChanged := false
	if before.HooksPath == "" {
		if err := os.MkdirAll(filepath.Join(repo.Dir, hooksDirDefault), 0o755); err != nil {
			return Result{}, fmt.Errorf("hooks: %s の作成に失敗しました: %w", hooksDirDefault, err)
		}
		if err := repo.ConfigSet("core.hooksPath", hooksDirDefault); err != nil {
			return Result{}, fmt.Errorf("hooks: core.hooksPath の設定に失敗しました: %w", err)
		}
		hooksPathChanged = true
	}

	// core.hooksPath を変更したので改めて解決する（hookFile が変わるため）。
	after, err := Inspect(repo)
	if err != nil {
		return Result{}, err
	}

	var outcome Outcome
	if after.HookFileExists {
		if err := appendManagedBlock(after.HookFile); err != nil {
			return Result{}, err
		}
		outcome = OutcomeAppended
	} else {
		if err := os.MkdirAll(filepath.Dir(after.HookFile), 0o755); err != nil {
			return Result{}, fmt.Errorf("hooks: %s の作成に失敗しました: %w", filepath.Dir(after.HookFile), err)
		}
		if err := os.WriteFile(after.HookFile, []byte(freshHookTemplate), 0o755); err != nil {
			return Result{}, fmt.Errorf("hooks: %s の作成に失敗しました: %w", after.HookFile, err)
		}
		outcome = OutcomeCreated
	}

	final, err := Inspect(repo)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: final, Outcome: outcome, HooksPathChanged: hooksPathChanged}, nil
}

func appendManagedBlock(hookFile string) error {
	f, err := os.OpenFile(hookFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("hooks: %s への追記に失敗しました: %w", hookFile, err)
	}
	defer f.Close()

	if _, err := f.WriteString("\n" + managedBlock); err != nil {
		return fmt.Errorf("hooks: %s への追記に失敗しました: %w", hookFile, err)
	}
	return nil
}

// resolveHookFile は commit-msg フックの実際のパスを求める。hooksPathSet が false
// （core.hooksPath 未設定）なら、git 自身が使う既定の hooks ディレクトリを
// `git rev-parse --git-path hooks` で解決する（worktree・submodule でも正しく
// 求まるようにするため、".git/hooks" と決め打ちにしない）。
func resolveHookFile(repo *gitutil.Repo, hooksPath string, hooksPathSet bool) (string, error) {
	if !hooksPathSet {
		dir, err := repo.GitPath("hooks")
		if err != nil {
			return "", fmt.Errorf("hooks: 既定の hooks ディレクトリの解決に失敗しました: %w", err)
		}
		return filepath.Join(resolveDir(repo.Dir, dir), "commit-msg"), nil
	}
	return filepath.Join(resolveDir(repo.Dir, hooksPath), "commit-msg"), nil
}

// resolveDir は p が絶対パスならそのまま、相対パスなら root からの相対として解決する。
func resolveDir(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
