package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/source/codex"
	"github.com/spf13/cobra"
)

// staleLogThreshold を超えて最古の jsonl が古い場合、取りこぼしの警告を出す。
// Claude Code のログは約30日で自動削除されるため、余裕を持たせて25日とする。
const staleLogThreshold = 25 * 24 * time.Hour

// doctorResult は `config doctor` の診断結果全体。--json ではこの構造体をそのまま出す。
type doctorResult struct {
	OK               bool                `json:"ok"`
	ConfigPath       string              `json:"config_path"`
	ConfigExists     bool                `json:"config_exists"`
	ValidationErrors []string            `json:"validation_errors"`
	Tools            []toolCheck         `json:"tools"`
	ClaudeCodeLogs   claudeCodeLogsCheck `json:"claude_code_logs"`
	CodexLogs        codexLogsCheck      `json:"codex_logs"`
	Output           writeCheck          `json:"output"`
	Database         writeCheck          `json:"database"`
}

// toolCheck は外部コマンド 1 つの検出結果。
type toolCheck struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Found    bool   `json:"found"`
	Path     string `json:"path,omitempty"`
}

// claudeCodeLogsCheck は Claude Code の jsonl ログの状態。
type claudeCodeLogsCheck struct {
	Enabled       bool   `json:"enabled"`
	Root          string `json:"root,omitempty"`
	ProjectsDir   string `json:"projects_dir,omitempty"`
	ProjectsFound bool   `json:"projects_dir_found"`
	JSONLCount    int    `json:"jsonl_count"`
	OldestFile    string `json:"oldest_file,omitempty"` // RFC3339
	OldestAgeDays int    `json:"oldest_age_days,omitempty"`
	StaleWarning  bool   `json:"stale_warning"`
	Error         string `json:"error,omitempty"`
}

// codexLogsCheck は Codex のロールアウトログの状態。
//
// Claude Code と違い、Codex のログには自動削除が無い（7 日以上経つと zstd に
// 圧縮されるだけ）。そのため「最古のログが古い」ことは警告にならず、代わりに
// 圧縮済みの件数を出して、読めている（＝圧縮版も取り込める）ことを示す。
type codexLogsCheck struct {
	Enabled         bool   `json:"enabled"`
	Root            string `json:"root,omitempty"`
	SessionsDir     string `json:"sessions_dir,omitempty"`
	SessionsFound   bool   `json:"sessions_dir_found"`
	RolloutCount    int    `json:"rollout_count"`
	CompressedCount int    `json:"compressed_count"`
	NewestFile      string `json:"newest_file,omitempty"` // RFC3339
	Error           string `json:"error,omitempty"`
}

// writeCheck は出力先ディレクトリ／DBパスの書き込み可否。
type writeCheck struct {
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
	Error    string `json:"error,omitempty"`
}

// newConfigDoctorCommand は `insights config doctor` を組み立てる。
func newConfigDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "設定・外部コマンド・ログの状態を診断する",
		Long: "設定ファイルの妥当性、claude/codex/git/gh/glab の疎通、Claude Code ログの取りこぼしリスク、\n" +
			"Codex ログの検出状況、出力先の書き込み可否をまとめて確認する。\n" +
			"致命的な設定エラーがない限り終了コードは 0。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}
			path, err := ConfigPathFromContext(cmd)
			if err != nil {
				return err
			}

			result := doctorResult{ConfigPath: path, ValidationErrors: []string{}}

			if _, statErr := os.Stat(path); statErr == nil {
				result.ConfigExists = true
			}

			for _, e := range cfg.Validate() {
				result.ValidationErrors = append(result.ValidationErrors, e.Error())
			}

			result.Tools = checkTools()
			result.ClaudeCodeLogs = checkClaudeCodeLogs(cfg)
			result.CodexLogs = checkCodexLogs(cfg)
			result.Output = checkWritable(cfg.Output.Dir, true)
			result.Database = checkWritable(cfg.Database, false)
			result.OK = len(result.ValidationErrors) == 0

			if err := PrintResult(cmd, func(w io.Writer) error {
				return renderDoctorHuman(w, result)
			}, result); err != nil {
				return err
			}

			if !result.OK {
				return ErrDoctorProblems
			}
			return nil
		},
	}
	return cmd
}

// checkTools は claude/git/gh/glab/codex の有無を exec.LookPath で確認する。
// 見つからなくてもエラーにはせず、結果に「見つからない」旨を記録するだけ。
func checkTools() []toolCheck {
	candidates := []struct {
		name     string
		required bool
	}{
		{"claude", true},
		{"git", true},
		{"gh", false},
		{"glab", false},
		{"codex", false},
	}

	results := make([]toolCheck, 0, len(candidates))
	for _, c := range candidates {
		tc := toolCheck{Name: c.name, Required: c.required}
		if p, err := exec.LookPath(c.name); err == nil {
			tc.Found = true
			tc.Path = p
		}
		results = append(results, tc)
	}
	return results
}

// checkClaudeCodeLogs は sources.claude-code.root 配下の projects/**/*.jsonl を数え、
// 最古のファイルの更新日時から取りこぼしの警告を判定する。
func checkClaudeCodeLogs(cfg *config.Config) claudeCodeLogsCheck {
	result := claudeCodeLogsCheck{Enabled: cfg.Sources.ClaudeCode.Enabled}
	if !result.Enabled {
		return result
	}

	root, err := config.ExpandPath(cfg.Sources.ClaudeCode.Root)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Root = root

	projectsDir := filepath.Join(root, "projects")
	result.ProjectsDir = projectsDir

	info, statErr := os.Stat(projectsDir)
	if statErr != nil || !info.IsDir() {
		result.ProjectsFound = false
		return result
	}
	result.ProjectsFound = true

	var count int
	var oldest time.Time
	walkErr := filepath.WalkDir(projectsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 個別のエラーは無視してベストエフォートで走査を続ける。
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".jsonl") {
			return nil
		}
		count++
		if fi, infoErr := d.Info(); infoErr == nil {
			if oldest.IsZero() || fi.ModTime().Before(oldest) {
				oldest = fi.ModTime()
			}
		}
		return nil
	})
	if walkErr != nil {
		result.Error = walkErr.Error()
	}

	result.JSONLCount = count
	if !oldest.IsZero() {
		result.OldestFile = oldest.Format(time.RFC3339)
		age := time.Since(oldest)
		result.OldestAgeDays = int(age.Hours() / 24)
		if age > staleLogThreshold {
			result.StaleWarning = true
		}
	}
	return result
}

// checkCodexLogs は sources.codex.root 配下の sessions/**/rollout-*.jsonl[.zst] を数える。
//
// 走査対象のパスは source/codex 側に決めさせる（root が空のときの $CODEX_HOME →
// ~/.codex という解決も含む）。ここでパスを組み直すと、取り込みが実際に見る場所と
// 診断が見る場所がずれて、診断が嘘をつくようになる。
func checkCodexLogs(cfg *config.Config) codexLogsCheck {
	result := codexLogsCheck{Enabled: cfg.Sources.Codex.Enabled}
	if !result.Enabled {
		return result
	}

	root, err := config.ExpandPath(cfg.Sources.Codex.Root)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	src := codex.New(root)
	result.Root = src.Root
	result.SessionsDir = src.SessionsDir()

	info, statErr := os.Stat(result.SessionsDir)
	if statErr != nil || !info.IsDir() {
		result.SessionsFound = false
		return result
	}
	result.SessionsFound = true

	refs, discoverErr := src.Discover(time.Time{})
	if discoverErr != nil {
		result.Error = discoverErr.Error()
		return result
	}

	var newest time.Time
	for _, ref := range refs {
		result.RolloutCount++
		if strings.HasSuffix(ref.Path, ".jsonl.zst") {
			result.CompressedCount++
		}
		if ref.ModTime.After(newest) {
			newest = ref.ModTime
		}
	}
	if !newest.IsZero() {
		result.NewestFile = newest.Format(time.RFC3339)
	}
	return result
}

// checkWritable は path（isDir なら path 自体、そうでなければその親）を対象に、
// 実在する最も近い祖先ディレクトリへ一時ファイルを作成できるかで書き込み可否を判定する。
// 対象ディレクトリ自体は作成しない（doctor に副作用を持たせないため）。
func checkWritable(rawPath string, isDir bool) writeCheck {
	result := writeCheck{}

	expanded, err := config.ExpandPath(rawPath)
	if err != nil {
		result.Path = rawPath
		result.Error = err.Error()
		return result
	}
	result.Path = expanded

	target := expanded
	if !isDir {
		target = filepath.Dir(expanded)
	}

	dir := target
	for {
		info, statErr := os.Stat(dir)
		if statErr == nil {
			if !info.IsDir() {
				result.Error = fmt.Sprintf("%s はディレクトリではありません", dir)
				return result
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			result.Error = statErr.Error()
			return result
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			result.Error = "書き込み可能な親ディレクトリが見つかりません"
			return result
		}
		dir = parent
	}

	f, err := os.CreateTemp(dir, ".insights-doctor-*")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)

	result.Writable = true
	return result
}

// renderDoctorHuman は doctorResult を人間向けに整形して w に書き出す。
func renderDoctorHuman(w io.Writer, r doctorResult) error {
	fmt.Fprintln(w, "=== insights config doctor ===")
	fmt.Fprintln(w)

	fmt.Fprintf(w, "設定ファイル: %s\n", r.ConfigPath)
	if r.ConfigExists {
		fmt.Fprintln(w, "  状態: 存在します")
	} else {
		fmt.Fprintln(w, "  状態: 見つかりません（既定値で動作中。`insights config init` で作成できます）")
	}
	if len(r.ValidationErrors) == 0 {
		fmt.Fprintln(w, "  検証: 問題なし")
	} else {
		fmt.Fprintln(w, "  検証: 問題あり")
		for _, e := range r.ValidationErrors {
			fmt.Fprintf(w, "    - %s\n", e)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "外部コマンド:")
	for _, t := range r.Tools {
		if t.Found {
			fmt.Fprintf(w, "  - %-6s 利用可能 (%s)\n", t.Name, t.Path)
			continue
		}
		note := ""
		if !t.Required {
			note = "（任意）"
		}
		fmt.Fprintf(w, "  - %-6s 見つかりません%s\n", t.Name, note)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Claude Code ログ:")
	lc := r.ClaudeCodeLogs
	switch {
	case !lc.Enabled:
		fmt.Fprintln(w, "  sources.claude-code は無効化されています")
	case lc.Error != "":
		fmt.Fprintf(w, "  エラー: %s\n", lc.Error)
	case !lc.ProjectsFound:
		fmt.Fprintf(w, "  projects ディレクトリが見つかりません: %s\n", lc.ProjectsDir)
	default:
		fmt.Fprintf(w, "  projects ディレクトリ: %s\n", lc.ProjectsDir)
		fmt.Fprintf(w, "  jsonl 件数: %d\n", lc.JSONLCount)
		if lc.OldestFile != "" {
			fmt.Fprintf(w, "  最古のファイル: %s (%d 日前)\n", lc.OldestFile, lc.OldestAgeDays)
		}
		if lc.StaleWarning {
			fmt.Fprintln(w, "  警告: 最古のログが25日以上前です。Claude Code のログは約30日で自動削除されるため、")
			fmt.Fprintln(w, "        取りこぼす前に `insights ingest` を実行してください。")
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Codex ログ:")
	cx := r.CodexLogs
	switch {
	case !cx.Enabled:
		fmt.Fprintln(w, "  sources.codex は無効化されています")
	case cx.Error != "":
		fmt.Fprintf(w, "  エラー: %s\n", cx.Error)
	case !cx.SessionsFound:
		fmt.Fprintf(w, "  sessions ディレクトリが見つかりません: %s（Codex を使っていなければ問題ありません）\n", cx.SessionsDir)
	default:
		fmt.Fprintf(w, "  sessions ディレクトリ: %s\n", cx.SessionsDir)
		fmt.Fprintf(w, "  ロールアウト件数: %d（うち圧縮済み %d）\n", cx.RolloutCount, cx.CompressedCount)
		if cx.NewestFile != "" {
			fmt.Fprintf(w, "  最新のファイル: %s\n", cx.NewestFile)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "書き込み確認:")
	printWriteCheck(w, "出力ディレクトリ", r.Output)
	printWriteCheck(w, "データベース", r.Database)
	fmt.Fprintln(w)

	if r.OK {
		fmt.Fprintln(w, "総合判定: OK")
	} else {
		fmt.Fprintln(w, "総合判定: 致命的な設定エラーがあります（上記の検証結果を参照してください）")
	}
	return nil
}

func printWriteCheck(w io.Writer, label string, c writeCheck) {
	if c.Error != "" {
		fmt.Fprintf(w, "  - %s (%s): 確認できません - %s\n", label, c.Path, c.Error)
		return
	}
	status := "書き込み不可"
	if c.Writable {
		status = "書き込み可"
	}
	fmt.Fprintf(w, "  - %s (%s): %s\n", label, c.Path, status)
}
