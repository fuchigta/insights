package skill

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fuchigta/insights/internal/skill/assets"
)

// 【なぜ配置処理をここに置くか】
// Claude Code も Codex も「<エージェントのホーム>/skills/insights/SKILL.md を
// 1 枚置く」という同じ規約で、違うのは配置先ディレクトリの決め方と、そのエージェントが
// 入っているかの判定だけである。Installer 実装ごとに書くと、改変検出・アトミックな
// 書き込み・後始末といった間違えやすい部分が複製され、片方だけ直る事故が起きる。
// そこで「どこに置くか」だけを各実装に残し、「どう置くか」はここに集約する。
//
// 本パッケージが internal/skill/assets を import しても循環にはならない
// （assets は skill を import しない）。具象 Installer 実装を import しない、という
// registry.go の方針もそのまま保たれる。

// SkillFileName は配置するスキル本体のファイル名。
const SkillFileName = "SKILL.md"

// InstallTo は target ディレクトリへスキルを配置する。
// 現在の状態が StateModified のとき、force が false ならエラーにする。
//
// 書き込みは一時ファイル経由のアトミックな置き換えで行う
// （途中失敗で壊れた SKILL.md が残らないようにするため）。
func InstallTo(agent string, scope Scope, target string, force bool) (Result, error) {
	status, err := StatusOf(agent, scope, target)
	if err != nil {
		return Result{}, err
	}
	if status.State == StateModified && !force {
		return Result{}, fmt.Errorf(
			"%s の %s は手で書き換えられています。上書きするには force を指定してください: %s",
			agent, SkillFileName, status.Path,
		)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return Result{}, fmt.Errorf("スキル配置先の作成に失敗しました (%s): %w", target, err)
	}

	dest := filepath.Join(target, SkillFileName)
	if err := writeFileAtomic(dest, assets.SkillMD(), 0o644); err != nil {
		return Result{}, fmt.Errorf("%s の書き込みに失敗しました (%s): %w", SkillFileName, dest, err)
	}

	return Result{
		Path:    target,
		Written: []string{dest},
		From:    status.State,
	}, nil
}

// StatusOf は target ディレクトリの導入状態を返す。
func StatusOf(agent string, scope Scope, target string) (Status, error) {
	st := Status{
		Agent:          agent,
		Scope:          scope,
		Path:           target,
		State:          StateAbsent,
		BundledVersion: assets.Version,
	}

	path := filepath.Join(target, SkillFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("%s の読み込みに失敗しました (%s): %w", SkillFileName, path, err)
	}

	fm, err := parseFrontMatter(data)
	if err != nil {
		// frontmatter が壊れている＝insights が書いた形式ではなくなっている。
		// 手で書き換えられたものとみなす。
		st.State = StateModified
		return st, nil
	}
	st.InstalledVersion = fm.version()

	if fm.version() != assets.Version {
		st.State = StateOutdated
		return st, nil
	}

	if !bytes.Equal(data, assets.SkillMD()) {
		st.State = StateModified
		return st, nil
	}

	st.State = StateCurrent
	return st, nil
}

// UninstallFrom は target に配置済みのスキルを削除する。未導入なら何もせず成功する。
// insights が置いた SKILL.md（と、それだけで空になったディレクトリ）だけを消し、
// 利用者が同じ skills/ 配下に置いた他のスキルには触れない。
func UninstallFrom(target string) error {
	path := filepath.Join(target, SkillFileName)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s の削除に失敗しました (%s): %w", SkillFileName, path, err)
	}

	// insights 専用ディレクトリ（.../skills/insights）が空になったら片付ける。
	// 空でなければ（利用者が他ファイルを置いていれば）何もせず残す。
	// skills/ より上の階層には一切触れない。
	_ = os.Remove(target)
	return nil
}

// frontMatterDelim は YAML フロントマターの開始・終了に使う区切り行。
const frontMatterDelim = "---"

// frontMatter は SKILL.md 先頭の YAML frontmatter のうち、状態判定に必要な部分だけ。
//
// バージョンは Agent Skills spec の `metadata`（利用側のツールが自由に使ってよい
// キーバリューのマップ）に入れている。トップレベルに独自キーを足すと、claude.ai への
// アップロードやパッケージングが "Unexpected key(s) in SKILL.md frontmatter" で
// 落ちるため。LegacyVersion は metadata へ移す前に配置された SKILL.md
// （トップレベルの x-insights-version）を読むための後方互換で、これが無いと
// 旧バージョンの導入済みスキルが outdated ではなく「改変済み」に見えてしまう。
type frontMatter struct {
	Name     string `yaml:"name"`
	Metadata struct {
		Version string `yaml:"insights-version"`
	} `yaml:"metadata"`
	LegacyVersion string `yaml:"x-insights-version"`
}

// version は frontmatter から読み取ったスキルのバージョン。
func (fm frontMatter) version() string {
	if fm.Metadata.Version != "" {
		return fm.Metadata.Version
	}
	return fm.LegacyVersion
}

// parseFrontMatter は SKILL.md のバイト列先頭にある YAML frontmatter を取り出してパースする。
func parseFrontMatter(data []byte) (frontMatter, error) {
	var fm frontMatter

	// CRLF を LF に寄せてから解析する。Windows で git の改行変換が有効な状態で
	// チェックアウトされた SKILL.md や、利用者がエディタで編集したファイルは
	// CRLF になりうる。区切り行の判定が "---\n" 固定だと、そのとき frontmatter を
	// 読めず「手で改変済み」と誤判定してしまう。
	raw := strings.ReplaceAll(string(data), "\r\n", "\n")
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
