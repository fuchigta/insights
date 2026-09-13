package docsync_test

import (
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/check/docsync"
	"github.com/fuchigta/spotter/internal/config"
)

// fakeSource はテスト用の固定応答 check.Source。
type fakeSource struct {
	changed []string
	diffs   map[string]string
}

func (f fakeSource) ChangedFiles() ([]string, error) { return f.changed, nil }
func (f fakeSource) DiffLines(path string) (string, error) {
	return f.diffs[path], nil
}
func (f fakeSource) BlobSize(path string) (int64, error) { return 0, nil }

func mustNew(t *testing.T, cc config.CheckConfig) *docsync.Check {
	t.Helper()
	c, err := docsync.New(cc)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return c
}

func TestRunViolation(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{
			{Paths: "internal/cli/*.go", Doc: "README.md"},
		},
	})

	src := fakeSource{changed: []string{"internal/cli/root.go"}}
	violations, err := c.Run(check.Context{Source: src})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("違反が 1 件出るはず, got %d", len(violations))
	}
	if got := violations[0].Files; len(got) != 1 || got[0] != "internal/cli/root.go" {
		t.Errorf("Files = %v", got)
	}
}

func TestRunDocUpdatedTogether(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{
			{Paths: "internal/cli/*.go", Doc: "README.md"},
		},
	})

	src := fakeSource{changed: []string{"internal/cli/root.go", "README.md"}}
	violations, err := c.Run(check.Context{Source: src})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("ドキュメントも一緒に変更されていれば違反は出ないはず, got %v", violations)
	}
}

func TestRunTestFileExcludedAlways(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{
			{Paths: "internal/cli/*.go", Doc: "README.md"},
		},
	})

	src := fakeSource{changed: []string{"internal/cli/root_test.go"}}
	violations, err := c.Run(check.Context{Source: src})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("_test.go は常に対象外のはず, got %v", violations)
	}
}

func TestRunWhenRegexGatesFiring(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{
			{Paths: "internal/cli/*.go", Doc: "README.md", When: `^[+-].*Use:`},
		},
	})

	t.Run("正規表現に一致しない差分では発火しない", func(t *testing.T) {
		src := fakeSource{
			changed: []string{"internal/cli/root.go"},
			diffs:   map[string]string{"internal/cli/root.go": "+func run() {}\n"},
		}
		violations, err := c.Run(check.Context{Source: src})
		if err != nil {
			t.Fatalf("Run() error: %v", err)
		}
		if len(violations) != 0 {
			t.Errorf("when に一致しなければ違反は出ないはず, got %v", violations)
		}
	})

	t.Run("正規表現に一致する差分では発火する", func(t *testing.T) {
		src := fakeSource{
			changed: []string{"internal/cli/root.go"},
			diffs:   map[string]string{"internal/cli/root.go": `+	Use: "foo",` + "\n"},
		}
		violations, err := c.Run(check.Context{Source: src})
		if err != nil {
			t.Fatalf("Run() error: %v", err)
		}
		if len(violations) != 1 {
			t.Fatalf("when に一致すれば違反が出るはず, got %d", len(violations))
		}
	})
}

func TestRunExcludePattern(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{
			{Paths: "internal/cli/*.go", Doc: "README.md"},
		},
		Exclude: []string{"internal/cli/generated_*.go"},
	})

	src := fakeSource{changed: []string{"internal/cli/generated_foo.go"}}
	violations, err := c.Run(check.Context{Source: src})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("exclude に一致すれば違反は出ないはず, got %v", violations)
	}
}

func TestRunNoChanges(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Pairs: []config.DocSyncPair{{Paths: "*.go", Doc: "README.md"}},
	})
	violations, err := c.Run(check.Context{Source: fakeSource{}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("変更が無ければ nil のはず, got %v", violations)
	}
}

func TestGranularity(t *testing.T) {
	c := mustNew(t, config.CheckConfig{})
	if c.Granularity() != check.GranularitySquashed {
		t.Errorf("doc-sync の granularity は squashed 固定のはず, got %v", c.Granularity())
	}
}

func TestNewInvalidPair(t *testing.T) {
	if _, err := docsync.New(config.CheckConfig{
		Pairs: []config.DocSyncPair{{Paths: "*.go"}},
	}); err == nil {
		t.Fatal("doc が空なら New() はエラーになるはず")
	}
}
