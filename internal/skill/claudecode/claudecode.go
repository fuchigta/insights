// Package claudecode は Claude Code 向けの skill.Installer 実装。
// スキルの配置先は Claude Code の規約（~/.claude/skills/<name> または
// ./.claude/skills/<name>）に従う。
//
// 「どこに置くか」だけをここに持ち、置き方（改変検出・アトミックな書き込み・後始末）は
// internal/skill の共通実装に委譲する。エージェントが増えても間違えやすい部分が
// 複製されないようにするため。
package claudecode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fuchigta/insights/internal/skill"
)

// agentName は Agent() が返す識別子。
const agentName = "claude-code"

// init は自分自身を internal/skill のレジストリに登録する。
//
// 【なぜここで import "github.com/fuchigta/insights/internal/skill" を許すのに
// registry.go 側から本パッケージを import しないのか】
// 本パッケージは Scope/State/Status/Result/Installer 型を使うために
// package skill を import する。もし internal/skill/registry.go が
// Installers() の実装として本パッケージを import すると、
// skill → claudecode → skill という import cycle になりビルドできない
// （Go の import cycle 検出はディレクトリの親子関係とは無関係に働く）。
// そのため package skill 側は具象実装を一切 import せず、各実装パッケージが
// 自分の init() から skill.Register() を呼んで名乗り出る自己登録方式にしている。
// 呼び出し元（internal/cli）が
// `import _ "github.com/fuchigta/insights/internal/skill/claudecode"` する
// ことで、この登録が有効になる。
func init() {
	skill.Register(New())
}

// Installer は Claude Code 向けの skill.Installer 実装。
type Installer struct {
	// HomeDir はユーザースコープ（ScopeUser）の基準ディレクトリ。
	// 空なら os.UserHomeDir() を使う。テストで t.TempDir() に差し替えるためのフック。
	HomeDir string

	// WorkDir はプロジェクトスコープ（ScopeProject）の基準ディレクトリ。
	// 空なら os.Getwd() を使う。テストで t.TempDir() に差し替えるためのフック。
	WorkDir string

	// LookPath はコマンド探索に使う関数。空なら exec.LookPath を使う。
	// Detect() のテストで PATH に依存させないためのフック。
	LookPath func(file string) (string, error)
}

// New は既定設定の Installer を返す（HomeDir/WorkDir は実環境を使う）。
func New() *Installer {
	return &Installer{}
}

// skill.Installer を満たすことをコンパイル時に確認する。
var _ skill.Installer = (*Installer)(nil)

// Agent は "claude-code" を返す。
func (i *Installer) Agent() string {
	return agentName
}

// Detect は claude コマンドが PATH にあるか、~/.claude が存在するかで判定する。
func (i *Installer) Detect() bool {
	lookPath := i.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("claude"); err == nil {
		return true
	}

	home, err := i.homeDir()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		return true
	}
	return false
}

// Target は scope に対応する配置先ディレクトリを返す。
func (i *Installer) Target(scope skill.Scope) (string, error) {
	switch scope {
	case skill.ScopeUser:
		home, err := i.homeDir()
		if err != nil {
			return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", "insights"), nil
	case skill.ScopeProject:
		wd, err := i.workDir()
		if err != nil {
			return "", fmt.Errorf("カレントディレクトリの取得に失敗しました: %w", err)
		}
		return filepath.Join(wd, ".claude", "skills", "insights"), nil
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

// homeDir は HomeDir フィールドがあればそれを、なければ os.UserHomeDir() を返す。
func (i *Installer) homeDir() (string, error) {
	if i.HomeDir != "" {
		return i.HomeDir, nil
	}
	return os.UserHomeDir()
}

// workDir は WorkDir フィールドがあればそれを、なければ os.Getwd() を返す。
func (i *Installer) workDir() (string, error) {
	if i.WorkDir != "" {
		return i.WorkDir, nil
	}
	return os.Getwd()
}
