// Package codex は Codex 向けの skill.Installer 実装。
//
// 【配置先をどう決めたか】
// Codex はスキルを複数の場所から読む（codex-rs/ext/skills の resolve_skill_roots）。
//
//   - $CODEX_HOME/skills            … ユーザー全体。ソース上は「後方互換のために残して
//     いる場所」と書かれているが、現行版も読み込む
//   - ~/.agents/skills              … ユーザー全体。エージェント非依存の新しい置き場
//   - <プロジェクト>/.codex/skills  … リポジトリ単位（プロジェクトの設定ディレクトリ配下）
//   - <プロジェクト>/.agents/skills … リポジトリ単位（cwd からプロジェクトルートまで探索）
//
// insights は $CODEX_HOME/skills と <プロジェクト>/.codex/skills に置く。理由は 2 つ。
// ひとつは、どちらも Codex 固有の置き場なので、insights が置いたものが他エージェントの
// 一覧に紛れ込まないこと。もうひとつは、~/.agents/skills を読む機能が入ったのは
// $CODEX_HOME/skills より後で、手元の Codex がそれを読む保証が無いこと
// （読めない古い版に置くと、導入したのに使えないという最悪の失敗の仕方をする）。
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fuchigta/insights/internal/skill"
)

// agentName は Agent() が返す識別子。
const agentName = "codex"

// codexHomeDirName は $CODEX_HOME 未設定時のホーム配下のディレクトリ名。
const codexHomeDirName = ".codex"

// skillsDirName / skillName は配置先の末尾 2 階層。
const (
	skillsDirName = "skills"
	skillName     = "insights"
)

// init は自分自身を internal/skill のレジストリに登録する
// （自己登録方式の理由は internal/skill/registry.go のコメントを参照）。
func init() {
	skill.Register(New())
}

// Installer は Codex 向けの skill.Installer 実装。
type Installer struct {
	// CodexHome はユーザースコープ（ScopeUser）の基準ディレクトリ（$CODEX_HOME 相当）。
	// 空なら環境変数 CODEX_HOME、それも無ければ ~/.codex を使う。
	// テストで t.TempDir() に差し替えるためのフックでもある。
	CodexHome string

	// WorkDir はプロジェクトスコープ（ScopeProject）の基準ディレクトリ。
	// 空なら os.Getwd() を使う。テストで t.TempDir() に差し替えるためのフック。
	WorkDir string

	// LookPath はコマンド探索に使う関数。空なら exec.LookPath を使う。
	// Detect() のテストで PATH に依存させないためのフック。
	LookPath func(file string) (string, error)
}

// New は既定設定の Installer を返す（CodexHome/WorkDir は実環境を使う）。
func New() *Installer {
	return &Installer{}
}

// skill.Installer を満たすことをコンパイル時に確認する。
var _ skill.Installer = (*Installer)(nil)

// Agent は "codex" を返す。
func (i *Installer) Agent() string {
	return agentName
}

// Detect は codex コマンドが PATH にあるか、$CODEX_HOME が存在するかで判定する。
func (i *Installer) Detect() bool {
	lookPath := i.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("codex"); err == nil {
		return true
	}

	home, err := i.codexHome()
	if err != nil {
		return false
	}
	if _, err := os.Stat(home); err == nil {
		return true
	}
	return false
}

// Target は scope に対応する配置先ディレクトリを返す。
func (i *Installer) Target(scope skill.Scope) (string, error) {
	switch scope {
	case skill.ScopeUser:
		home, err := i.codexHome()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, skillsDirName, skillName), nil
	case skill.ScopeProject:
		wd, err := i.workDir()
		if err != nil {
			return "", fmt.Errorf("カレントディレクトリの取得に失敗しました: %w", err)
		}
		// プロジェクトスコープは <プロジェクト>/.codex/skills 配下。
		// ユーザースコープと違い、こちらは $CODEX_HOME の影響を受けない。
		return filepath.Join(wd, codexHomeDirName, skillsDirName, skillName), nil
	default:
		return "", fmt.Errorf("未知の scope です: %q", scope)
	}
}

// Install はスキルを配置する。State が StateModified のとき force が false ならエラー。
func (i *Installer) Install(scope skill.Scope, force bool) (skill.Result, error) {
	target, err := i.Target(scope)
	if err != nil {
		return skill.Result{}, err
	}
	return skill.InstallTo(i.Agent(), scope, target, force)
}

// Status は現在の導入状態を返す。
func (i *Installer) Status(scope skill.Scope) (skill.Status, error) {
	target, err := i.Target(scope)
	if err != nil {
		return skill.Status{}, err
	}
	return skill.StatusOf(i.Agent(), scope, target)
}

// Uninstall は配置済みスキルを削除する。未導入なら何もせず成功する。
func (i *Installer) Uninstall(scope skill.Scope) error {
	target, err := i.Target(scope)
	if err != nil {
		return err
	}
	return skill.UninstallFrom(target)
}

// codexHome は Codex のホームディレクトリを返す。
// CodexHome フィールド → 環境変数 CODEX_HOME → ~/.codex の順に解決する。
// Codex 自身が CODEX_HOME を優先するため、それに合わせないと、ホームを移して
// いる利用者の Codex が読まない場所へスキルを置いてしまう。
func (i *Installer) codexHome() (string, error) {
	if i.CodexHome != "" {
		return i.CodexHome, nil
	}
	if env := strings.TrimSpace(os.Getenv("CODEX_HOME")); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
	}
	return filepath.Join(home, codexHomeDirName), nil
}

// workDir は WorkDir フィールドがあればそれを、なければ os.Getwd() を返す。
func (i *Installer) workDir() (string, error) {
	if i.WorkDir != "" {
		return i.WorkDir, nil
	}
	return os.Getwd()
}
