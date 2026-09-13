package commitsubject_test

import (
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/check/commitsubject"
	"github.com/fuchigta/spotter/internal/config"
)

func mustNew(t *testing.T) *commitsubject.Check {
	t.Helper()
	c, err := commitsubject.New(config.CheckConfig{
		AllowedTypes: []string{"feat", "fix", "perf", "refactor", "docs", "test", "build", "ci", "chore", "revert"},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return c
}

func TestRun(t *testing.T) {
	c := mustNew(t)

	tests := []struct {
		name    string
		message string
		wantHit bool
	}{
		{"通常の feat", "feat: 新機能を追加する", false},
		{"scope つき fix", "fix(rollup): 直す", false},
		{"破壊的変更", "feat(judge)!: 列挙値を変える", false},
		{"本文があっても subject 行だけ見る", "feat: 追加する\n\n詳細な本文", false},
		{"許可されていない type", "wip: 作業中", true},
		{"コロンの後にスペースが無い", "feat:追加する", true},
		{"説明が無い", "feat: ", true},
		{"Merge コミットは対象外", "Merge branch 'main' into feature", false},
		{"Revert コミットは対象外", "Revert \"feat: 何か\"", false},
		{"空メッセージは対象外", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, err := c.Run(check.Context{Message: tt.message})
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			gotHit := len(violations) > 0
			if gotHit != tt.wantHit {
				t.Errorf("Run(%q) violations = %v, want hit=%v", tt.message, violations, tt.wantHit)
			}
		})
	}
}

func TestGranularity(t *testing.T) {
	c := mustNew(t)
	if c.Granularity() != check.GranularityPerCommit {
		t.Errorf("commit-subject の granularity は per-commit 固定のはず, got %v", c.Granularity())
	}
}

func TestNewRequiresAllowedTypes(t *testing.T) {
	if _, err := commitsubject.New(config.CheckConfig{}); err == nil {
		t.Fatal("allowed_types が空なら New() はエラーになるはず")
	}
}
