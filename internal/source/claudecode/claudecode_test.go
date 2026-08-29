package claudecode

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/source"
)

func TestName(t *testing.T) {
	s := New("testdata/fakehome")
	if got := s.Name(); got != "claude-code" {
		t.Fatalf("Name() = %q, want %q", got, "claude-code")
	}
}

func TestAvailable(t *testing.T) {
	t.Run("存在するprojectsディレクトリ", func(t *testing.T) {
		s := &Source{Root: "testdata/fakehome"}
		if err := s.Available(); err != nil {
			t.Fatalf("Available() = %v, want nil", err)
		}
	})

	t.Run("存在しないルート", func(t *testing.T) {
		s := &Source{Root: filepath.Join("testdata", "does-not-exist")}
		if err := s.Available(); err == nil {
			t.Fatal("Available() = nil, want error")
		}
	})
}

func TestDiscover(t *testing.T) {
	s := &Source{Root: "testdata/fakehome"}

	refs, err := s.Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	sort.Slice(refs, func(i, j int) bool { return refs[i].SessionID < refs[j].SessionID })

	if len(refs) != 2 {
		t.Fatalf("Discover() returned %d refs, want 2: %+v", len(refs), refs)
	}

	wantIDs := []string{"22222222-2222-2222-2222-222222222222", "agent-deadbeef01"}
	for i, want := range wantIDs {
		if refs[i].SessionID != want {
			t.Errorf("refs[%d].SessionID = %q, want %q", i, refs[i].SessionID, want)
		}
		if refs[i].Source != "claude-code" {
			t.Errorf("refs[%d].Source = %q, want claude-code", i, refs[i].Source)
		}
		if refs[i].Size <= 0 {
			t.Errorf("refs[%d].Size = %d, want > 0", i, refs[i].Size)
		}
	}

	// memory/ 配下の decoy.jsonl が拾われていないことを確認する。
	for _, r := range refs {
		if filepath.Base(filepath.Dir(r.Path)) == "memory" {
			t.Errorf("memory/ 配下のファイルが Discover に含まれている: %s", r.Path)
		}
	}
}

func TestDiscover_Since(t *testing.T) {
	s := &Source{Root: "testdata/fakehome"}

	future := time.Now().Add(24 * time.Hour)
	refs, err := s.Discover(future)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("Discover(未来のsince) returned %d refs, want 0", len(refs))
	}
}

func TestDiscover_MissingProjectsDir(t *testing.T) {
	s := &Source{Root: filepath.Join("testdata", "does-not-exist")}
	if _, err := s.Discover(time.Time{}); err == nil {
		t.Fatal("Discover() = nil error, want error")
	}
}

// refFor はテスト用に testdata 内のファイルを指す source.Ref を組み立てる。
func refFor(t *testing.T, path string) source.Ref {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return source.Ref{
		Source:    "claude-code",
		SessionID: "fixture-session-1",
		Path:      path,
		ModTime:   info.ModTime(),
		Size:      info.Size(),
	}
}

func TestParseBasicSession(t *testing.T) {
	s := &Source{}
	ref := refFor(t, filepath.Join("testdata", "basic_session.jsonl"))

	sess, err := s.Parse(ref)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if sess.Source != "claude-code" {
		t.Errorf("Source = %q, want claude-code", sess.Source)
	}
	if sess.SessionID != "fixture-session-1" {
		t.Errorf("SessionID = %q, want fixture-session-1", sess.SessionID)
	}
	if sess.TranscriptPath != ref.Path {
		t.Errorf("TranscriptPath = %q, want %q", sess.TranscriptPath, ref.Path)
	}
	if want := `C:\Users\testuser\src\example\myapp`; sess.ProjectPath != want {
		t.Errorf("ProjectPath = %q, want %q", sess.ProjectPath, want)
	}
	if sess.ProjectLabel != "myapp" {
		t.Errorf("ProjectLabel = %q, want myapp", sess.ProjectLabel)
	}
	if sess.GitBranch != "main" {
		t.Errorf("GitBranch = %q, want main", sess.GitBranch)
	}
	if sess.Entrypoint != "cli" {
		t.Errorf("Entrypoint = %q, want cli", sess.Entrypoint)
	}
	if sess.IsSidechain {
		t.Error("IsSidechain = true, want false")
	}
	if sess.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", sess.ParentSessionID)
	}
	if want := "テストセッションのタイトル"; sess.Title != want {
		t.Errorf("Title = %q, want %q", sess.Title, want)
	}
	if want := "テストを実行してください。"; sess.FirstPrompt != want {
		t.Errorf("FirstPrompt = %q, want %q", sess.FirstPrompt, want)
	}

	wantStart := mustTime(t, "2026-01-01T00:00:00Z")
	wantEnd := mustTime(t, "2026-01-01T00:00:14Z")
	if !sess.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want %v", sess.StartedAt, wantStart)
	}
	if !sess.EndedAt.Equal(wantEnd) {
		t.Errorf("EndedAt = %v, want %v", sess.EndedAt, wantEnd)
	}

	if sess.ContentHash == "" {
		t.Error("ContentHash が空")
	}
	if len(sess.ContentHash) != 64 {
		t.Errorf("ContentHash の長さ = %d, want 64 (sha256 hex)", len(sess.ContentHash))
	}

	wantLen := 13
	if len(sess.Messages) != wantLen {
		t.Fatalf("len(Messages) = %d, want %d: %+v", len(sess.Messages), wantLen, sess.Messages)
	}

	type expect struct {
		role     model.Role
		text     string
		model    string
		effort   string
		toolName string
		isError  bool
		isMeta   bool
		hasUsage bool
	}
	wants := []expect{
		{role: model.RoleUser, text: "テストを実行してください。"},
		{role: model.RoleAssistant, text: "テストを実行します。", model: "claude-sonnet-5", effort: "high", hasUsage: true},
		{role: model.RoleAssistant, text: `{"command":"echo this is a somewhat longer command used to test truncation behavior"}`, model: "claude-sonnet-5", effort: "high", toolName: "Bash"},
		{role: model.RoleTool, text: "this is a somewhat longer command result text used to test truncation behavior in tool results", toolName: "Bash"},
		{role: model.RoleAssistant, text: "内部思考のテキスト", model: "claude-sonnet-5", effort: "medium", isMeta: true, hasUsage: true},
		{role: model.RoleAssistant, text: "完了しました。", model: "claude-sonnet-5", effort: "medium"},
		{role: model.RoleUser, isMeta: true},
		{role: model.RoleUser, isMeta: true},
		{role: model.RoleUser, isMeta: true},
		{role: model.RoleUser, isMeta: true},
		{role: model.RoleAssistant, text: `{"path":"out.txt","content":"data"}`, model: "claude-sonnet-5", effort: "high", toolName: "Write", hasUsage: true},
		{role: model.RoleTool, text: "permission denied", toolName: "Write", isError: true},
		{role: model.RoleAssistant, text: "エラーが発生しましたが完了しました。", model: "claude-sonnet-5", effort: "high", hasUsage: true},
	}

	for i, w := range wants {
		m := sess.Messages[i]
		if m.Seq != i {
			t.Errorf("Messages[%d].Seq = %d, want %d", i, m.Seq, i)
		}
		if m.Role != w.role {
			t.Errorf("Messages[%d].Role = %q, want %q", i, m.Role, w.role)
		}
		if w.text != "" && m.Text != w.text {
			t.Errorf("Messages[%d].Text = %q, want %q", i, m.Text, w.text)
		}
		if w.model != "" && m.Model != w.model {
			t.Errorf("Messages[%d].Model = %q, want %q", i, m.Model, w.model)
		}
		if w.effort != "" && m.Effort != w.effort {
			t.Errorf("Messages[%d].Effort = %q, want %q", i, m.Effort, w.effort)
		}
		if m.ToolName != w.toolName {
			t.Errorf("Messages[%d].ToolName = %q, want %q", i, m.ToolName, w.toolName)
		}
		if m.IsError != w.isError {
			t.Errorf("Messages[%d].IsError = %v, want %v", i, m.IsError, w.isError)
		}
		if m.IsMeta != w.isMeta {
			t.Errorf("Messages[%d].IsMeta = %v, want %v", i, m.IsMeta, w.isMeta)
		}
		if w.hasUsage && m.Usage == nil {
			t.Errorf("Messages[%d].Usage = nil, want non-nil", i)
		}
		if !w.hasUsage && m.Usage != nil {
			t.Errorf("Messages[%d].Usage = %+v, want nil (重複計上の疑い)", i, m.Usage)
		}
		if m.Truncated {
			t.Errorf("Messages[%d].Truncated = true, want false (デフォルトの切り詰め長では切れないはず)", i)
		}
	}

	// トークン集計の詳細チェック。
	u1 := sess.Messages[1].Usage
	if u1.InputTokens != 10 || u1.OutputTokens != 5 || u1.CacheRead != 100 || u1.CacheCreation5m != 20 || u1.CacheCreation1h != 0 || u1.ServiceTier != "standard" {
		t.Errorf("Messages[1].Usage = %+v, 期待値と不一致", u1)
	}
	u4 := sess.Messages[4].Usage
	if u4.InputTokens != 7 || u4.OutputTokens != 3 || u4.ThinkingTokens != 2 || u4.CacheCreation5m != 15 {
		t.Errorf("Messages[4].Usage = %+v, 期待値と不一致 (cache_creation_input_tokens のフォールバック)", u4)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return tm
}

func TestParseTruncation(t *testing.T) {
	s := &Source{MaxTextLen: 20}
	ref := refFor(t, filepath.Join("testdata", "basic_session.jsonl"))

	sess, err := s.Parse(ref)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	toolUse := sess.Messages[2]
	if !toolUse.Truncated {
		t.Error("tool_use の Text が切り詰められていない")
	}
	if got := len([]rune(toolUse.Text)); got != 20 {
		t.Errorf("tool_use の Text の長さ = %d, want 20", got)
	}

	toolResult := sess.Messages[3]
	if !toolResult.Truncated {
		t.Error("tool_result の Text が切り詰められていない")
	}
	if got := len([]rune(toolResult.Text)); got != 20 {
		t.Errorf("tool_result の Text の長さ = %d, want 20", got)
	}
}

func TestParseAllBrokenFileReturnsError(t *testing.T) {
	s := &Source{}
	ref := refFor(t, filepath.Join("testdata", "all_broken.jsonl"))

	if _, err := s.Parse(ref); err == nil {
		t.Fatal("Parse() = nil error, want error（ファイル全体が壊れている）")
	}
}

func TestContentHashDeterministic(t *testing.T) {
	s := &Source{}
	ref := refFor(t, filepath.Join("testdata", "basic_session.jsonl"))

	sess1, err := s.Parse(ref)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	sess2, err := s.Parse(ref)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if sess1.ContentHash == "" {
		t.Fatal("ContentHash が空")
	}
	if sess1.ContentHash != sess2.ContentHash {
		t.Errorf("ContentHash が非決定的: %q != %q", sess1.ContentHash, sess2.ContentHash)
	}
}

func TestParseSubagentSession(t *testing.T) {
	s := &Source{Root: "testdata/fakehome"}
	refs, err := s.Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	var mainRef, subRef *source.Ref
	for i := range refs {
		switch refs[i].SessionID {
		case "22222222-2222-2222-2222-222222222222":
			mainRef = &refs[i]
		case "agent-deadbeef01":
			subRef = &refs[i]
		}
	}
	if mainRef == nil || subRef == nil {
		t.Fatalf("Discover() が期待する ref を返さなかった: %+v", refs)
	}

	mainSess, err := s.Parse(*mainRef)
	if err != nil {
		t.Fatalf("Parse(main) error = %v", err)
	}
	if mainSess.IsSidechain {
		t.Error("main session の IsSidechain = true, want false")
	}
	if mainSess.ParentSessionID != "" {
		t.Errorf("main session の ParentSessionID = %q, want empty", mainSess.ParentSessionID)
	}

	subSess, err := s.Parse(*subRef)
	if err != nil {
		t.Fatalf("Parse(subagent) error = %v", err)
	}
	if !subSess.IsSidechain {
		t.Error("subagent の IsSidechain = false, want true")
	}
	if want := "22222222-2222-2222-2222-222222222222"; subSess.ParentSessionID != want {
		t.Errorf("subagent の ParentSessionID = %q, want %q", subSess.ParentSessionID, want)
	}
	if want := "サブエージェントのテスト用説明"; subSess.Title != want {
		t.Errorf("subagent の Title = %q, want %q (.meta.json の description)", subSess.Title, want)
	}
	if len(subSess.Messages) == 0 {
		t.Error("subagent の Messages が空")
	}
}

func TestIsMetaText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"system-reminder", "<system-reminder>hello</system-reminder>", true},
		{"command-name", "<command-name>/plan</command-name>", true},
		{"local-command-stdout", "<local-command-stdout>ok</local-command-stdout>", true},
		{"caveat substring", "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands.</local-command-caveat>", true},
		{"先頭の空白を許容", "  <system-reminder>hi</system-reminder>", true},
		{"通常の発話", "テストを実行してください。", false},
		{"空文字", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMetaText(c.in); got != c.want {
				t.Errorf("isMetaText(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	// マルチバイト文字（日本語）が文字化けせずルーン単位で切り詰められることを確認する。
	s := "あいうえおかきくけこ"
	got := truncateText(s, 3)
	want := "あいう"
	if got != want {
		t.Errorf("truncateText(%q, 3) = %q, want %q", s, got, want)
	}

	if got := truncateText("short", 100); got != "short" {
		t.Errorf("truncateText 短い文字列は変更されないはず: got %q", got)
	}
}

func TestReconstructProjectPath(t *testing.T) {
	// 実在しないディレクトリ名からは復元できず、空文字を返すはず（存在確認できないため）。
	if got := reconstructProjectPath("C--Users-nobody-does-not-exist-xyz"); got != "" {
		t.Errorf("reconstructProjectPath(存在しないパス) = %q, want empty", got)
	}
	if got := reconstructProjectPath(""); got != "" {
		t.Errorf("reconstructProjectPath(\"\") = %q, want empty", got)
	}
}

// TestParseRealTranscripts は実際の Claude Code ログ（~/.claude/projects）に対して
// Parse がエラーなく通ることだけを確認する。内容には依存しない。
// ~/.claude/projects が無い環境では skip する。
func TestParseRealTranscripts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("ホームディレクトリが取得できないため skip")
	}
	root := filepath.Join(home, ".claude")
	if _, err := os.Stat(filepath.Join(root, "projects")); err != nil {
		t.Skip("~/.claude/projects が存在しないため skip")
	}

	s := New(root)
	refs, err := s.Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(refs) == 0 {
		t.Skip("~/.claude/projects にセッションが無いため skip")
	}

	var (
		ok      int
		failed  int
		sawMain bool
		sawSub  bool
	)
	for _, ref := range refs {
		sess, err := s.Parse(ref)
		if err != nil {
			t.Errorf("Parse(%s) error = %v", ref.Path, err)
			failed++
			continue
		}
		if sess == nil {
			t.Errorf("Parse(%s) = nil session", ref.Path)
			failed++
			continue
		}
		if sess.SessionID == "" {
			t.Errorf("Parse(%s).SessionID が空", ref.Path)
		}
		if sess.Source != "claude-code" {
			t.Errorf("Parse(%s).Source = %q, want claude-code", ref.Path, sess.Source)
		}
		if sess.ParentSessionID != "" {
			sawSub = true
		} else {
			sawMain = true
		}
		ok++
	}

	t.Logf("real transcripts: %d refs discovered, %d parsed ok, %d failed (main seen=%v, subagent seen=%v)", len(refs), ok, failed, sawMain, sawSub)
}
