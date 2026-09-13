package rangespec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/gitutil"
	"github.com/fuchigta/spotter/internal/rangespec"
)

// newTestRepo は一時ディレクトリに git リポジトリを作り、指定したコミットを順に積む。
// files に書いたパス（相対）を各コミットで 1 ファイルずつ作成・コミットする。
func newTestRepo(t *testing.T, commits []struct{ file, message string }) (*gitutil.Repo, []string) {
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

	var shas []string
	for _, c := range commits {
		path := filepath.Join(dir, c.file)
		if err := os.WriteFile(path, []byte(c.message+"\n"), 0o644); err != nil {
			t.Fatalf("ファイル作成に失敗しました: %v", err)
		}
		run("add", c.file)
		run("commit", "-q", "-m", c.message)
		shas = append(shas, run("rev-parse", "HEAD"))
	}

	return gitutil.New(dir), shas
}

func TestPlanSquashed(t *testing.T) {
	repo, shas := newTestRepo(t, []struct{ file, message string }{
		{"a.txt", "1st"},
		{"b.txt", "2nd"},
		{"c.txt", "3rd\n\nDoc-Sync: skip 理由"},
	})

	invocations, err := rangespec.Plan(repo, "-3 HEAD", check.GranularitySquashed)
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if len(invocations) != 1 {
		t.Fatalf("squashed は 1 回にまとまるはず, got %d", len(invocations))
	}

	inv := invocations[0]
	if !strings.Contains(inv.Message, "Doc-Sync: skip 理由") {
		t.Errorf("Message に範囲内の全コミットメッセージが連結されていない: %q", inv.Message)
	}
	if !strings.Contains(inv.Label, shas[2][:7]) {
		t.Errorf("Label には最新コミットの短い sha が入るはず: %q", inv.Label)
	}

	files, err := inv.Source.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles() error: %v", err)
	}
	want := []string{"a.txt", "b.txt", "c.txt"}
	if !equalUnordered(files, want) {
		t.Errorf("ChangedFiles() = %v, want %v (順不同)", files, want)
	}
}

func TestPlanPerCommit(t *testing.T) {
	repo, shas := newTestRepo(t, []struct{ file, message string }{
		{"a.txt", "1st"},
		{"b.txt", "2nd"},
	})

	invocations, err := rangespec.Plan(repo, "-2 HEAD", check.GranularityPerCommit)
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if len(invocations) != 2 {
		t.Fatalf("per-commit はコミット数ぶん起動するはず, got %d", len(invocations))
	}

	// rev-list は新しい順。
	if !strings.Contains(invocations[0].Message, "2nd") {
		t.Errorf("先頭は最新コミットのはず: %q", invocations[0].Message)
	}
	if !strings.Contains(invocations[0].Label, shas[1][:7]) {
		t.Errorf("Label が一致しない: %q", invocations[0].Label)
	}

	files, err := invocations[0].Source.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles() error: %v", err)
	}
	if !equalUnordered(files, []string{"b.txt"}) {
		t.Errorf("最新コミット単体では b.txt だけのはず, got %v", files)
	}

	filesOldest, err := invocations[1].Source.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles() error: %v", err)
	}
	if !equalUnordered(filesOldest, []string{"a.txt"}) {
		t.Errorf("根コミットは EmptyTree との差分になるはず, got %v", filesOldest)
	}
}

func TestPlanNoCommits(t *testing.T) {
	repo, _ := newTestRepo(t, []struct{ file, message string }{{"a.txt", "1st"}})

	invocations, err := rangespec.Plan(repo, "HEAD..HEAD", check.GranularitySquashed)
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if len(invocations) != 0 {
		t.Errorf("対象コミットが無ければ空のはず, got %d", len(invocations))
	}
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}
