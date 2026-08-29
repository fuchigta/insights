// Package claudecode は Claude Code 向けの skill.Installer 実装。
// スキルの配置先は Claude Code の規約（~/.claude/skills/<name> または
// ./.claude/skills/<name>）に従う。
package claudecode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fuchigta/insights/internal/skill"
	"github.com/fuchigta/insights/internal/skill/assets"
)

// skillFileName は配置するスキル本体のファイル名。
const skillFileName = "SKILL.md"

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
// 呼び出し元（将来の cmd/insights・internal/cli）が
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
// 書き込みは一時ファイル経由のアトミックな置き換えで行う（途中失敗で壊れた
// SKILL.md が残らないようにするため）。
func (i *Installer) Install(scope skill.Scope, force bool) (skill.Result, error) {
	target, err := i.Target(scope)
	if err != nil {
		return skill.Result{}, err
	}

	status, err := i.Status(scope)
	if err != nil {
		return skill.Result{}, err
	}
	if status.State == skill.StateModified && !force {
		return skill.Result{}, fmt.Errorf(
			"%s の SKILL.md は手で書き換えられています。上書きするには force を指定してください: %s",
			i.Agent(), status.Path,
		)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return skill.Result{}, fmt.Errorf("スキル配置先の作成に失敗しました (%s): %w", target, err)
	}

	dest := filepath.Join(target, skillFileName)
	if err := writeFileAtomic(dest, assets.SkillMD(), 0o644); err != nil {
		return skill.Result{}, fmt.Errorf("SKILL.md の書き込みに失敗しました (%s): %w", dest, err)
	}

	return skill.Result{
		Path:    target,
		Written: []string{dest},
		From:    status.State,
	}, nil
}

// Status は現在の導入状態を返す。
func (i *Installer) Status(scope skill.Scope) (skill.Status, error) {
	target, err := i.Target(scope)
	if err != nil {
		return skill.Status{}, err
	}

	st := skill.Status{
		Agent:          i.Agent(),
		Scope:          scope,
		Path:           target,
		State:          skill.StateAbsent,
		BundledVersion: assets.Version,
	}

	path := filepath.Join(target, skillFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return skill.Status{}, fmt.Errorf("SKILL.md の読み込みに失敗しました (%s): %w", path, err)
	}

	fm, err := parseFrontMatter(data)
	if err != nil {
		// frontmatter が壊れている＝insights が書いた形式ではなくなっている。
		// 手で書き換えられたものとみなす。
		st.State = skill.StateModified
		return st, nil
	}
	st.InstalledVersion = fm.Version

	if fm.Version != assets.Version {
		st.State = skill.StateOutdated
		return st, nil
	}

	if !bytes.Equal(data, assets.SkillMD()) {
		st.State = skill.StateModified
		return st, nil
	}

	st.State = skill.StateCurrent
	return st, nil
}

// Uninstall は配置済みスキルを削除する。未導入なら何もせず成功する。
// insights が置いた SKILL.md（と、それだけで空になったディレクトリ）だけを消し、
// ユーザーが同じ skills/ 配下に置いた他のスキルには触れない。
func (i *Installer) Uninstall(scope skill.Scope) error {
	target, err := i.Target(scope)
	if err != nil {
		return err
	}

	path := filepath.Join(target, skillFileName)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("SKILL.md の削除に失敗しました (%s): %w", path, err)
	}

	// insights 専用ディレクトリ（.../skills/insights）が空になったら片付ける。
	// 空でなければ（ユーザーが他ファイルを置いていれば）何もせず残す。
	// skills/ より上の階層には一切触れない。
	_ = os.Remove(target)
	return nil
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

// frontMatterDelim は YAML フロントマターの開始・終了に使う区切り行。
const frontMatterDelim = "---"

// frontMatter は SKILL.md 先頭の YAML frontmatter のうち、状態判定に必要な部分だけ。
type frontMatter struct {
	Name    string `yaml:"name"`
	Version string `yaml:"x-insights-version"`
}

// parseFrontMatter は SKILL.md のバイト列先頭にある YAML frontmatter を取り出してパースする。
func parseFrontMatter(data []byte) (frontMatter, error) {
	var fm frontMatter

	raw := string(data)
	if !strings.HasPrefix(raw, frontMatterDelim+"\n") {
		return fm, fmt.Errorf("frontmatter が見つかりません（先頭が %q ではありません）", frontMatterDelim)
	}
	rest := raw[len(frontMatterDelim)+1:]
	end := strings.Index(rest, "\n"+frontMatterDelim)
	if end < 0 {
		return fm, fmt.Errorf("frontmatter の終端 %q が見つかりません", frontMatterDelim)
	}
	yamlPart := rest[:end]

	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return fm, fmt.Errorf("frontmatter の YAML デコードに失敗しました: %w", err)
	}
	return fm, nil
}

// writeFileAtomic は dest と同じディレクトリに一時ファイルを作って書き込み、
// 成功したら rename で dest に置き換える。os.Rename はターゲット OS 上で
// アトミックな置き換えとして扱われる（Windows でも既存ファイルを置き換え可能）。
// 途中で失敗した場合は一時ファイルを削除し、dest には触れない。
func writeFileAtomic(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".insights-skill-*.tmp")
	if err != nil {
		return fmt.Errorf("一時ファイルの作成に失敗しました: %w", err)
	}
	tmpPath := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("一時ファイルへの書き込みに失敗しました: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("一時ファイルのクローズに失敗しました: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("一時ファイルの権限設定に失敗しました: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("一時ファイルのリネームに失敗しました: %w", err)
	}
	ok = true
	return nil
}
