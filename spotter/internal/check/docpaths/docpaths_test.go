package docpaths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/check/docpaths"
	"github.com/fuchigta/spotter/internal/config"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestRunMissingPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "参照先は `internal/cli/root.go` です。\n")

	c, err := docpaths.New(config.CheckConfig{Docs: []string{"README.md"}})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("違反は 1 件のはず, got %d", len(violations))
	}
	if got := violations[0].Files; len(got) != 1 || got[0] != "internal/cli/root.go" {
		t.Errorf("Files = %v", got)
	}
}

func TestRunExistingPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/cli/root.go", "package cli\n")
	writeFile(t, root, "README.md", "参照先は `internal/cli/root.go` です。\n")

	c, err := docpaths.New(config.CheckConfig{Docs: []string{"README.md"}})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("実在すれば違反は出ないはず, got %v", violations)
	}
}

func TestRunGlobPattern(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/cli/root.go", "package cli\n")
	writeFile(t, root, "README.md", "参照先は `internal/cli/*.go` です。\n")

	c, err := docpaths.New(config.CheckConfig{Docs: []string{"README.md"}})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("グロブに 1 つでも一致すれば違反は出ないはず, got %v", violations)
	}
}

func TestRunIgnoreList(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "将来の拡張点は `internal/source/codex` です。\n")

	c, err := docpaths.New(config.CheckConfig{
		Docs:   []string{"README.md"},
		Ignore: []string{"internal/source/codex"},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("ignore に載っていれば違反は出ないはず, got %v", violations)
	}
}

func TestRunIgnoresNonPathBackticks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "コマンドは `spotter check` を使います。\n")

	c, err := docpaths.New(config.CheckConfig{Docs: []string{"README.md"}})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("internal/cmd/scripts/.githooks/.github で始まらない候補は無視するはず, got %v", violations)
	}
}

func TestRunDefaultDocs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "参照先は `internal/missing.go` です。\n")
	writeFile(t, root, "docs/guide.md", "参照先は `cmd/missing.go` です。\n")

	c, err := docpaths.New(config.CheckConfig{})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	violations, err := c.Run(check.Context{Root: root})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("既定のドキュメント一式（README.md と docs/*.md）を見るはず, got %d件: %v", len(violations), violations)
	}
}

func TestGranularity(t *testing.T) {
	c, err := docpaths.New(config.CheckConfig{})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if c.Granularity() != check.GranularityWorktree {
		t.Errorf("doc-paths の granularity は worktree 固定のはず, got %v", c.Granularity())
	}
}
