package consistency_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/check/consistency"
	"github.com/fuchigta/spotter/internal/config"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// commitTypesConfig は scripts/check-commit-types.sh が突き合わせる 3 箇所を再現した設定。
func commitTypesConfig() config.CheckConfig {
	return config.CheckConfig{
		Sources: []config.ConsistencySource{
			{
				File:    "cliff.toml",
				Line:    `message = .\^[A-Za-z]+.`,
				Extract: `\^([A-Za-z]+)`,
			},
			{
				File:    "check-commit-subject.sh",
				Line:    `^PATTERN=`,
				Extract: `\(([a-zA-Z|]+)\)`,
				Split:   "|",
			},
			{
				File:    "CLAUDE.md",
				Line:    "^\\| `[a-zA-Z]+` \\|",
				Extract: "`([a-zA-Z]+)`",
			},
		},
	}
}

func TestRunConsistent(t *testing.T) {
	root := t.TempDir()
	// chore(release) はスコープ限定の抑制指定であり、type としては通常の chore 行と
	// 同じ "chore" を抽出する（docs/hooks-extraction.md 想定の挙動）。3 箇所とも
	// chore を含めておくことでこの重複が無害であることも確認する。
	writeFile(t, root, "cliff.toml", `
commit_parsers = [
  { message = '^feat', group = 'Features' },
  { message = '^fix', group = 'Fixes' },
  { message = '^chore', group = 'Miscellaneous' },
  { message = '^chore\(release\)', skip = true },
  { message = '.*', group = 'その他' },
]
`)
	writeFile(t, root, "check-commit-subject.sh", `PATTERN='^(feat|fix|chore)(\([a-zA-Z0-9._/-]+\))?!?: .+'`+"\n")
	writeFile(t, root, "CLAUDE.md", "| `feat` | 機能追加 |\n| `fix` | 不具合修正 |\n| `chore` | 雑務 |\n")

	c, err := consistency.New(commitTypesConfig())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("3 箇所が一致していれば違反は出ないはず, got %v", violations)
	}
}

func TestRunMismatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cliff.toml", `
commit_parsers = [
  { message = '^feat', group = 'Features' },
  { message = '^fix', group = 'Fixes' },
  { message = '^perf', group = 'Performance' },
]
`)
	writeFile(t, root, "check-commit-subject.sh", `PATTERN='^(feat|fix)(\([a-zA-Z0-9._/-]+\))?!?: .+'`+"\n")
	writeFile(t, root, "CLAUDE.md", "| `feat` | 機能追加 |\n| `fix` | 不具合修正 |\n")

	c, err := consistency.New(commitTypesConfig())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("cliff.toml だけ perf を持つので 2 件（subject 側・CLAUDE.md 側）出るはず, got %d: %v", len(violations), violations)
	}
}

func TestRunEmptyExtractionIsError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cliff.toml", "何も一致しない内容\n")
	writeFile(t, root, "check-commit-subject.sh", `PATTERN='^(feat|fix)(\([a-zA-Z0-9._/-]+\))?!?: .+'`+"\n")
	writeFile(t, root, "CLAUDE.md", "| `feat` | 機能追加 |\n")

	c, err := consistency.New(commitTypesConfig())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if _, err := c.Run(check.Context{Root: root}); err == nil {
		t.Fatal("抽出結果が空なら Run() はエラーになるはず")
	}
}

func TestNewRequiresAtLeastTwoSources(t *testing.T) {
	if _, err := consistency.New(config.CheckConfig{
		Sources: []config.ConsistencySource{{File: "a", Extract: "(x)"}},
	}); err == nil {
		t.Fatal("sources が 1 つなら New() はエラーになるはず")
	}
}

func TestNewRequiresCaptureGroup(t *testing.T) {
	if _, err := consistency.New(config.CheckConfig{
		Sources: []config.ConsistencySource{
			{File: "a", Extract: "x"},
			{File: "b", Extract: "(y)"},
		},
	}); err == nil {
		t.Fatal("キャプチャグループが無ければ New() はエラーになるはず")
	}
}

func TestGranularity(t *testing.T) {
	c, err := consistency.New(commitTypesConfig())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if c.Granularity() != check.GranularityWorktree {
		t.Errorf("consistency の granularity は worktree 固定のはず, got %v", c.Granularity())
	}
}
