// Package claudecode は Claude Code (~/.claude/projects/**/*.jsonl) を
// 正規化データモデルへ変換する source.Source 実装。
package claudecode

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/source"
)

// sourceName は source.Source.Name() と source.Ref.Source に使う識別子。
const sourceName = "claude-code"

// DefaultMaxTextLen はツール結果・tool_use 入力の本文を切り詰める既定の文字数。
// Source.MaxTextLen で上書きできる。
const DefaultMaxTextLen = 2000

// Source は Claude Code のログ置き場 1 つを表す source.Source 実装。
type Source struct {
	// Root は ~/.claude 相当のディレクトリ。配下の projects/ を見る。
	Root string
	// MaxTextLen はツール結果・tool_use 入力本文の切り詰め文字数。
	// 0 以下なら DefaultMaxTextLen を使う。
	MaxTextLen int
}

// New は root を Claude Code のログ置き場として Source を作る。
// root が空文字なら ~/.claude を既定値として推定する。
func New(root string) *Source {
	if strings.TrimSpace(root) == "" {
		root = defaultRoot()
	}
	return &Source{
		Root:       root,
		MaxTextLen: DefaultMaxTextLen,
	}
}

// defaultRoot はホームディレクトリ配下の .claude を返す。
// ホームディレクトリが取得できない場合は空文字を返す
// （呼び出し側の Available() がエラーとして検出する）。
func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// Name は "claude-code" を返す。
func (s *Source) Name() string {
	return sourceName
}

// projectsDir は <root>/projects の絶対パスを返す。
func (s *Source) projectsDir() string {
	return filepath.Join(s.Root, "projects")
}

// Available は <root>/projects が存在し読める状態かを確認する。
func (s *Source) Available() error {
	if strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("claude code のログ置き場のパスが決定できません（ホームディレクトリの取得に失敗した可能性があります）")
	}

	dir := s.projectsDir()
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("claude code のログ置き場が見つかりません (%s): %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("claude code のログ置き場がディレクトリではありません: %s", dir)
	}
	if _, err := os.ReadDir(dir); err != nil {
		return fmt.Errorf("claude code のログ置き場を読み取れません (%s): %w", dir, err)
	}
	return nil
}

// memoryDirName は走査対象外のディレクトリ名（記憶ファイル置き場。jsonl は無い）。
const memoryDirName = "memory"

// subagentsDirName はサブエージェントの会話ログが入るサブディレクトリ名。
const subagentsDirName = "subagents"

// Discover は <root>/projects/ 配下のセッションを列挙する。実レイアウトは 2 種類ある:
//
//	<root>/projects/<project-slug>/<uuid>.jsonl                          … メインセッション
//	<root>/projects/<project-slug>/<uuid>/subagents/agent-<id>.jsonl     … サブエージェント（sidechain）
//
// <project-slug>/<uuid>/tool-results/ と <project-slug>/memory/ は走査しない。
// since がゼロ値でなければ ModTime が since 以降のものだけ返す。
// プロジェクトディレクトリ単位の読み取り失敗は警告ログを出して他を続行する。
func (s *Source) Discover(since time.Time) ([]source.Ref, error) {
	projectsDir := s.projectsDir()

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, fmt.Errorf("プロジェクト一覧の取得に失敗しました (%s): %w", projectsDir, err)
	}

	var refs []source.Ref
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, e.Name())

		files, err := os.ReadDir(projDir)
		if err != nil {
			slog.Warn("claude code: プロジェクトディレクトリの読み取りに失敗しました", "dir", projDir, "error", err)
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				// memory/ は無視。それ以外のディレクトリは <uuid>/subagents/ の可能性があるので覗く。
				if strings.EqualFold(f.Name(), memoryDirName) {
					continue
				}
				subRefs, err := discoverSubagents(filepath.Join(projDir, f.Name()), since)
				if err != nil {
					slog.Warn("claude code: サブエージェントディレクトリの読み取りに失敗しました", "dir", projDir, "session", f.Name(), "error", err)
					continue
				}
				refs = append(refs, subRefs...)
				continue
			}

			name := f.Name()
			if !strings.EqualFold(filepath.Ext(name), ".jsonl") {
				continue
			}

			full := filepath.Join(projDir, name)
			info, err := f.Info()
			if err != nil {
				slog.Warn("claude code: ファイル情報の取得に失敗しました", "path", full, "error", err)
				continue
			}

			if !since.IsZero() && info.ModTime().Before(since) {
				continue
			}

			refs = append(refs, source.Ref{
				Source:    sourceName,
				SessionID: strings.TrimSuffix(name, filepath.Ext(name)),
				Path:      full,
				ModTime:   info.ModTime(),
				Size:      info.Size(),
			})
		}
	}

	return refs, nil
}

// discoverSubagents は <project-slug>/<uuid>/subagents/agent-*.jsonl を列挙する。
// subagents/ が無い（サブエージェントを起動しなかったセッション）場合は空を返す。
// .meta.json は Ref にせず、Parse がパスから直接読む。
func discoverSubagents(sessionDir string, since time.Time) ([]source.Ref, error) {
	subDir := filepath.Join(sessionDir, subagentsDirName)

	files, err := os.ReadDir(subDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var refs []source.Ref
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if !strings.EqualFold(filepath.Ext(name), ".jsonl") {
			continue
		}

		full := filepath.Join(subDir, name)
		info, err := f.Info()
		if err != nil {
			slog.Warn("claude code: サブエージェントファイル情報の取得に失敗しました", "path", full, "error", err)
			continue
		}

		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}

		refs = append(refs, source.Ref{
			Source:    sourceName,
			SessionID: strings.TrimSuffix(name, filepath.Ext(name)),
			Path:      full,
			ModTime:   info.ModTime(),
			Size:      info.Size(),
		})
	}
	return refs, nil
}
