package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/source"
)

// parseBody は body をロールアウトとして書き出し、Parse した結果を返す。
func parseBody(t *testing.T, body string) *model.Session {
	t.Helper()

	root := t.TempDir()
	const name = "rollout-2026-08-30T10-00-00-sess-1.jsonl"
	path := writeRollout(t, root, "sessions", "2026/08/30", name, body, false)

	sess, err := New(root).Parse(source.Ref{Source: sourceName, SessionID: "sess-1", Path: path})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return sess
}

// fullRollout は 1 ターン分のやり取りを一通り含むロールアウト。
// 実データの行構造（session_meta → turn_context → response_item... →
// token_usage_record）に合わせている。
const fullRollout = `{"timestamp":"2026-08-30T10:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"sess-1","session_id":"sess-1","timestamp":"2026-08-30T10:00:00Z","cwd":"/home/u/work/myrepo","originator":"codex_cli_rs","cli_version":"0.128.0","source":"cli","git":{"branch":"feature/x","commit_hash":"deadbeef"}}}
{"timestamp":"2026-08-30T10:00:01Z","ordinal":1,"type":"turn_context","payload":{"cwd":"/home/u/work/myrepo","model":"gpt-5.5","effort":"high","approval_policy":"never","sandbox_policy":{"mode":"read-only"},"summary":"auto"}}
{"timestamp":"2026-08-30T10:00:02Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<environments>…</environments>"}]}}
{"timestamp":"2026-08-30T10:00:03Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\n\n<INSTRUCTIONS>\n守ること\n</INSTRUCTIONS>"}]}}
{"timestamp":"2026-08-30T10:00:04Z","ordinal":4,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"バグを直して"}]}}
{"timestamp":"2026-08-30T10:00:05Z","ordinal":5,"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"原因を絞り込む"}],"encrypted_content":null}}
{"timestamp":"2026-08-30T10:00:06Z","ordinal":6,"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"call-1"}}
{"timestamp":"2026-08-30T10:00:07Z","ordinal":7,"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"main.go\nREADME.md"}}
{"timestamp":"2026-08-30T10:00:08Z","ordinal":8,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"直しました"}]}}
{"timestamp":"2026-08-30T10:00:09Z","ordinal":9,"type":"token_usage_record","payload":{"thread_id":"sess-1","turn_id":"t1","session_id":"sess-1","root_turn_id":"t1","response_id":"resp-1","usage":{"input_tokens":1000,"cached_input_tokens":400,"cache_write_input_tokens":100,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200},"turn_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200},"thread_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200}}}
{"timestamp":"2026-08-30T10:00:10Z","ordinal":10,"type":"event_msg","payload":{"type":"user_message","message":"バグを直して","kind":"plain"}}
`

func TestParse_SessionFields(t *testing.T) {
	sess := parseBody(t, fullRollout)

	if sess.Source != sourceName {
		t.Errorf("Source = %q, want %q", sess.Source, sourceName)
	}
	if sess.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", sess.SessionID)
	}
	if sess.ProjectPath != "/home/u/work/myrepo" {
		t.Errorf("ProjectPath = %q", sess.ProjectPath)
	}
	if sess.ProjectLabel != "myrepo" {
		t.Errorf("ProjectLabel = %q, want myrepo", sess.ProjectLabel)
	}
	if sess.GitBranch != "feature/x" {
		t.Errorf("GitBranch = %q, want feature/x", sess.GitBranch)
	}
	if sess.Entrypoint != "cli" {
		t.Errorf("Entrypoint = %q, want cli", sess.Entrypoint)
	}
	if sess.IsSidechain {
		t.Error("IsSidechain = true, want false")
	}
	if sess.FirstPrompt != "バグを直して" {
		t.Errorf("FirstPrompt = %q, want バグを直して（Codex が差し込む user メッセージは数えない）", sess.FirstPrompt)
	}
	if want := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC); !sess.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", sess.StartedAt, want)
	}
	if want := time.Date(2026, 8, 30, 10, 0, 10, 0, time.UTC); !sess.EndedAt.Equal(want) {
		t.Errorf("EndedAt = %v, want %v", sess.EndedAt, want)
	}
	if sess.ContentHash == "" {
		t.Error("ContentHash が空")
	}
}

// TestParse_Messages は 1 ターン分の行が、正規化モデルの発話列にどう落ちるかを固定する。
// event_msg（legacy 履歴モードで response_item と同じ発話をもう一度書く行）を
// 取り込んで会話が二重になっていないことも、ここで見ている。
func TestParse_Messages(t *testing.T) {
	sess := parseBody(t, fullRollout)

	type want struct {
		role     model.Role
		isMeta   bool
		toolName string
		text     string
	}
	wants := []want{
		{role: model.RoleSystem, isMeta: true, text: "<environments>"},
		{role: model.RoleUser, isMeta: true, text: "# AGENTS.md instructions"},
		{role: model.RoleUser, text: "バグを直して"},
		{role: model.RoleAssistant, isMeta: true, text: "原因を絞り込む"},
		{role: model.RoleAssistant, toolName: "shell", text: `{"command":["ls"]}`},
		{role: model.RoleTool, toolName: "shell", text: "main.go"},
		{role: model.RoleAssistant, text: "直しました"},
	}

	if len(sess.Messages) != len(wants) {
		for i, m := range sess.Messages {
			t.Logf("messages[%d] role=%s tool=%q meta=%v text=%q", i, m.Role, m.ToolName, m.IsMeta, m.Text)
		}
		t.Fatalf("メッセージ数 = %d, want %d", len(sess.Messages), len(wants))
	}

	for i, w := range wants {
		m := sess.Messages[i]
		if m.Seq != i {
			t.Errorf("messages[%d].Seq = %d, want %d", i, m.Seq, i)
		}
		if m.Role != w.role {
			t.Errorf("messages[%d].Role = %q, want %q", i, m.Role, w.role)
		}
		if m.IsMeta != w.isMeta {
			t.Errorf("messages[%d].IsMeta = %v, want %v", i, m.IsMeta, w.isMeta)
		}
		if m.ToolName != w.toolName {
			t.Errorf("messages[%d].ToolName = %q, want %q", i, m.ToolName, w.toolName)
		}
		if !strings.Contains(m.Text, w.text) {
			t.Errorf("messages[%d].Text = %q, want %q を含む", i, m.Text, w.text)
		}
	}

	// アシスタント発話にはターンのモデル・推論強度が乗る（ロールアウトは
	// 発話ごとにモデル名を持たないので turn_context から引き継ぐ）。
	last := sess.Messages[len(sess.Messages)-1]
	if last.Model != "gpt-5.5" || last.Effort != "high" {
		t.Errorf("最後のアシスタント発話 model=%q effort=%q, want gpt-5.5 / high", last.Model, last.Effort)
	}
}

// TestParse_Usage は token_usage_record が直近のアシスタント発話に載ること、
// および Codex の input_tokens（キャッシュ読み取りを含む合計）を insights の
// 意味論に変換していることを確かめる。差し引かないとキャッシュ分を入力単価で
// 二重に数える。
func TestParse_Usage(t *testing.T) {
	sess := parseBody(t, fullRollout)

	var withUsage []model.Message
	for _, m := range sess.Messages {
		if m.Usage != nil {
			withUsage = append(withUsage, m)
		}
	}
	if len(withUsage) != 1 {
		t.Fatalf("Usage を持つ発話 = %d 件, want 1 件", len(withUsage))
	}

	m := withUsage[0]
	if m.Text != "直しました" {
		t.Errorf("Usage が載った発話 = %q, want 直しました", m.Text)
	}
	got := *m.Usage
	want := model.Usage{
		InputTokens:     600, // 1000 - 400（キャッシュ読み取り分）
		OutputTokens:    200,
		ThinkingTokens:  50,
		CacheRead:       400,
		CacheCreation5m: 100,
	}
	if got != want {
		t.Errorf("Usage = %+v, want %+v", got, want)
	}
}

// TestParse_UsageWithoutAssistantMessage は、発話を伴わないレスポンスの使用量も
// 落とさないことを確かめる。集計から漏らすとコストが過小に出る。
func TestParse_UsageWithoutAssistantMessage(t *testing.T) {
	body := `{"timestamp":"2026-08-30T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","session_id":"sess-1","timestamp":"2026-08-30T10:00:00Z","cwd":"/w","source":"cli"}}
{"timestamp":"2026-08-30T10:00:01Z","type":"turn_context","payload":{"cwd":"/w","model":"gpt-5.5","effort":"medium"}}
{"timestamp":"2026-08-30T10:00:02Z","type":"token_usage_record","payload":{"usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}}
`
	sess := parseBody(t, body)

	if len(sess.Messages) != 1 {
		t.Fatalf("メッセージ数 = %d, want 1（使用量だけを持つ発話）", len(sess.Messages))
	}
	m := sess.Messages[0]
	if m.Usage == nil || m.Usage.InputTokens != 10 || m.Usage.OutputTokens != 5 {
		t.Fatalf("Usage = %+v, want 入力 10 / 出力 5", m.Usage)
	}
	if !m.IsMeta {
		t.Error("使用量だけの発話は IsMeta = true であるべき（人間の発話ではない）")
	}
	if m.Model != "gpt-5.5" {
		t.Errorf("Model = %q, want gpt-5.5", m.Model)
	}
}

// TestParse_SubAgentSource は、サブエージェントのロールアウトを親に畳み込めるよう
// IsSidechain と ParentSessionID が立つことを確かめる。
func TestParse_SubAgentSource(t *testing.T) {
	body := `{"timestamp":"2026-08-30T10:00:00Z","type":"session_meta","payload":{"id":"child-1","session_id":"root-1","timestamp":"2026-08-30T10:00:00Z","cwd":"/w","thread_source":"subagent","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-1","depth":1,"agent_role":"reviewer"}}}}}
{"timestamp":"2026-08-30T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"レビューして"}]}}
`
	sess := parseBody(t, body)

	if !sess.IsSidechain {
		t.Error("IsSidechain = false, want true")
	}
	if sess.ParentSessionID != "parent-1" {
		t.Errorf("ParentSessionID = %q, want parent-1", sess.ParentSessionID)
	}
	if sess.Entrypoint != "subagent_thread_spawn" {
		t.Errorf("Entrypoint = %q, want subagent_thread_spawn", sess.Entrypoint)
	}
}

// TestParse_ExecEntrypointIsNonInteractive は `codex exec` のセッションが
// 非対話として扱われることを確かめる。対話／自動の内訳と、評価軸の読み替えの
// 両方がこの判定に乗っている。
func TestParse_ExecEntrypointIsNonInteractive(t *testing.T) {
	body := `{"timestamp":"2026-08-30T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","session_id":"sess-1","timestamp":"2026-08-30T10:00:00Z","cwd":"/w","source":"exec"}}
`
	sess := parseBody(t, body)

	if sess.Entrypoint != "exec" {
		t.Fatalf("Entrypoint = %q, want exec", sess.Entrypoint)
	}
	if model.IsInteractiveEntrypoint(sess.Entrypoint) {
		t.Error("codex exec が対話セッション扱いになっている")
	}
}

// TestParse_BrokenLinesAreSkipped は壊れた行を飛ばして読み進めることを確かめる。
// 1 行の破損でセッション全体を捨てると、その日の母集団が欠ける。
func TestParse_BrokenLinesAreSkipped(t *testing.T) {
	body := `{"timestamp":"2026-08-30T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","session_id":"sess-1","timestamp":"2026-08-30T10:00:00Z","cwd":"/w","source":"cli"}}
this is not json
{"timestamp":"2026-08-30T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"やあ"}]}}
`
	sess := parseBody(t, body)

	if len(sess.Messages) != 1 || sess.Messages[0].Text != "やあ" {
		t.Fatalf("Messages = %+v, want 壊れた行を飛ばして 1 件", sess.Messages)
	}
}

// TestParse_AllLinesBrokenIsError は、ファイル全体が壊れているときだけ
// エラーにすることを確かめる（呼び出し側が「取り込めなかった」と数えられるように）。
func TestParse_AllLinesBrokenIsError(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "sessions", "2026/08/30",
		"rollout-2026-08-30T10-00-00-broken.jsonl", "nope\nnot json either\n", false)

	if _, err := New(root).Parse(source.Ref{Source: sourceName, SessionID: "broken", Path: path}); err == nil {
		t.Fatal("Parse() = nil, want error")
	}
}

// TestParse_CompressedRollout は圧縮済みロールアウトを読めることを確かめる。
// Codex は書き終えて 7 日以上経ったファイルを zstd に圧縮するので、これを読めないと
// 過去ぶんの取り込みが 7 日より前で途切れる。
func TestParse_CompressedRollout(t *testing.T) {
	root := t.TempDir()
	path := writeRollout(t, root, "sessions", "2026/08/30",
		"rollout-2026-08-30T10-00-00-zstd.jsonl.zst", fullRollout, true)

	sess, err := New(root).Parse(source.Ref{Source: sourceName, SessionID: "zstd", Path: path})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if sess.FirstPrompt != "バグを直して" {
		t.Errorf("FirstPrompt = %q, want バグを直して", sess.FirstPrompt)
	}
}

// TestParse_ContentHashMatchesModel は、評価キャッシュの冪等キーが
// 正規化モデル側の規則で計算されていることを確かめる（ソースごとにずれると、
// 同じ内容のセッションが別物として再評価される）。
func TestParse_ContentHashMatchesModel(t *testing.T) {
	sess := parseBody(t, fullRollout)
	if want := model.ContentHash(sess.Messages); sess.ContentHash != want {
		t.Errorf("ContentHash = %q, want %q", sess.ContentHash, want)
	}
}

func TestIsMetaText(t *testing.T) {
	metas := []string{
		"# AGENTS.md instructions for /repo",
		"<user_shell_command>ls</user_shell_command>",
		"<turn_aborted>止めた</turn_aborted>",
		"<subagent_notification>done</subagent_notification>",
		`<codex_internal_context source="memory">…</codex_internal_context>`,
		"<skill>insights</skill>",
		"<environment_context>…</environment_context>",
	}
	for _, s := range metas {
		if !isMetaText(s) {
			t.Errorf("isMetaText(%q) = false, want true", s)
		}
	}
	// 判定は先頭一致に限る。本文の途中に印と同じ文字列が出てくることは
	// （たとえばこのツール自身の話をしているとき）普通にあるため。
	if isMetaText("この <skill> という書き方について教えて") {
		t.Error("本文の途中に印が現れただけで meta 扱いになっている")
	}
	if isMetaText("バグを直して") {
		t.Error("普通の発話が meta 扱いになっている")
	}
}
