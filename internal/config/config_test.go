package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Language != "ja" {
		t.Errorf("Language = %q, want %q", cfg.Language, "ja")
	}
	if !cfg.Sources.ClaudeCode.Enabled {
		t.Error("Sources.ClaudeCode.Enabled = false, want true")
	}
	if cfg.Sources.ClaudeCode.Root != "~/.claude" {
		t.Errorf("Sources.ClaudeCode.Root = %q, want %q", cfg.Sources.ClaudeCode.Root, "~/.claude")
	}
	if !cfg.Sources.Codex.Enabled {
		t.Error("Sources.Codex.Enabled = false, want true")
	}
	// codex の root は既定で空にしてある。Codex は $CODEX_HOME でログ置き場を移せるため、
	// "~/.codex" を書き込むと移している利用者のログを取りこぼす（解決は実行時に行う）。
	if cfg.Sources.Codex.Root != "" {
		t.Errorf("Sources.Codex.Root = %q, want 空（実行時に $CODEX_HOME → ~/.codex で解決する）", cfg.Sources.Codex.Root)
	}
	if cfg.Judge.Backend != "claude-cli" {
		t.Errorf("Judge.Backend = %q, want %q", cfg.Judge.Backend, "claude-cli")
	}
	if cfg.Judge.Concurrency != 3 {
		t.Errorf("Judge.Concurrency = %d, want 3", cfg.Judge.Concurrency)
	}
	if cfg.Judge.Timeout.Duration != 180*time.Second {
		t.Errorf("Judge.Timeout = %s, want 180s", cfg.Judge.Timeout.Duration)
	}
	if cfg.Evidence.Gh != TristateAuto || cfg.Evidence.Glab != TristateAuto {
		t.Errorf("Evidence.Gh/Glab = %q/%q, want auto/auto", cfg.Evidence.Gh, cfg.Evidence.Glab)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("Default().Validate() = %v, want empty", errs)
	}
}

func TestDefaultPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".insights", "config.yaml")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "config.yaml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Language != Default().Language {
		t.Errorf("Load() on missing file = %+v, want defaults", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := Default()
	cfg.Language = "en"
	cfg.Judge.Concurrency = 5
	cfg.Judge.Timeout = Duration{Duration: 90 * time.Second}
	cfg.Evidence.Gh = TristateTrue
	cfg.Evidence.Glab = TristateFalse
	cfg.Exclude.Projects = []string{`C:\Users\me\project`}
	cfg.Goals.Global = "global goal"
	cfg.Goals.Projects = map[string]string{`C:\Users\me\project`: "project goal"}
	cfg.Pricing.Overrides = map[string]ModelPrice{
		"claude-sonnet-5": {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Language != cfg.Language {
		t.Errorf("Language round-trip: got %q, want %q", got.Language, cfg.Language)
	}
	if got.Judge.Concurrency != cfg.Judge.Concurrency {
		t.Errorf("Judge.Concurrency round-trip: got %d, want %d", got.Judge.Concurrency, cfg.Judge.Concurrency)
	}
	if got.Judge.Timeout.Duration != cfg.Judge.Timeout.Duration {
		t.Errorf("Judge.Timeout round-trip: got %s, want %s", got.Judge.Timeout.Duration, cfg.Judge.Timeout.Duration)
	}
	if got.Evidence.Gh != cfg.Evidence.Gh || got.Evidence.Glab != cfg.Evidence.Glab {
		t.Errorf("Evidence.Gh/Glab round-trip: got %q/%q, want %q/%q", got.Evidence.Gh, got.Evidence.Glab, cfg.Evidence.Gh, cfg.Evidence.Glab)
	}
	if len(got.Exclude.Projects) != 1 || got.Exclude.Projects[0] != cfg.Exclude.Projects[0] {
		t.Errorf("Exclude.Projects round-trip: got %v, want %v", got.Exclude.Projects, cfg.Exclude.Projects)
	}
	if got.Goals.Global != cfg.Goals.Global {
		t.Errorf("Goals.Global round-trip: got %q, want %q", got.Goals.Global, cfg.Goals.Global)
	}
	if got.Pricing.Overrides["claude-sonnet-5"] != cfg.Pricing.Overrides["claude-sonnet-5"] {
		t.Errorf("Pricing.Overrides round-trip: got %+v, want %+v", got.Pricing.Overrides["claude-sonnet-5"], cfg.Pricing.Overrides["claude-sonnet-5"])
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "config.yaml")

	if err := Default().Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Save() did not create file: %v", err)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tilde only", "~", filepath.Clean(home)},
		{"tilde slash", "~/.insights/config.yaml", filepath.Clean(filepath.Join(home, ".insights", "config.yaml"))},
		{"home var", "$HOME/.insights", filepath.Clean(filepath.Join(home, ".insights"))},
		{"absolute unchanged", filepath.Join(home, "foo"), filepath.Clean(filepath.Join(home, "foo"))},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandPath(tc.in)
			if err != nil {
				t.Fatalf("ExpandPath(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExcludesProject(t *testing.T) {
	cfg := Default()
	cfg.Exclude.Projects = []string{`C:\Users\fuchigta\src\github.com\fuchigta\insights`}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"exact match", `C:\Users\fuchigta\src\github.com\fuchigta\insights`, true},
		{"case insensitive", `c:\users\fuchigta\src\github.com\fuchigta\INSIGHTS`, true},
		{"forward slashes", `C:/Users/fuchigta/src/github.com/fuchigta/insights`, true},
		{"trailing slash", `C:\Users\fuchigta\src\github.com\fuchigta\insights\`, true},
		{"no match", `C:\Users\fuchigta\src\github.com\fuchigta\other`, false},
		// 除外したい単位はディレクトリなので、その配下も除外に含める。
		{"under the excluded dir", `C:\Users\fuchigta\src\github.com\fuchigta\insights\internal\cli`, true},
		{"under, forward slashes", `C:/Users/fuchigta/src/github.com/fuchigta/insights/docs`, true},
		// 前方一致だけだと "insights-old" のような兄弟まで巻き込むので境界を見る。
		{"sibling with same prefix", `C:\Users\fuchigta\src\github.com\fuchigta\insights-old`, false},
		// 親ディレクトリは配下ではない。
		{"parent of the excluded dir", `C:\Users\fuchigta\src\github.com\fuchigta`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.ExcludesProject(tc.path); got != tc.want {
				t.Errorf("ExcludesProject(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// 実際に効いてほしいのは「一時ディレクトリごと除外する」という書き方。
// 配下の作業ディレクトリが 1 つずつ別プロジェクトとして記録されるため、
// 親を 1 行書けば全部落ちること。
func TestExcludesProjectUnderTempDir(t *testing.T) {
	cfg := Default()
	cfg.Exclude.Projects = []string{"~/AppData/Local/Temp"}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("ホームディレクトリを取得できないためスキップします")
	}

	under := filepath.Join(home, "AppData", "Local", "Temp", "claude_judge_workdir_debug")
	if !cfg.ExcludesProject(under) {
		t.Errorf("ExcludesProject(%q) = false, want true（~ 展開 + 配下判定）", under)
	}
	self := filepath.Join(home, "AppData", "Local", "Temp")
	if !cfg.ExcludesProject(self) {
		t.Errorf("ExcludesProject(%q) = false, want true（ディレクトリ自身）", self)
	}
	outside := filepath.Join(home, "AppData", "Local", "TempFiles")
	if cfg.ExcludesProject(outside) {
		t.Errorf("ExcludesProject(%q) = true, want false（名前が前方一致するだけの別ディレクトリ）", outside)
	}
}

func TestGoalFor(t *testing.T) {
	cfg := Default()
	cfg.Goals.Global = "global goal"
	cfg.Goals.Projects = map[string]string{
		`C:\Users\fuchigta\src\github.com\fuchigta\life`: "生活の自動化",
	}

	if got := cfg.GoalFor(`C:\Users\fuchigta\src\github.com\fuchigta\life`); got != "生活の自動化" {
		t.Errorf("GoalFor(matched) = %q, want %q", got, "生活の自動化")
	}
	if got := cfg.GoalFor(`c:/users/fuchigta/src/github.com/fuchigta/life/`); got != "生活の自動化" {
		t.Errorf("GoalFor(normalized match) = %q, want %q", got, "生活の自動化")
	}
	if got := cfg.GoalFor(`C:\Users\fuchigta\other`); got != "global goal" {
		t.Errorf("GoalFor(unmatched) = %q, want global goal", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{"defaults ok", func(c *Config) {}, false},
		{"empty language", func(c *Config) { c.Language = "" }, true},
		{"empty backend", func(c *Config) { c.Judge.Backend = "" }, true},
		{"zero concurrency", func(c *Config) { c.Judge.Concurrency = 0 }, true},
		{"negative timeout", func(c *Config) { c.Judge.Timeout = Duration{Duration: -1} }, true},
		{"invalid gh", func(c *Config) { c.Evidence.Gh = Tristate("maybe") }, true},
		{"invalid glab", func(c *Config) { c.Evidence.Glab = Tristate("maybe") }, true},
		{"negative max body chars", func(c *Config) { c.Evidence.MaxBodyChars = -1 }, true},
		{"empty output dir", func(c *Config) { c.Output.Dir = "" }, true},
		{"empty database", func(c *Config) { c.Database = "" }, true},
		{"negative pricing override", func(c *Config) {
			c.Pricing.Overrides = map[string]ModelPrice{"m": {Input: -1}}
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			errs := cfg.Validate()
			if tc.wantErr && len(errs) == 0 {
				t.Errorf("Validate() = empty, want errors")
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Errorf("Validate() = %v, want empty", errs)
			}
		})
	}
}

func TestTristateEnabled(t *testing.T) {
	cases := []struct {
		t     Tristate
		found bool
		want  bool
	}{
		{TristateTrue, false, true},
		{TristateTrue, true, true},
		{TristateFalse, true, false},
		{TristateFalse, false, false},
		{TristateAuto, true, true},
		{TristateAuto, false, false},
	}
	for _, tc := range cases {
		if got := tc.t.Enabled(tc.found); got != tc.want {
			t.Errorf("%q.Enabled(%v) = %v, want %v", tc.t, tc.found, got, tc.want)
		}
	}
}
