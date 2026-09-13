package hooks_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/spotter/internal/gitutil"
	"github.com/fuchigta/spotter/internal/hooks"
)

func newTestRepo(t *testing.T) *gitutil.Repo {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-q", "-b", "main")
	run("config", "user.name", "spotter test")
	run("config", "user.email", "spotter@example.invalid")

	return gitutil.New(dir)
}

func TestInspectFreshRepo(t *testing.T) {
	repo := newTestRepo(t)

	status, err := hooks.Inspect(repo)
	if err != nil {
		t.Fatalf("Inspect() error: %v", err)
	}
	if status.HooksPath != "" {
		t.Errorf("HooksPath = %q, want 空", status.HooksPath)
	}
	if status.HookFileExists {
		t.Errorf("HookFileExists = true, want false")
	}
	if status.Managed {
		t.Errorf("Managed = true, want false")
	}
}

func TestInstallFreshRepoSetsHooksPath(t *testing.T) {
	repo := newTestRepo(t)

	result, err := hooks.Install(repo, ".githooks")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if result.Outcome != hooks.OutcomeCreated {
		t.Errorf("Outcome = %q, want created", result.Outcome)
	}
	if !result.HooksPathChanged {
		t.Errorf("HooksPathChanged = false, want true")
	}
	if result.HooksPath != ".githooks" {
		t.Errorf("HooksPath = %q, want .githooks", result.HooksPath)
	}
	if !result.HookFileExists || !result.Managed {
		t.Errorf("HookFileExists/Managed = %v/%v, want true/true", result.HookFileExists, result.Managed)
	}

	data, err := os.ReadFile(result.HookFile)
	if err != nil {
		t.Fatalf("生成されたフックの読み込みに失敗しました: %v", err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh") {
		t.Errorf("新規作成したフックに shebang が無い: %q", data)
	}
	if !strings.Contains(string(data), `spotter check --message "$1"`) {
		t.Errorf("生成されたフックが spotter を呼び出していない: %q", data)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)

	if _, err := hooks.Install(repo, ".githooks"); err != nil {
		t.Fatalf("1 回目の Install() error: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(repo.Dir, ".githooks", "commit-msg"))
	if err != nil {
		t.Fatalf("読み込みに失敗しました: %v", err)
	}

	result, err := hooks.Install(repo, ".githooks")
	if err != nil {
		t.Fatalf("2 回目の Install() error: %v", err)
	}
	if result.Outcome != hooks.OutcomeAlready {
		t.Errorf("2 回目の Outcome = %q, want already", result.Outcome)
	}
	if result.HooksPathChanged {
		t.Errorf("2 回目は HooksPathChanged = false のはず")
	}

	after, err := os.ReadFile(filepath.Join(repo.Dir, ".githooks", "commit-msg"))
	if err != nil {
		t.Fatalf("読み込みに失敗しました: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("べき等であるべきなのにフックの内容が変わった:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestInstallRespectsExistingHooksPath(t *testing.T) {
	repo := newTestRepo(t)

	cmd := exec.Command("git", "config", "core.hooksPath", "custom-hooks")
	cmd.Dir = repo.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	result, err := hooks.Install(repo, ".githooks")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if result.HooksPathChanged {
		t.Errorf("既に core.hooksPath が設定済みなら変更しないはず")
	}
	if result.HooksPath != "custom-hooks" {
		t.Errorf("HooksPath = %q, want custom-hooks", result.HooksPath)
	}
	if want := filepath.Join(repo.Dir, "custom-hooks", "commit-msg"); result.HookFile != want {
		t.Errorf("HookFile = %q, want %q", result.HookFile, want)
	}
}

func TestInstallAppendsToExistingHook(t *testing.T) {
	repo := newTestRepo(t)

	hookDir := filepath.Join(repo.Dir, "custom-hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	existing := "#!/bin/sh\necho 既存のフック\n"
	hookFile := filepath.Join(hookDir, "commit-msg")
	if err := os.WriteFile(hookFile, []byte(existing), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cmd := exec.Command("git", "config", "core.hooksPath", "custom-hooks")
	cmd.Dir = repo.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	result, err := hooks.Install(repo, ".githooks")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}
	if result.Outcome != hooks.OutcomeAppended {
		t.Errorf("Outcome = %q, want appended", result.Outcome)
	}

	data, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("読み込みに失敗しました: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "既存のフック") {
		t.Errorf("既存の内容が失われた: %q", content)
	}
	if !strings.Contains(content, `spotter check --message "$1"`) {
		t.Errorf("spotter の呼び出しが追記されていない: %q", content)
	}
	if strings.Index(content, "既存のフック") > strings.Index(content, `spotter check`) {
		t.Errorf("追記は既存内容の後ろに来るはず: %q", content)
	}
}

func TestInvocationLine(t *testing.T) {
	if hooks.InvocationLine != `spotter check --message "$1"` {
		t.Errorf("InvocationLine = %q", hooks.InvocationLine)
	}
}
