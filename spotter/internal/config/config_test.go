package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "spotter.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("設定ファイルの書き込みに失敗しました: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeConfig(t, `
checks:
  doc-sync:
    type: doc-sync
    pairs:
      - paths: "internal/cli/*.go"
        doc: README.md
  unwanted-files:
    type: unwanted-files
    max_bytes: 1048576
    deny:
      - { paths: "*.jsonl", reason: "セッションログ" }
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Checks) != 2 {
		t.Fatalf("checks の数 = %d, want 2", len(cfg.Checks))
	}
	if cfg.Checks["doc-sync"].Type != TypeDocSync {
		t.Errorf("doc-sync.Type = %q", cfg.Checks["doc-sync"].Type)
	}
	if got := cfg.Checks["unwanted-files"].Deny[0].Reason; got != "セッションログ" {
		t.Errorf("deny[0].Reason = %q", got)
	}
}

func TestLoadDocPathsAndCommitSubject(t *testing.T) {
	path := writeConfig(t, `
checks:
  doc-paths:
    type: doc-paths
    docs: ["README.md"]
    ignore: ["internal/source/codex"]
  commit-subject:
    type: commit-subject
    allowed_types: [feat, fix]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := cfg.Checks["doc-paths"].Docs; len(got) != 1 || got[0] != "README.md" {
		t.Errorf("doc-paths.Docs = %v", got)
	}
	if got := cfg.Checks["commit-subject"].AllowedTypes; len(got) != 2 {
		t.Errorf("commit-subject.AllowedTypes = %v", got)
	}
}

func TestLoadUnknownType(t *testing.T) {
	path := writeConfig(t, `
checks:
  my-check:
    type: my-custom-check
`)
	if _, err := Load(path); err == nil {
		t.Fatal("未対応の type で Load() がエラーになりませんでした")
	}
}

func TestLoadMissingType(t *testing.T) {
	path := writeConfig(t, `
checks:
  my-check: {}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("type 未指定で Load() がエラーになりませんでした")
	}
}

func TestResolveExempt(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name        string
		cfg         Config
		key         string
		cc          CheckConfig
		wantEnable  bool
		wantTrailer string
	}{
		{"既定はキーから生成", Config{}, "doc-sync", CheckConfig{}, true, "Doc-Sync"},
		{"ハイフン区切りも大文字化する", Config{}, "doc-sync-frontend", CheckConfig{}, true, "Doc-Sync-Frontend"},
		{"enable を上書きできる", Config{}, "doc-sync", CheckConfig{Exempt: &ExemptConfig{Enable: &falseVal}}, false, "Doc-Sync"},
		{"trailer を上書きできる", Config{}, "doc-sync", CheckConfig{Exempt: &ExemptConfig{Enable: &trueVal, Trailer: "Custom"}}, true, "Custom"},
		{"commit-subject は既定で免除不可", Config{}, "commit-subject", CheckConfig{Type: TypeCommitSubject}, false, "Commit-Subject"},
		{"commit-subject でも明示すれば免除できる", Config{}, "commit-subject", CheckConfig{Type: TypeCommitSubject, Exempt: &ExemptConfig{Enable: &trueVal}}, true, "Commit-Subject"},
		{
			"types.<type>.default.exempt が checks より弱い優先度",
			Config{Types: map[string]TypeConfig{
				"my-check": {Command: "./x", Default: &TypeDefault{Granularity: "squashed", Exempt: &ExemptConfig{Enable: &falseVal, Trailer: "MyCheck"}}},
			}},
			"my-check-a", CheckConfig{Type: "my-check"}, false, "MyCheck",
		},
		{
			"checks 側の指定は types.default より優先される",
			Config{Types: map[string]TypeConfig{
				"my-check": {Command: "./x", Default: &TypeDefault{Granularity: "squashed", Exempt: &ExemptConfig{Enable: &falseVal, Trailer: "MyCheck"}}},
			}},
			"my-check-a", CheckConfig{Type: "my-check", Exempt: &ExemptConfig{Enable: &trueVal}}, true, "MyCheck",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enable, trailer := tt.cfg.ResolveExempt(tt.key, tt.cc)
			if enable != tt.wantEnable || trailer != tt.wantTrailer {
				t.Errorf("ResolveExempt() = (%v, %q), want (%v, %q)", enable, trailer, tt.wantEnable, tt.wantTrailer)
			}
		})
	}
}
