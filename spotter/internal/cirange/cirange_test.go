package cirange_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuchigta/spotter/internal/cirange"
)

func envFromMap(m map[string]string) cirange.Env {
	return func(key string) string { return m[key] }
}

func writeEventJSON(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("イベント JSON の書き込みに失敗しました: %v", err)
	}
	return path
}

func alwaysExists(sha string) (bool, error) { return sha != "", nil }
func neverExists(sha string) (bool, error)  { return false, nil }

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want cirange.Provider
		ok   bool
	}{
		{"GitHub Actions", map[string]string{"GITHUB_ACTIONS": "true"}, cirange.ProviderGitHubActions, true},
		{"GitLab CI", map[string]string{"GITLAB_CI": "true"}, cirange.ProviderGitLabCI, true},
		{"どちらでもない", map[string]string{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cirange.Detect(envFromMap(tt.env))
			if got != tt.want || ok != tt.ok {
				t.Errorf("Detect() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveGitHubActionsPullRequest(t *testing.T) {
	eventPath := writeEventJSON(t, `{"pull_request": {"base": {"sha": "aaa111"}}}`)
	env := envFromMap(map[string]string{
		"GITHUB_EVENT_NAME": "pull_request",
		"GITHUB_EVENT_PATH": eventPath,
	})

	got, err := cirange.Resolve(cirange.ProviderGitHubActions, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "aaa111..HEAD"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitHubActionsPush(t *testing.T) {
	eventPath := writeEventJSON(t, `{"before": "bbb222"}`)
	env := envFromMap(map[string]string{
		"GITHUB_EVENT_NAME": "push",
		"GITHUB_EVENT_PATH": eventPath,
		"GITHUB_SHA":        "ccc333",
	})

	got, err := cirange.Resolve(cirange.ProviderGitHubActions, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "bbb222..ccc333"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitHubActionsPushNewBranchFallsBack(t *testing.T) {
	// 新規ブランチの初回 push は before が全ゼロになり、実在しないコミットとして扱われる。
	eventPath := writeEventJSON(t, `{"before": "0000000000000000000000000000000000000000"}`)
	env := envFromMap(map[string]string{
		"GITHUB_EVENT_NAME": "push",
		"GITHUB_EVENT_PATH": eventPath,
		"GITHUB_SHA":        "ccc333",
	})

	got, err := cirange.Resolve(cirange.ProviderGitHubActions, env, neverExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "-1 HEAD"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitHubActionsNoEventPathFallsBack(t *testing.T) {
	env := envFromMap(map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch"})

	got, err := cirange.Resolve(cirange.ProviderGitHubActions, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "-1 HEAD"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitLabMergeRequest(t *testing.T) {
	env := envFromMap(map[string]string{
		"CI_PIPELINE_SOURCE":             "merge_request_event",
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": "aaa111",
	})

	got, err := cirange.Resolve(cirange.ProviderGitLabCI, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "aaa111..HEAD"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitLabPush(t *testing.T) {
	env := envFromMap(map[string]string{
		"CI_COMMIT_BEFORE_SHA": "bbb222",
		"CI_COMMIT_SHA":        "ccc333",
	})

	got, err := cirange.Resolve(cirange.ProviderGitLabCI, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "bbb222..ccc333"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveGitLabBeforeMissingFallsBack(t *testing.T) {
	env := envFromMap(map[string]string{})

	got, err := cirange.Resolve(cirange.ProviderGitLabCI, env, alwaysExists)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if want := "-1 HEAD"; got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	if _, err := cirange.Resolve("unknown", envFromMap(nil), alwaysExists); err == nil {
		t.Fatal("未対応の provider では Resolve() がエラーになるはず")
	}
}
