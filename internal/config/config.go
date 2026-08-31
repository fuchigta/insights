// Package config は ~/.insights/config.yaml のロード・保存・既定値を扱う。
// 他パッケージはこの構造体を通じて設定を参照する。internal/store・
// internal/source/claudecode・internal/pricing には依存しない
// （それらの型が必要な箇所は呼び出し側で変換する）。
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config は ~/.insights/config.yaml の内容そのものを表す。
type Config struct {
	Language string         `yaml:"language"`
	Sources  SourcesConfig  `yaml:"sources"`
	Judge    JudgeConfig    `yaml:"judge"`
	Evidence EvidenceConfig `yaml:"evidence"`
	Output   OutputConfig   `yaml:"output"`
	Report   ReportConfig   `yaml:"report"`
	Database string         `yaml:"database"`
	Exclude  ExcludeConfig  `yaml:"exclude"`
	Goals    GoalsConfig    `yaml:"goals"`
	Pricing  PricingConfig  `yaml:"pricing"`
}

// SourcesConfig はログソースごとの設定。将来 codex などを追加する。
type SourcesConfig struct {
	ClaudeCode ClaudeCodeSource `yaml:"claude-code"`
}

// ClaudeCodeSource は Claude Code ログの置き場設定。
type ClaudeCodeSource struct {
	Root    string `yaml:"root"` // ~/.claude 相当。projects/ サブディレクトリを見る
	Enabled bool   `yaml:"enabled"`
}

// JudgeConfig は AI 評価バックエンドの設定。
type JudgeConfig struct {
	Backend     string   `yaml:"backend"` // "claude-cli" など。judge.Judge の Name() と対応
	Model       string   `yaml:"model"`
	Concurrency int      `yaml:"concurrency"`
	Timeout     Duration `yaml:"timeout"`
}

// EvidenceConfig は成果物（git/gh/glab）収集の設定。
type EvidenceConfig struct {
	Git          bool     `yaml:"git"`
	Gh           Tristate `yaml:"gh"`   // true/false/auto
	Glab         Tristate `yaml:"glab"` // true/false/auto
	MaxBodyChars int      `yaml:"max_body_chars"`

	// GithubHosts / GitlabHosts は SaaS 以外のホスト（GitHub Enterprise Server・
	// セルフホスト GitLab）を明示するための一覧。origin の host がここに載って
	// いれば、ホスト名から推測できなくても対応する CLI を使う。
	// 例: ["ghe.example.com"] / ["git.example.co.jp"]
	GithubHosts []string `yaml:"github_hosts"`
	GitlabHosts []string `yaml:"gitlab_hosts"`
}

// ReportConfig はレポートの見せ方に関する設定。
type ReportConfig struct {
	Rollup RollupConfig `yaml:"rollup"`
}

// RollupConfig は「価値への寄与が薄い小さなセッションを個別に列挙せず畳む」
// ためのしきい値。両方を下回るセッションだけが丸められる（どちらか一方でも
// 大きければ個別に載せる）。0 なら集計側の既定値が使われる。
type RollupConfig struct {
	// CostShare はその日の総コストに対する割合（0..1）。
	CostShare float64 `yaml:"cost_share"`
	// DurationMinutes はセッションの所要時間（分）。
	DurationMinutes float64 `yaml:"duration_minutes"`
}

// OutputConfig はレポート出力先の設定。
type OutputConfig struct {
	Dir string `yaml:"dir"`
}

// ExcludeConfig は評価対象から除外するプロジェクト・entrypoint。
type ExcludeConfig struct {
	Projects    []string `yaml:"projects"`
	Entrypoints []string `yaml:"entrypoints"`
}

// GoalsConfig はレポートの評価軸に使う「重視する価値」の設定。
type GoalsConfig struct {
	Global   string            `yaml:"global"`
	Projects map[string]string `yaml:"projects"`
}

// PricingConfig はモデル単価の上書き設定。
// internal/pricing の型には依存せず、ここで独自に定義する。
type PricingConfig struct {
	Overrides map[string]ModelPrice `yaml:"overrides"`
}

// ModelPrice は 1 モデルあたりの単価上書き。単位は internal/pricing 側の規約に合わせる
// （100万トークンあたりのドルなど）想定だが、config パッケージはその意味を解釈しない。
type ModelPrice struct {
	Input        float64 `yaml:"input"`
	Output       float64 `yaml:"output"`
	CacheWrite5m float64 `yaml:"cache_write_5m"`
	CacheWrite1h float64 `yaml:"cache_write_1h"`
	CacheRead    float64 `yaml:"cache_read"`
}

// Default は既定値を埋めた Config を返す。
func Default() *Config {
	return &Config{
		Language: "ja",
		Sources: SourcesConfig{
			ClaudeCode: ClaudeCodeSource{
				Root:    "~/.claude",
				Enabled: true,
			},
		},
		Judge: JudgeConfig{
			Backend:     "claude-cli",
			Model:       "claude-sonnet-5",
			Concurrency: 3,
			Timeout:     Duration{Duration: 180 * time.Second},
		},
		Evidence: EvidenceConfig{
			Git:          true,
			Gh:           TristateAuto,
			Glab:         TristateAuto,
			MaxBodyChars: 4000,
			GithubHosts:  []string{},
			GitlabHosts:  []string{},
		},
		Output: OutputConfig{
			Dir: "~/.insights/reports",
		},
		Report: ReportConfig{
			Rollup: RollupConfig{
				CostShare:       0.01,
				DurationMinutes: 10,
			},
		},
		Database: "~/.insights/insights.db",
		Exclude: ExcludeConfig{
			Projects:    []string{},
			Entrypoints: []string{},
		},
		Goals: GoalsConfig{
			Global:   "",
			Projects: map[string]string{},
		},
		Pricing: PricingConfig{
			Overrides: map[string]ModelPrice{},
		},
	}
}

// DefaultPath は既定の設定ファイルパス（~/.insights/config.yaml）を返す。
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
	}
	return filepath.Join(home, ".insights", "config.yaml"), nil
}

// Load は path から設定を読み込む。path が空なら DefaultPath() を使う。
// ファイルが存在しない場合はエラーにせず Default() を返す。
// 存在する場合は既定値の上に yaml の内容を重ねる（未指定のキーは既定値のまま残る）。
func Load(path string) (*Config, error) {
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("設定ファイルの読み込みに失敗しました: %w", err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("設定ファイルの解析に失敗しました (%s): %w", resolved, err)
	}
	return cfg, nil
}

// Save は c を path に書き出す。必要なディレクトリは作成する。
// 既存ファイルを上書きするかどうかは呼び出し側が判断すること
// （この関数自体は常に上書きする）。
func (c *Config) Save(path string) error {
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return err
	}

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("設定ディレクトリの作成に失敗しました (%s): %w", dir, err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("設定のシリアライズに失敗しました: %w", err)
	}

	if err := os.WriteFile(resolved, data, 0o644); err != nil {
		return fmt.Errorf("設定ファイルの書き込みに失敗しました (%s): %w", resolved, err)
	}
	return nil
}

// resolveConfigPath は空パスを DefaultPath() に、~ や $HOME を含むパスを展開する。
func resolveConfigPath(path string) (string, error) {
	if path == "" {
		return DefaultPath()
	}
	return ExpandPath(path)
}

// Validate は設定値の矛盾を列挙する。空スライスなら問題なし。
func (c *Config) Validate() []error {
	var errs []error

	if strings.TrimSpace(c.Language) == "" {
		errs = append(errs, errors.New("language が空です"))
	}
	if c.Sources.ClaudeCode.Enabled && strings.TrimSpace(c.Sources.ClaudeCode.Root) == "" {
		errs = append(errs, errors.New("sources.claude-code.root が空です"))
	}
	if strings.TrimSpace(c.Judge.Backend) == "" {
		errs = append(errs, errors.New("judge.backend が空です"))
	}
	if c.Judge.Concurrency <= 0 {
		errs = append(errs, fmt.Errorf("judge.concurrency は 1 以上である必要があります: %d", c.Judge.Concurrency))
	}
	if c.Judge.Timeout.Duration <= 0 {
		errs = append(errs, fmt.Errorf("judge.timeout は正の値である必要があります: %s", c.Judge.Timeout.Duration))
	}
	if !c.Evidence.Gh.Valid() {
		errs = append(errs, fmt.Errorf("evidence.gh の値が不正です (true/false/auto のいずれか): %q", c.Evidence.Gh))
	}
	if !c.Evidence.Glab.Valid() {
		errs = append(errs, fmt.Errorf("evidence.glab の値が不正です (true/false/auto のいずれか): %q", c.Evidence.Glab))
	}
	if c.Evidence.MaxBodyChars < 0 {
		errs = append(errs, fmt.Errorf("evidence.max_body_chars は 0 以上である必要があります: %d", c.Evidence.MaxBodyChars))
	}
	if strings.TrimSpace(c.Output.Dir) == "" {
		errs = append(errs, errors.New("output.dir が空です"))
	}
	if strings.TrimSpace(c.Database) == "" {
		errs = append(errs, errors.New("database が空です"))
	}
	for name, price := range c.Pricing.Overrides {
		if price.Input < 0 || price.Output < 0 || price.CacheWrite5m < 0 || price.CacheWrite1h < 0 || price.CacheRead < 0 {
			errs = append(errs, fmt.Errorf("pricing.overrides[%s] に負の単価があります", name))
		}
	}

	return errs
}

// ExcludesProject は path が exclude.projects に含まれるかを判定する。
// 比較前に filepath.Clean + strings.EqualFold で正規化し、大文字小文字・
// パス区切りの差（Windows 前提）を吸収する。
func (c *Config) ExcludesProject(path string) bool {
	target := normalizeForCompare(path)
	if target == "" {
		return false
	}
	for _, p := range c.Exclude.Projects {
		if strings.EqualFold(normalizeForCompare(p), target) {
			return true
		}
	}
	return false
}

// GoalFor は projectPath に一致する goals.projects の値を返す。
// 一致しなければ goals.global を返す。
func (c *Config) GoalFor(projectPath string) string {
	target := normalizeForCompare(projectPath)
	if target != "" {
		for p, goal := range c.Goals.Projects {
			if strings.EqualFold(normalizeForCompare(p), target) {
				return goal
			}
		}
	}
	return c.Goals.Global
}

// normalizeForCompare はパス比較用に ~ 展開 + Clean を行う。展開に失敗した場合は
// 元の文字列を Clean するだけに留める（比較不能な値を握り潰さないため）。
func normalizeForCompare(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	expanded, err := ExpandPath(p)
	if err != nil {
		expanded = p
	}
	// 区切り文字を "/" に寄せてから正規化する。filepath.Clean は
	// 実行中の OS の区切り文字しか解釈しないため、Linux/macOS では
	// "C:\Users\..." の "\" が区切りとして扱われず、設定に書いた
	// Windows 形式のパスと突き合わせられない。insights は Windows の
	// ログを他 OS で解析することもあるので、OS に依存しない形で比較する。
	slashed := strings.ReplaceAll(expanded, `\`, "/")
	return path.Clean(slashed)
}

// ExpandPath は "~"・"~/..."・"$HOME" を os.UserHomeDir() で展開し、
// filepath.Clean した結果を返す。
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
	}

	expanded := os.Expand(p, func(key string) string {
		if key == "HOME" {
			return home
		}
		// 未知の環境変数参照はそのまま残す（誤って空文字に潰さない）。
		return "$" + key
	})

	switch {
	case expanded == "~":
		expanded = home
	case strings.HasPrefix(expanded, "~/"), strings.HasPrefix(expanded, `~\`):
		expanded = filepath.Join(home, expanded[2:])
	}

	return filepath.Clean(expanded), nil
}
