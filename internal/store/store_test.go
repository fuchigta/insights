package store

import (
	"database/sql"
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
	if err := d.SaveEval("sess-eval", "claude-cli", "claude-opus-5", "v1", s.ContentHash, evalJSON, EvalRun{}); err != nil {
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

// TestEvalRunTotals_AggregatesAcrossSessions は、日報が meta.judge_cost_usd /
// meta.judge_session_ids を組み立てる際に使う EvalRunTotals の集計仕様を確認する回帰テスト。
// `insights judge` で先に評価を済ませてから日報を作る経路では、その場の実行結果ではなく
// この集計を頼りにコストと run_session_id を復元するため、複数セッション分の合計・
// run_session_id が空の評価の除外・対象外 prompt_version・空の sessionIDs を網羅する。
func TestEvalRunTotals_AggregatesAcrossSessions(t *testing.T) {
	d := openTestDB(t)

	sessions := []string{"sess-a", "sess-b", "sess-c"}
	for _, id := range sessions {
		if err := d.SaveSession(testSession(id), nil); err != nil {
			t.Fatalf("SaveSession(%s) error = %v", id, err)
		}
	}

	evalJSON := json.RawMessage(`{"outcome":"achieved"}`)
	if err := d.SaveEval("sess-a", "claude-cli", "claude-opus-5", "v1", "hash-sess-a", evalJSON,
		EvalRun{CostUSD: 0.01, SessionID: "run-1"}); err != nil {
		t.Fatalf("SaveEval(sess-a) error = %v", err)
	}
	if err := d.SaveEval("sess-b", "claude-cli", "claude-opus-5", "v1", "hash-sess-b", evalJSON,
		EvalRun{CostUSD: 0.02, SessionID: "run-2"}); err != nil {
		t.Fatalf("SaveEval(sess-b) error = %v", err)
	}
	// run_session_id が空（コストを取得できないバックエンドを想定）の評価は一覧から除外される。
	if err := d.SaveEval("sess-c", "claude-cli", "claude-opus-5", "v1", "hash-sess-c", evalJSON,
		EvalRun{CostUSD: 0.03, SessionID: ""}); err != nil {
		t.Fatalf("SaveEval(sess-c) error = %v", err)
	}

	total, runIDs, err := d.EvalRunTotals(sessions, "v1")
	if err != nil {
		t.Fatalf("EvalRunTotals() error = %v", err)
	}
	if want := 0.06; total < want-1e-9 || total > want+1e-9 {
		t.Errorf("EvalRunTotals() total = %v, want %v", total, want)
	}
	gotRunIDs := map[string]bool{}
	for _, id := range runIDs {
		gotRunIDs[id] = true
	}
	if len(gotRunIDs) != 2 || !gotRunIDs["run-1"] || !gotRunIDs["run-2"] {
		t.Errorf("EvalRunTotals() runIDs = %v, want [run-1 run-2]（run_session_id が空の評価は含まれないはず）", runIDs)
	}

	// 対象外の prompt_version を指定したら評価が見つからず 0 / 空になる。
	total, runIDs, err = d.EvalRunTotals(sessions, "v2")
	if err != nil {
		t.Fatalf("EvalRunTotals(v2) error = %v", err)
	}
	if total != 0 {
		t.Errorf("EvalRunTotals(v2) total = %v, want 0", total)
	}
	if runIDs != nil {
		t.Errorf("EvalRunTotals(v2) runIDs = %v, want nil", runIDs)
	}

	// sessionIDs が空スライスなら、対象が無いのでクエリを投げずに 0 / nil を返す。
	total, runIDs, err = d.EvalRunTotals(nil, "v1")
	if err != nil {
		t.Fatalf("EvalRunTotals(空) error = %v", err)
	}
	if total != 0 || runIDs != nil {
		t.Errorf("EvalRunTotals(空) = (%v, %v), want (0, nil)", total, runIDs)
	}
}

// TestSaveEval_UpsertOverwritesRunFields は、同じ (session_id, prompt_version) への
// SaveEval の再実行（ON CONFLICT 経路）で cost_usd / run_session_id も上書きされることを
// 確認する。評価結果本体だけ更新されてコスト情報が古いまま残ると、再評価のたびに
// meta.judge_cost_usd が積み上がってしまう（合計ではなく最新値であるべき）。
func TestSaveEval_UpsertOverwritesRunFields(t *testing.T) {
	d := openTestDB(t)
	s := testSession("sess-upsert")
	if err := d.SaveSession(s, nil); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	evalJSON := json.RawMessage(`{"outcome":"achieved"}`)
	if err := d.SaveEval("sess-upsert", "claude-cli", "claude-opus-5", "v1", s.ContentHash, evalJSON,
		EvalRun{CostUSD: 0.01, SessionID: "run-1"}); err != nil {
		t.Fatalf("SaveEval() 1回目 error = %v", err)
	}
	if err := d.SaveEval("sess-upsert", "claude-cli", "claude-opus-5", "v1", s.ContentHash, evalJSON,
		EvalRun{CostUSD: 0.05, SessionID: "run-2"}); err != nil {
		t.Fatalf("SaveEval() 2回目 error = %v", err)
	}

	total, runIDs, err := d.EvalRunTotals([]string{"sess-upsert"}, "v1")
	if err != nil {
		t.Fatalf("EvalRunTotals() error = %v", err)
	}
	if want := 0.05; total < want-1e-9 || total > want+1e-9 {
		t.Errorf("EvalRunTotals() total = %v, want %v（合算ではなく上書きのはず）", total, want)
	}
	if len(runIDs) != 1 || runIDs[0] != "run-2" {
		t.Errorf("EvalRunTotals() runIDs = %v, want [run-2]", runIDs)
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

// 検証対象に渡すのは「その日より前に作られた提案」だけ。当日の提案を当日に検証させると、
// 回し直すたびに閉じては作り直される積み上がりが起きる。
// 一方、同じ日の実行で閉じたものは回し直しのときに戻ってこないと、その日の検証結果が
// 前回実行の分だけ欠ける。
func TestActionsForVerification(t *testing.T) {
	d := openTestDB(t)

	mk := func(createdOn, title string) int64 {
		t.Helper()
		id, err := d.CreateAction(&model.Action{CreatedOn: createdOn, Title: title})
		if err != nil {
			t.Fatalf("CreateAction(%s) error = %v", title, err)
		}
		return id
	}

	past := mk("2026-08-28", "過去の未決着")
	sameDay := mk("2026-08-29", "当日に出した提案")
	closedToday := mk("2026-08-27", "当日の実行で閉じたもの")
	closedBefore := mk("2026-08-26", "前日までに閉じたもの")

	if err := d.UpdateActionStatus(closedToday, model.ActionDone, "実行された", "2026-08-29"); err != nil {
		t.Fatalf("UpdateActionStatus() error = %v", err)
	}
	if err := d.UpdateActionStatus(closedBefore, model.ActionDone, "実行された", "2026-08-28"); err != nil {
		t.Fatalf("UpdateActionStatus() error = %v", err)
	}

	got, err := d.ActionsForVerification("2026-08-29")
	if err != nil {
		t.Fatalf("ActionsForVerification() error = %v", err)
	}

	ids := map[int64]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	if !ids[past] {
		t.Error("過去の未決着提案が検証対象に入っていない")
	}
	if !ids[closedToday] {
		t.Error("同じ日の実行で閉じた提案が検証対象に戻っていない（回し直すと検証結果が欠ける）")
	}
	if ids[sameDay] {
		t.Error("当日に出した提案が検証対象に入っている（積み上がりの原因）")
	}
	if ids[closedBefore] {
		t.Error("前日までに決着済みの提案が検証対象に戻っている")
	}
}

func TestActionsCreatedOn_AndDeleteAction(t *testing.T) {
	d := openTestDB(t)

	mk := func(createdOn, title string) int64 {
		t.Helper()
		id, err := d.CreateAction(&model.Action{CreatedOn: createdOn, Title: title})
		if err != nil {
			t.Fatalf("CreateAction(%s) error = %v", title, err)
		}
		return id
	}

	before := mk("2026-08-28", "前日の提案")
	todayA := mk("2026-08-29", "当日の提案A")
	todayB := mk("2026-08-29", "当日の提案B")

	got, err := d.ActionsCreatedOn("2026-08-29")
	if err != nil {
		t.Fatalf("ActionsCreatedOn() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ActionsCreatedOn() len = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != todayA || got[1].ID != todayB {
		t.Errorf("ActionsCreatedOn() の並び = %d, %d, want %d, %d（ID 昇順）",
			got[0].ID, got[1].ID, todayA, todayB)
	}

	if err := d.DeleteAction(todayA); err != nil {
		t.Fatalf("DeleteAction() error = %v", err)
	}

	all, err := d.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("削除後の AllActions() len = %d, want 2: %+v", len(all), all)
	}
	for _, a := range all {
		if a.ID == todayA {
			t.Errorf("削除したはずの action(id=%d) が残っている", todayA)
		}
	}
	if all[0].ID != before {
		t.Errorf("別の日の提案まで消えている: %+v", all)
	}

	// 存在しない ID の削除はエラーにしない（呼び出し側が競合を気にせず消せるように）。
	if err := d.DeleteAction(todayA); err != nil {
		t.Errorf("存在しない ID の DeleteAction() error = %v, want nil", err)
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

// 旧バージョンで作られた DB（worktree 列が無い）を開いても、既存セッションが
// 読めたうえで worktree を保存・復元できること。
func TestMigrate_WorktreeColumnOnOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema_migrations の作成に失敗: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("schemaV1 の適用に失敗: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(timeLayout)); err != nil {
		t.Fatalf("schema_migrations への記録に失敗: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO sessions (session_id, source, project_path, started_at) VALUES (?, ?, ?, ?)`,
		"sess-old", "claude-code", "/proj", time.Now().UTC().Format(timeLayout)); err != nil {
		t.Fatalf("旧スキーマへのセッション保存に失敗: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("sql.DB.Close() error = %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open()（旧 DB を開く）error = %v", err)
	}
	defer d.Close()

	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	sess := &model.Session{
		Source: "claude-code", SessionID: "sess-wt",
		ProjectPath: "/proj", ProjectLabel: "proj", Worktree: "feat-x",
		StartedAt: start, EndedAt: start.Add(time.Minute), ContentHash: "hash-wt",
	}
	if err := d.SaveSession(sess, nil); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	rows, err := d.SessionsInRange(start.Add(-time.Hour), start.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.SessionID == "sess-wt" {
			found = true
			if r.Worktree != "feat-x" {
				t.Errorf("SessionRow.Worktree = %q, want %q", r.Worktree, "feat-x")
			}
		}
	}
	if !found {
		t.Fatal("保存したセッションが SessionsInRange に出てこない")
	}

	got, err := d.SessionByID("sess-wt")
	if err != nil {
		t.Fatalf("SessionByID() error = %v", err)
	}
	if got.Worktree != "feat-x" {
		t.Errorf("SessionByID().Worktree = %q, want %q", got.Worktree, "feat-x")
	}
}

// ワークツリーの畳み込みは取り込み時にしか効かないので、コードを直しても既存の行は
// 古いパスのまま残る。しかも mtime/size が変わらないファイルは再解析されないため、
// ingest --all でも直らない。DB を開いたときに移行されることを確かめる。
func TestMigrate_BackfillsWorktreeOnExistingRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db")

	// worktree 列を持たない時代（v1）の DB に、ワークツリーの cwd をそのまま
	// project_path として持つ行を入れておく。
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema_migrations の作成に失敗: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("schemaV1 の適用に失敗: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(timeLayout)); err != nil {
		t.Fatalf("schema_migrations への記録に失敗: %v", err)
	}
	insert := `INSERT INTO sessions (session_id, source, project_path, project_label, started_at) VALUES (?, ?, ?, ?, ?)`
	started := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC).Format(timeLayout)
	if _, err := raw.Exec(insert, "sess-wt", "claude-code",
		`C:\Users\me\src\insights\.claude\worktree\feat-x`, "feat-x", started); err != nil {
		t.Fatalf("ワークツリー行の投入に失敗: %v", err)
	}
	if _, err := raw.Exec(insert, "sess-plain", "claude-code",
		"/home/me/src/insights", "insights", started); err != nil {
		t.Fatalf("通常行の投入に失敗: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("sql.DB.Close() error = %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer d.Close()

	wt, err := d.SessionByID("sess-wt")
	if err != nil {
		t.Fatalf("SessionByID(sess-wt) error = %v", err)
	}
	if wt.Worktree != "feat-x" {
		t.Errorf("Worktree = %q, want feat-x", wt.Worktree)
	}
	if wt.ProjectPath != `C:\Users\me\src\insights` {
		t.Errorf("ProjectPath = %q, 元のプロジェクトへ寄っていない", wt.ProjectPath)
	}
	if wt.ProjectLabel != "insights" {
		t.Errorf("ProjectLabel = %q, want insights", wt.ProjectLabel)
	}

	// ワークツリーでない行は触らない。
	plain, err := d.SessionByID("sess-plain")
	if err != nil {
		t.Fatalf("SessionByID(sess-plain) error = %v", err)
	}
	if plain.ProjectPath != "/home/me/src/insights" || plain.Worktree != "" {
		t.Errorf("通常のセッションが書き換えられている: path=%q worktree=%q", plain.ProjectPath, plain.Worktree)
	}
}

// TestMigrate_UpgradesExistingV1Database は、v2 を知らないバージョンで作られた既存 DB を
// 開いたときに、データを保ったまま session_evals の追加カラムが使えるようになることを確かめる。
// 利用者の手元にあるのは必ず「前のバージョンで作られた DB」なので、新規作成のときだけ通っても
// 意味がない。
func TestMigrate_UpgradesExistingV1Database(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insights.db")

	// v1 までしか適用されていない DB を手で作る（旧バージョンが残した状態の再現）。
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema_migrations の作成に失敗: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("schemaV1 の適用に失敗: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, time.Now().UTC().Format(timeLayout)); err != nil {
		t.Fatalf("schema_migrations への記録に失敗: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO session_evals (session_id, judge, judge_model, prompt_version, content_hash, eval_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"sess-old", "claude-cli", "claude-opus-5", "v1", "hash-sess-old", `{"outcome":"achieved"}`, time.Now().UTC().Format(timeLayout)); err != nil {
		t.Fatalf("旧スキーマへの評価保存に失敗: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("sql.DB.Close() error = %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open()（v1 の DB を開く）error = %v", err)
	}
	defer d.Close()

	// 既存行は残り、追加カラムは既定値（0 / 空）として読める。
	got, ok, err := d.EvalFor("sess-old", "v1", "hash-sess-old")
	if err != nil {
		t.Fatalf("EvalFor() error = %v", err)
	}
	if !ok {
		t.Fatal("移行後に既存の評価が読めない")
	}
	if len(got) == 0 {
		t.Error("移行後の eval_json が空になっている")
	}
	total, runIDs, err := d.EvalRunTotals([]string{"sess-old"}, "v1")
	if err != nil {
		t.Fatalf("EvalRunTotals() error = %v", err)
	}
	if total != 0 || runIDs != nil {
		t.Errorf("移行直後の EvalRunTotals() = (%v, %v), want (0, nil)", total, runIDs)
	}

	// 移行後は新しいカラムに書き込める。
	if err := d.SaveEval("sess-old", "claude-cli", "claude-opus-5", "v1", "hash-sess-old",
		json.RawMessage(`{"outcome":"achieved"}`), EvalRun{CostUSD: 0.04, SessionID: "run-x"}); err != nil {
		t.Fatalf("SaveEval() error = %v", err)
	}
	total, runIDs, err = d.EvalRunTotals([]string{"sess-old"}, "v1")
	if err != nil {
		t.Fatalf("EvalRunTotals() error = %v", err)
	}
	if want := 0.04; total < want-1e-9 || total > want+1e-9 {
		t.Errorf("EvalRunTotals() total = %v, want %v", total, want)
	}
	if len(runIDs) != 1 || runIDs[0] != "run-x" {
		t.Errorf("EvalRunTotals() runIDs = %v, want [run-x]", runIDs)
	}

	// v3 で足した実行記録のテーブルも、v1 の DB を開いた時点で使えるようになっている。
	if err := d.SaveEvalRun(EvalRunRecord{
		SessionID: "sess-old", PromptVersion: "v1", Judge: "claude-cli",
		JudgeModel: "claude-opus-5", OK: true, CostUSD: 0.04, RunSessionID: "run-x",
	}); err != nil {
		t.Fatalf("SaveEvalRun() error = %v", err)
	}
	now := time.Now().UTC()
	stats, err := d.EvalRunStatsInRange(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("EvalRunStatsInRange() error = %v", err)
	}
	if stats.Total != 1 || stats.Succeeded != 1 {
		t.Errorf("移行後の stats = %+v, want Total=1 Succeeded=1", stats)
	}
}

// TestEvalRunStats_CountsAndFiltersByPeriod は評価実行の集計を確かめる。
// 期間で絞れないと、レポートに「いつの失敗か分からない数字」が載ってしまう。
func TestEvalRunStats_CountsAndFiltersByPeriod(t *testing.T) {
	d := openTestDB(t)

	runs := []EvalRunRecord{
		{SessionID: "s1", PromptVersion: "v1", Judge: "claude-cli", JudgeModel: "claude-opus-5", OK: true, CostUSD: 0.02, RunSessionID: "run-1"},
		{SessionID: "s2", PromptVersion: "v1", Judge: "claude-cli", JudgeModel: "claude-opus-5", OK: true, CostUSD: 0.03, RunSessionID: "run-2"},
		// 失敗した試行にもコストは発生しうるので、合計に含める。
		{SessionID: "s3", PromptVersion: "v1", Judge: "claude-cli", JudgeModel: "claude-opus-5",
			FailureKind: EvalFailureRateLimit, FailureReason: "レート制限", CostUSD: 0.01},
	}
	for _, r := range runs {
		if err := d.SaveEvalRun(r); err != nil {
			t.Fatalf("SaveEvalRun(%s) error = %v", r.SessionID, err)
		}
	}

	now := time.Now().UTC()
	stats, err := d.EvalRunStatsInRange(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("EvalRunStatsInRange() error = %v", err)
	}
	if stats.Total != 3 || stats.Succeeded != 2 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want Total=3 Succeeded=2 Failed=1", stats)
	}
	if want := 0.06; stats.CostUSD < want-1e-9 || stats.CostUSD > want+1e-9 {
		t.Errorf("CostUSD = %v, want %v（失敗した試行のぶんも含む）", stats.CostUSD, want)
	}
	if got := stats.FailuresByKind[EvalFailureRateLimit]; got != 1 {
		t.Errorf("FailuresByKind[%s] = %d, want 1", EvalFailureRateLimit, got)
	}

	// 期間外は 1 件も数えない。
	past, err := d.EvalRunStatsInRange(now.AddDate(0, 0, -10), now.AddDate(0, 0, -9))
	if err != nil {
		t.Fatalf("EvalRunStatsInRange(過去) error = %v", err)
	}
	if past.Total != 0 || past.CostUSD != 0 || len(past.FailuresByKind) != 0 {
		t.Errorf("期間外の stats = %+v, want ゼロ値", past)
	}
}
