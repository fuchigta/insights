package gitutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/spotter/internal/gitutil"
)

func newTestRepo(t *testing.T) (*gitutil.Repo, string) {
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

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("ファイル作成に失敗しました: %v", err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "1st")
	sha := run("rev-parse", "HEAD")

	return gitutil.New(dir), sha
}

func TestCommitExists(t *testing.T) {
	repo, sha := newTestRepo(t)

	ok, err := repo.CommitExists(sha)
	if err != nil {
		t.Fatalf("CommitExists() error: %v", err)
	}
	if !ok {
		t.Errorf("実在するコミットのはずが CommitExists() = false")
	}
}

func TestCommitExistsAllZero(t *testing.T) {
	repo, _ := newTestRepo(t)

	ok, err := repo.CommitExists("0000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("CommitExists() error: %v", err)
	}
	if ok {
		t.Errorf("全ゼロの SHA は実在しないはずが CommitExists() = true")
	}
}

func TestCommitExistsEmpty(t *testing.T) {
	repo, _ := newTestRepo(t)

	ok, err := repo.CommitExists("")
	if err != nil {
		t.Fatalf("CommitExists() error: %v", err)
	}
	if ok {
		t.Errorf("空文字は実在しないはずが CommitExists() = true")
	}
}
