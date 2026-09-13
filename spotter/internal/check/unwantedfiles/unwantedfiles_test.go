package unwantedfiles_test

import (
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/check/unwantedfiles"
	"github.com/fuchigta/spotter/internal/config"
)

type fakeSource struct {
	changed []string
	sizes   map[string]int64
}

func (f fakeSource) ChangedFiles() ([]string, error)       { return f.changed, nil }
func (f fakeSource) DiffLines(path string) (string, error) { return "", nil }
func (f fakeSource) BlobSize(path string) (int64, error)   { return f.sizes[path], nil }

func mustNew(t *testing.T, cc config.CheckConfig) *unwantedfiles.Check {
	t.Helper()
	c, err := unwantedfiles.New(cc)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return c
}

func TestRunDenyPattern(t *testing.T) {
	c := mustNew(t, config.CheckConfig{
		Deny: []config.UnwantedFilesDeny{
			{Paths: "*.jsonl", Reason: "セッションログ"},
		},
	})

	violations, err := c.Run(check.Context{Source: fakeSource{changed: []string{"a.jsonl", "b.go"}}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("違反は 1 件のはず, got %d", len(violations))
	}
	if violations[0].Summary != "a.jsonl: セッションログ" {
		t.Errorf("Summary = %q", violations[0].Summary)
	}
}

func TestRunMaxBytes(t *testing.T) {
	c := mustNew(t, config.CheckConfig{MaxBytes: 100})

	src := fakeSource{
		changed: []string{"big.bin", "small.bin"},
		sizes:   map[string]int64{"big.bin": 200, "small.bin": 50},
	}
	violations, err := c.Run(check.Context{Source: src})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("違反は 1 件のはず, got %d", len(violations))
	}
	if violations[0].Summary != "big.bin: 200 バイト（上限 100 バイト）" {
		t.Errorf("Summary = %q", violations[0].Summary)
	}
}

func TestRunNoViolation(t *testing.T) {
	c := mustNew(t, config.CheckConfig{MaxBytes: 100})
	violations, err := c.Run(check.Context{Source: fakeSource{changed: []string{"ok.go"}, sizes: map[string]int64{"ok.go": 10}}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if violations != nil {
		t.Errorf("違反が無ければ nil のはず, got %v", violations)
	}
}

func TestGranularity(t *testing.T) {
	c := mustNew(t, config.CheckConfig{})
	if c.Granularity() != check.GranularityPerCommit {
		t.Errorf("unwanted-files の granularity は per-commit 固定のはず, got %v", c.Granularity())
	}
}

func TestNewInvalidDeny(t *testing.T) {
	if _, err := unwantedfiles.New(config.CheckConfig{
		Deny: []config.UnwantedFilesDeny{{Paths: "*.jsonl"}},
	}); err == nil {
		t.Fatal("reason が空なら New() はエラーになるはず")
	}
}
