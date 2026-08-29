package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// openTestDB は t.TempDir() 上に新しい DB を開き、テスト終了時に Close する。
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return d
}

// assertCount は table の行数を検証する。table 名はテストコード内の固定リテラルのみ想定。
func assertCount(t *testing.T, d *DB, table string, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("COUNT(%s) error = %v", table, err)
	}
	if got != want {
		t.Errorf("%s の行数 = %d, want %d", table, got, want)
	}
}

// testSession は SaveSession 系テスト用の最小構成セッション。
func testSession(id string) *model.Session {
	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	return &model.Session{
		Source:       "claude-code",
		SessionID:    id,
		ProjectPath:  "/home/user/project",
		ProjectLabel: "project",
		Entrypoint:   "cli",
		StartedAt:    base,
		EndedAt:      base.Add(10 * time.Minute),
		FirstPrompt:  "fix the bug",
		ContentHash:  "hash-" + id,
		Messages: []model.Message{
			{Seq: 1, Timestamp: base, Role: model.RoleUser, Text: "fix the bug"},
			{
				Seq: 2, Timestamp: base.Add(time.Minute), Role: model.RoleAssistant,
				Model: "claude-sonnet-5", Text: "done",
				Usage: &model.Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Seq: 3, Timestamp: base.Add(2 * time.Minute), Role: model.RoleTool,
				ToolName: "bash", IsError: true,
			},
		},
	}
}

func TestOpen_MigrationsAppliedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 1回目 error = %v", err)
	}
	var count1 int
	if err := d1.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count1); err != nil {
		t.Fatalf("schema_migrations の取得に失敗: %v", err)
	}
	if count1 != len(migrations) {
		t.Fatalf("1回目 Open 後の schema_migrations 行数 = %d, want %d", count1, len(migrations))
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 2回目 error = %v", err)
	}
	defer d2.Close()

	var count2 int
	if err := d2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count2); err != nil {
		t.Fatalf("schema_migrations の取得に失敗: %v", err)
	}
	if count2 != len(migrations) {
		t.Fatalf("2回目 Open 後の schema_migrations 行数 = %d, want %d（二重適用されている）", count2, len(migrations))
	}
}

func TestSaveSession_Idempotent(t *testing.T) {
	d := openTestDB(t)
	s := testSession("sess-1")
	costs := []UsageCost{{Seq: 2, CostUSD: 0.0015, Known: true}}

	if err := d.SaveSession(s, costs); err != nil {
		t.Fatalf("SaveSession() 1回目 error = %v", err)
	}
	if err := d.SaveSession(s, costs); err != nil {
		t.Fatalf("SaveSession() 2回目 error = %v", err)
	}

	assertCount(t, d, "sessions", 1)
	assertCount(t, d, "messages", 3)
	assertCount(t, d, "usage_events", 1)

	got, err := d.SessionByID("sess-1")
	if err != nil {
		t.Fatalf("SessionByID() error = %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(got.Messages))
	}
	if got.Messages[1].Usage == nil {
		t.Fatal("assistant message の Usage が復元されていない")
	}
	if got.Messages[1].Usage.InputTokens != 100 {
		t.Errorf("Usage.InputTokens = %d, want 100", got.Messages[1].Usage.InputTokens)
	}
	if !got.StartedAt.Equal(s.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, s.StartedAt)
	}
	if !got.Messages[2].IsError {
		t.Error("tool エラーメッセージの IsError が復元されていない")
	}
}

func TestSaveSession_ReplacesMessagesOnResave(t *testing.T) {
	d := openTestDB(t)
	s := testSession("sess-replace")
	if err := d.SaveSession(s, nil); err != nil {
		t.Fatalf("SaveSession() 1回目 error = %v", err)
	}

	// メッセージ数が変わる形で再保存しても、古い行が残らず入れ替わることを確認する。
	s.Messages = s.Messages[:1]
	if err := d.SaveSession(s, nil); err != nil {
		t.Fatalf("SaveSession() 2回目 error = %v", err)
	}

	assertCount(t, d, "sessions", 1)
	assertCount(t, d, "messages", 1)
	assertCount(t, d, "usage_events", 0)
}

func TestNeedsIngest(t *testing.T) {
	d := openTestDB(t)
	mtime := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

	needs, err := d.NeedsIngest("claude-code", "/path/a.jsonl", mtime, 100)
	if err != nil {
		t.Fatalf("NeedsIngest() error = %v", err)
	}
	if !needs {
		t.Fatal("未登録ファイルなのに NeedsIngest() = false")
	}

	if err := d.MarkIngested("claude-code", "/path/a.jsonl", mtime, 100, "hash1"); err != nil {
		t.Fatalf("MarkIngested() error = %v", err)
	}

	if needs, err = d.NeedsIngest("claude-code", "/path/a.jsonl", mtime, 100); err != nil {
		t.Fatalf("NeedsIngest() error = %v", err)
	} else if needs {
		t.Fatal("mtime/size 不変なのに NeedsIngest() = true")
	}

	if needs, err = d.NeedsIngest("claude-code", "/path/a.jsonl", mtime, 200); err != nil {
		t.Fatalf("NeedsIngest() error = %v", err)
	} else if !needs {
		t.Fatal("size の変化を検出できていない")
	}

	if needs, err = d.NeedsIngest("claude-code", "/path/a.jsonl", mtime.Add(time.Hour), 100); err != nil {
		t.Fatalf("NeedsIngest() error = %v", err)
	} else if !needs {
		t.Fatal("mtime の変化を検出できていない")
	}

	last, err := d.LastIngestAt()
	if err != nil {
		t.Fatalf("LastIngestAt() error = %v", err)
	}
	if last.IsZero() {
		t.Fatal("LastIngestAt() がゼロ値: MarkIngested 済みなので非ゼロのはず")
	}
}

func TestLastIngestAt_ZeroWhenEmpty(t *testing.T) {
	d := openTestDB(t)
	last, err := d.LastIngestAt()
	if err != nil {
		t.Fatalf("LastIngestAt() error = %v", err)
	}
	if !last.IsZero() {
		t.Errorf("未取り込み状態なのに LastIngestAt() = %v, want ゼロ値", last)
	}
}

func TestEvalCache_ContentHashMismatch(t *testing.T) {
	d := openTestDB(t)
	s := testSession("sess-eval")
	if err := d.SaveSession(s, nil); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	evalJSON := json.RawMessage(`{"outcome":"achieved"}`)
	if err := d.SaveEval("sess-eval", "claude-cli", "claude-opus-5", "v1", s.ContentHash, evalJSON); err != nil {
		t.Fatalf("SaveEval() error = %v", err)
	}

	got, ok, err := d.EvalFor("sess-eval", "v1", s.ContentHash)
	if err != nil {
		t.Fatalf("EvalFor() error = %v", err)
	}
	if !ok {
		t.Fatal("EvalFor() ok = false, want true（content_hash 一致）")
	}
	if string(got) != string(evalJSON) {
		t.Errorf("EvalFor() = %s, want %s", got, evalJSON)
	}

	if _, ok, err = d.EvalFor("sess-eval", "v1", "different-hash"); err != nil {
		t.Fatalf("EvalFor() error = %v", err)
	} else if ok {
		t.Fatal("content_hash が異なるのに EvalFor() ok = true")
	}

	if _, ok, err = d.EvalFor("sess-eval", "v2", s.ContentHash); err != nil {
		t.Fatalf("EvalFor() error = %v", err)
	} else if ok {
		t.Fatal("未登録の prompt_version なのに EvalFor() ok = true")
	}

	ids, err := d.UnevaluatedSessions(s.StartedAt.Add(-time.Hour), s.StartedAt.Add(time.Hour), "v1")
	if err != nil {
		t.Fatalf("UnevaluatedSessions(v1) error = %v", err)
	}
	for _, id := range ids {
		if id == "sess-eval" {
			t.Fatal("v1 は評価済みのはずなのに UnevaluatedSessions に含まれている")
		}
	}

	ids, err = d.UnevaluatedSessions(s.StartedAt.Add(-time.Hour), s.StartedAt.Add(time.Hour), "v2")
	if err != nil {
		t.Fatalf("UnevaluatedSessions(v2) error = %v", err)
	}
	found := false
	for _, id := range ids {
		if id == "sess-eval" {
			found = true
		}
	}
	if !found {
		t.Fatal("v2 は未評価のはずなのに UnevaluatedSessions に含まれていない")
	}
}

func TestSessionsInRange_Boundaries(t *testing.T) {
	d := openTestDB(t)

	day := func(y, m, dd, h int) time.Time { return time.Date(y, time.Month(m), dd, h, 0, 0, 0, time.UTC) }
	mkSession := func(id string, started time.Time) *model.Session {
		s := testSession(id)
		s.StartedAt = started
		s.EndedAt = started.Add(time.Minute)
		s.ContentHash = "hash-" + id
		return s
	}

	sessions := []*model.Session{
		mkSession("before", day(2026, 8, 28, 23)),
		mkSession("lower-bound", day(2026, 8, 29, 0)),
		mkSession("middle", day(2026, 8, 29, 12)),
		mkSession("upper-bound", day(2026, 8, 29, 23)),
		mkSession("after", day(2026, 8, 30, 0)),
	}
	for _, s := range sessions {
		if err := d.SaveSession(s, nil); err != nil {
			t.Fatalf("SaveSession(%s) error = %v", s.SessionID, err)
		}
	}

	from := day(2026, 8, 29, 0)
	to := day(2026, 8, 29, 23)

	rows, err := d.SessionsInRange(from, to)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}

	got := map[string]bool{}
	for _, r := range rows {
		got[r.SessionID] = true
	}
	for _, want := range []string{"lower-bound", "middle", "upper-bound"} {
		if !got[want] {
			t.Errorf("SessionsInRange() に %s が含まれていない", want)
		}
	}
	for _, notWant := range []string{"before", "after"} {
		if got[notWant] {
			t.Errorf("SessionsInRange() に範囲外の %s が含まれている", notWant)
		}
	}
}

func TestUsageInRange_Boundaries(t *testing.T) {
	d := openTestDB(t)

	mk := func(id string, ts time.Time) *model.Session {
		return &model.Session{
			Source: "claude-code", SessionID: id, StartedAt: ts, EndedAt: ts,
			ContentHash: "hash-" + id,
			Messages: []model.Message{
				{
					Seq: 1, Timestamp: ts, Role: model.RoleAssistant, Model: "claude-sonnet-5",
					Usage: &model.Usage{InputTokens: 10},
				},
			},
		}
	}

	base := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	sessions := []*model.Session{
		mk("before", base.Add(-time.Minute)),
		mk("lower", base),
		mk("upper", base.Add(23*time.Hour)),
		mk("after", base.Add(24*time.Hour)),
	}
	for _, s := range sessions {
		if err := d.SaveSession(s, nil); err != nil {
			t.Fatalf("SaveSession(%s) error = %v", s.SessionID, err)
		}
	}

	rows, err := d.UsageInRange(base, base.Add(23*time.Hour))
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.SessionID] = true
	}
	for _, want := range []string{"lower", "upper"} {
		if !got[want] {
			t.Errorf("UsageInRange() に %s が含まれていない", want)
		}
	}
	for _, notWant := range []string{"before", "after"} {
		if got[notWant] {
			t.Errorf("UsageInRange() に範囲外の %s が含まれている", notWant)
		}
	}
}

func TestSaveEvidence_Upsert(t *testing.T) {
	d := openTestDB(t)
	s := testSession("sess-evidence")
	if err := d.SaveSession(s, nil); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	ev := model.Evidence{SessionID: "sess-evidence", Kind: "commit", Ref: "abc123", Timestamp: s.StartedAt, Title: "fix"}
	if err := d.SaveEvidence([]model.Evidence{ev}); err != nil {
		t.Fatalf("SaveEvidence() 1回目 error = %v", err)
	}

	ev.Title = "fix v2"
	ev.Insertions = 5
	if err := d.SaveEvidence([]model.Evidence{ev}); err != nil {
		t.Fatalf("SaveEvidence() 2回目 error = %v", err)
	}

	got, err := d.EvidenceFor("sess-evidence")
	if err != nil {
		t.Fatalf("EvidenceFor() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("EvidenceFor() len = %d, want 1（UPSERT で行数が増えないはず）", len(got))
	}
	if got[0].Title != "fix v2" {
		t.Errorf("Title = %q, want %q", got[0].Title, "fix v2")
	}
	if got[0].Insertions != 5 {
		t.Errorf("Insertions = %d, want 5", got[0].Insertions)
	}
}

func TestRollup_SaveAndFetch(t *testing.T) {
	d := openTestDB(t)
	raw := json.RawMessage(`{"total_sessions":3}`)
	if err := d.SaveRollup("2026-08-29", raw, "/daily/2026-08-29.md", "/retro/2026-08-29.md"); err != nil {
		t.Fatalf("SaveRollup() error = %v", err)
	}

	got, ok, err := d.Rollup("2026-08-29")
	if err != nil {
		t.Fatalf("Rollup() error = %v", err)
	}
	if !ok {
		t.Fatal("Rollup() ok = false, want true")
	}
	if string(got) != string(raw) {
		t.Errorf("Rollup() = %s, want %s", got, raw)
	}

	if _, ok, err = d.Rollup("2026-08-30"); err != nil {
		t.Fatalf("Rollup() error = %v", err)
	} else if ok {
		t.Fatal("未登録日なのに Rollup() ok = true")
	}

	rows, err := d.RollupsInRange("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("RollupsInRange() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("RollupsInRange() len = %d, want 1", len(rows))
	}
}

func TestActions_CreateAndUpdateStatus(t *testing.T) {
	d := openTestDB(t)

	id, err := d.CreateAction(&model.Action{
		CreatedOn: "2026-08-29", Title: "テストを増やす", Category: "quality",
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}
	if id == 0 {
		t.Fatal("CreateAction() の ID が 0")
	}

	all, err := d.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("AllActions() len = %d, want 1", len(all))
	}
	if all[0].Status != model.ActionOpen {
		t.Errorf("初期 Status = %v, want %v", all[0].Status, model.ActionOpen)
	}

	open, err := d.ActionsByStatus(model.ActionOpen)
	if err != nil {
		t.Fatalf("ActionsByStatus(open) error = %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ActionsByStatus(open) len = %d, want 1", len(open))
	}

	if err := d.UpdateActionStatus(id, model.ActionDone, "効果あり", "2026-09-01"); err != nil {
		t.Fatalf("UpdateActionStatus() error = %v", err)
	}

	open, err = d.ActionsByStatus(model.ActionOpen)
	if err != nil {
		t.Fatalf("ActionsByStatus(open) error = %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("状態更新後の ActionsByStatus(open) len = %d, want 0", len(open))
	}

	done, err := d.ActionsByStatus(model.ActionDone, model.ActionDropped)
	if err != nil {
		t.Fatalf("ActionsByStatus(done, dropped) error = %v", err)
	}
	if len(done) != 1 {
		t.Fatalf("ActionsByStatus(done, dropped) len = %d, want 1", len(done))
	}
	if done[0].Verdict != "効果あり" {
		t.Errorf("Verdict = %q, want %q", done[0].Verdict, "効果あり")
	}
	if done[0].VerifiedOn != "2026-09-01" {
		t.Errorf("VerifiedOn = %q, want %q", done[0].VerifiedOn, "2026-09-01")
	}
}
