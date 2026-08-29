package evidence

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
)

// requireGit は git が PATH に無い環境でテストをスキップする。
func requireGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git が PATH に見つからないためスキップします")
	}
	return path
}

// runGitT はテスト用に git コマンドを実行し、失敗したら t.Fatal する。
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s に失敗しました: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// initRepo は dir に実際の git リポジトリを作る。グローバル設定に依存しないよう
// user.email / user.name をリポジトリローカルに設定する。
func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGitT(t, dir, "init", "-b", "main")
	runGitT(t, dir, "config", "user.email", "test@example.com")
	runGitT(t, dir, "config", "user.name", "Test User")
	// commit.gpgsign がグローバルで有効な環境でも失敗しないようにする。
	runGitT(t, dir, "config", "commit.gpgsign", "false")
}

func testEvidenceConfig() config.EvidenceConfig {
	return config.EvidenceConfig{
		Git:          true,
		Gh:           config.TristateFalse,
		Glab:         config.TristateFalse,
		MaxBodyChars: 4000,
	}
}

func TestCollectGitCommit(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "a.txt")

	// 本文に改行とタブを含むコミットメッセージ。
	msg := "件名だよ\n\n本文1行目\tタブ入り\n本文2行目"
	runGitT(t, dir, "commit", "-m", msg)

	c := New(testEvidenceConfig())
	if c.gitPath == "" {
		t.Skip("git バイナリが検出されませんでした")
	}

	q := Query{
		SessionID:   "session-1",
		ProjectPath: dir,
		From:        time.Now().Add(-1 * time.Hour),
		To:          time.Now().Add(1 * time.Hour),
	}

	got := c.Collect(context.Background(), q)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (got=%+v)", len(got), got)
	}

	e := got[0]
	if e.Kind != "commit" {
		t.Errorf("Kind = %q, want %q", e.Kind, "commit")
	}
	if e.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "session-1")
	}
	if e.Title != "件名だよ" {
		t.Errorf("Title = %q, want %q", e.Title, "件名だよ")
	}
	wantBody := "本文1行目\tタブ入り\n本文2行目"
	if e.Body != wantBody {
		t.Errorf("Body = %q, want %q", e.Body, wantBody)
	}
	if e.Insertions != 1 {
		t.Errorf("Insertions = %d, want 1", e.Insertions)
	}
	if e.Deletions != 0 {
		t.Errorf("Deletions = %d, want 0", e.Deletions)
	}
	if e.Files != 1 {
		t.Errorf("Files = %d, want 1", e.Files)
	}
	if e.Ref == "" {
		t.Error("Ref が空です（短縮SHAが入るはず）")
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp がゼロ値です")
	}
}

func TestCollectGitCommitOutsideTimeRange(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "a.txt")
	runGitT(t, dir, "commit", "-m", "subject only")

	c := New(testEvidenceConfig())
	if c.gitPath == "" {
		t.Skip("git バイナリが検出されませんでした")
	}

	// セッションの時間帯を「未来」にずらし、今作ったコミットが範囲外になるようにする。
	q := Query{
		SessionID:   "session-2",
		ProjectPath: dir,
		From:        time.Now().Add(24 * time.Hour),
		To:          time.Now().Add(48 * time.Hour),
	}

	got := c.Collect(context.Background(), q)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (got=%+v)", len(got), got)
	}
}

func TestCollectNotAGitRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir() // git init していない

	c := New(testEvidenceConfig())
	q := Query{
		SessionID:   "session-3",
		ProjectPath: dir,
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
	}

	got := c.Collect(context.Background(), q) // パニックしないこと
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestCollectGitBinaryMissing(t *testing.T) {
	// git 自体が見つからない状態をシミュレートする。
	c := New(testEvidenceConfig())
	c.gitPath = "" // テストから差し替え

	q := Query{
		SessionID:   "session-4",
		ProjectPath: t.TempDir(),
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
	}

	got := c.Collect(context.Background(), q)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestCollectGhGlabMissingAreSkipped(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	runGitT(t, dir, "remote", "add", "origin", "https://github.com/example/repo.git")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "a.txt")
	runGitT(t, dir, "commit", "-m", "commit for gh/glab skip test")

	cfg := config.EvidenceConfig{
		Git:          true,
		Gh:           config.TristateTrue, // 有効にしているが…
		Glab:         config.TristateTrue, // …バイナリが無いのでスキップされるはず
		MaxBodyChars: 4000,
	}
	c := New(cfg)
	// gh/glab が実際にこの環境に無くても、ある環境でテストが揺れないよう明示的に空にする。
	c.ghPath = ""
	c.glabPath = ""

	q := Query{
		SessionID:   "session-5",
		ProjectPath: dir,
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
	}

	got := c.Collect(context.Background(), q)
	// git commit の収集自体は成功するはず（gh/glab だけがスキップされる）。
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (gh/glab はスキップされ commit だけ残るはず): %+v", len(got), got)
	}
	if got[0].Kind != "commit" {
		t.Errorf("Kind = %q, want %q", got[0].Kind, "commit")
	}
}

func TestAvailable(t *testing.T) {
	requireGit(t)

	cfg := config.EvidenceConfig{
		Git:  true,
		Gh:   config.TristateFalse,
		Glab: config.TristateFalse,
	}
	c := New(cfg)
	avail := c.Available()

	found := false
	for _, m := range avail {
		if m == "git" {
			found = true
		}
		if m == "gh" || m == "glab" {
			t.Errorf("Gh/Glab が false なのに Available() に含まれています: %v", avail)
		}
	}
	if !found {
		t.Errorf("git が Available() に含まれていません: %v", avail)
	}
}

func TestAvailableGhGlabMissingBinary(t *testing.T) {
	cfg := config.EvidenceConfig{
		Git:  false,
		Gh:   config.TristateTrue,
		Glab: config.TristateTrue,
	}
	c := New(cfg)
	c.ghPath = ""
	c.glabPath = ""

	avail := c.Available()
	if len(avail) != 0 {
		t.Errorf("Available() = %v, want empty（バイナリが無いので何も使えないはず）", avail)
	}
}

func TestTruncateBody(t *testing.T) {
	cfg := config.EvidenceConfig{MaxBodyChars: 5}
	c := &Collector{cfg: cfg}

	got := c.truncateBody("abcdefgh")
	if !strings.HasPrefix(got, "abcde") {
		t.Errorf("truncateBody 結果が先頭 5 文字を保持していません: %q", got)
	}
	if got == "abcdefgh" {
		t.Errorf("切り詰められていません: %q", got)
	}
	if strings.Contains(got, "fgh") {
		t.Errorf("切り詰め後に元の末尾が残っています: %q", got)
	}

	// 上限未満なら変化しない。
	short := "abc"
	if got := c.truncateBody(short); got != short {
		t.Errorf("truncateBody(%q) = %q, want unchanged", short, got)
	}

	// MaxBodyChars <= 0 は無制限。
	cfgUnlimited := config.EvidenceConfig{MaxBodyChars: 0}
	cUnlimited := &Collector{cfg: cfgUnlimited}
	long := strings.Repeat("x", 100)
	if got := cUnlimited.truncateBody(long); got != long {
		t.Errorf("MaxBodyChars=0 なのに切り詰められました")
	}

	// マルチバイト文字でも rune 単位で安全に切り詰められること（不正なUTF-8にならない）。
	cfgMB := config.EvidenceConfig{MaxBodyChars: 3}
	cMB := &Collector{cfg: cfgMB}
	mb := "あいうえお"
	gotMB := cMB.truncateBody(mb)
	if !strings.HasPrefix(gotMB, "あいう") {
		t.Errorf("マルチバイト切り詰めが壊れています: %q", gotMB)
	}
}

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		raw      string
		wantHost string
		wantSlug string
		wantErr  bool
	}{
		{"https://github.com/owner/repo.git", "github.com", "owner/repo", false},
		{"https://github.com/owner/repo", "github.com", "owner/repo", false},
		{"git@github.com:owner/repo.git", "github.com", "owner/repo", false},
		{"ssh://git@github.com/owner/repo.git", "github.com", "owner/repo", false},
		{"https://gitlab.com/group/subgroup/project.git", "gitlab.com", "group/subgroup/project", false},
		{"", "", "", true},
		{"not a url", "", "", true},
	}
	for _, tc := range cases {
		host, slug, err := parseRemoteURL(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseRemoteURL(%q) err = nil, want error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRemoteURL(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if host != tc.wantHost || slug != tc.wantSlug {
			t.Errorf("parseRemoteURL(%q) = (%q, %q), want (%q, %q)", tc.raw, host, slug, tc.wantHost, tc.wantSlug)
		}
	}
}

func TestIsGitHubGitLabHost(t *testing.T) {
	if !isGitHubHost("github.com") {
		t.Error("github.com が GitHub と判定されませんでした")
	}
	if !isGitHubHost("GitHub.com") {
		t.Error("大文字小文字を無視していません")
	}
	if isGitHubHost("gitlab.com") {
		t.Error("gitlab.com が GitHub と誤判定されました")
	}
	if !isGitLabHost("gitlab.com") {
		t.Error("gitlab.com が GitLab と判定されませんでした")
	}
	if isGitLabHost("github.com") {
		t.Error("github.com が GitLab と誤判定されました")
	}
}

func TestSplitBodyAndShortstat(t *testing.T) {
	cases := []struct {
		name     string
		tail     string
		wantBody string
		wantIns  int
		wantDel  int
		wantFile int
	}{
		{
			name:     "no changes",
			tail:     "subject only body\n",
			wantBody: "subject only body",
		},
		{
			name:     "with stat",
			tail:     "line1\nline2\n\n 3 files changed, 12 insertions(+), 4 deletions(-)\n",
			wantBody: "line1\nline2",
			wantIns:  12,
			wantDel:  4,
			wantFile: 3,
		},
		{
			name:     "single file single insertion",
			wantBody: "",
			tail:     "\n\n 1 file changed, 1 insertion(+)\n",
			wantIns:  1,
			wantFile: 1,
		},
		{
			name:     "empty body no stat",
			tail:     "\n",
			wantBody: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ins, del, files := splitBodyAndShortstat(tc.tail)
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if ins != tc.wantIns {
				t.Errorf("insertions = %d, want %d", ins, tc.wantIns)
			}
			if del != tc.wantDel {
				t.Errorf("deletions = %d, want %d", del, tc.wantDel)
			}
			if files != tc.wantFile {
				t.Errorf("files = %d, want %d", files, tc.wantFile)
			}
		})
	}
}
