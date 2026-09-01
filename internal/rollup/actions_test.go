package rollup

import (
	"path/filepath"
	"testing"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/store"
)

// openTestDB は t.TempDir() 上に実 DB を開く。internal/store の openTestDB と同じ方針。
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := store.Open(filepath.Join(dir, "insights.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return d
}

func TestPersistRetro_RegistersNewProposals(t *testing.T) {
	db := openTestDB(t)

	r := &Retro{
		Proposals: []Proposal{
			{Title: "レビュー観点をチェックリスト化する", Detail: "次回から使う", Category: "process"},
		},
	}

	if err := PersistRetro(db, "2026-08-29", r); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(actions))
	}
	if actions[0].Title != "レビュー観点をチェックリスト化する" {
		t.Errorf("actions[0].Title = %q", actions[0].Title)
	}
	if actions[0].Status != model.ActionOpen {
		t.Errorf("actions[0].Status = %q, want %q", actions[0].Status, model.ActionOpen)
	}
	if actions[0].CreatedOn != "2026-08-29" {
		t.Errorf("actions[0].CreatedOn = %q, want 2026-08-29", actions[0].CreatedOn)
	}
}

func TestPersistRetro_DoesNotDuplicateOpenProposal(t *testing.T) {
	db := openTestDB(t)

	// 既存の open な提案（表記ゆれ: 前後空白・連続空白・大文字小文字違い）。
	if _, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-20",
		Title:     "  Review  Checklist  を作る",
		Status:    model.ActionOpen,
	}); err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}

	r := &Retro{
		Proposals: []Proposal{
			{Title: "review checklist を作る", Detail: "重複するはず", Category: "process"},
			{Title: "新しい提案", Detail: "これは新規", Category: "process"},
		},
	}

	if err := PersistRetro(db, "2026-08-29", r); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("len(actions) = %d, want 2（重複した提案は登録されない）: %+v", len(actions), actions)
	}
}

func TestPersistRetro_DoesNotDuplicateWithinSameRetro(t *testing.T) {
	db := openTestDB(t)

	r := &Retro{
		Proposals: []Proposal{
			{Title: "同じ提案", Detail: "1つ目", Category: "process"},
			{Title: "同じ提案", Detail: "2つ目（表記は同一）", Category: "process"},
		},
	}

	if err := PersistRetro(db, "2026-08-29", r); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1（同一振り返り内の重複も1件に収まる）: %+v", len(actions), actions)
	}
}

// 同じ日に daily を回し直したとき、前回の実行が出した提案は残らず、
// 最後の実行の提案だけになること。
func TestPersistRetro_SameDayRerunReplacesProposals(t *testing.T) {
	db := openTestDB(t)

	first := &Retro{Proposals: []Proposal{
		{Title: "提案A", Detail: "1回目"},
		{Title: "提案B", Detail: "1回目"},
	}}
	if err := PersistRetro(db, "2026-08-29", first); err != nil {
		t.Fatalf("1回目の PersistRetro() error = %v", err)
	}

	second := &Retro{Proposals: []Proposal{{Title: "提案C", Detail: "2回目"}}}
	if err := PersistRetro(db, "2026-08-29", second); err != nil {
		t.Fatalf("2回目の PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1（最後の実行の提案だけが残る）: %+v", len(actions), actions)
	}
	if actions[0].Title != "提案C" {
		t.Errorf("actions[0].Title = %q, want 提案C", actions[0].Title)
	}
}

// 回し直しで同じ提案がまた出てきた場合、その提案は作り直されず ID が保たれること
// （ID は `insights actions drop <ID>` で人が指すものなので、回すたびに変わると使えない）。
func TestPersistRetro_SameDayRerunKeepsRepeatedProposalID(t *testing.T) {
	db := openTestDB(t)

	if err := PersistRetro(db, "2026-08-29", &Retro{Proposals: []Proposal{{Title: "Review Checklist を作る"}}}); err != nil {
		t.Fatalf("1回目の PersistRetro() error = %v", err)
	}
	before, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("len(before) = %d, want 1", len(before))
	}

	// 表記ゆれ（大文字小文字・空白の数）があっても同じ提案として扱われること。
	second := &Retro{Proposals: []Proposal{{Title: "  review  checklist を作る  "}, {Title: "増えた提案"}}}
	if err := PersistRetro(db, "2026-08-29", second); err != nil {
		t.Fatalf("2回目の PersistRetro() error = %v", err)
	}

	after, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("len(after) = %d, want 2: %+v", len(after), after)
	}
	if after[0].ID != before[0].ID || after[0].Title != "Review Checklist を作る" {
		t.Errorf("出続けた提案の ID/タイトルが変わっている: before=%+v after=%+v", before[0], after[0])
	}
}

// 回し直しの掃除は「その日の実行が作ったまま誰も触っていない提案」だけを消し、
// 別の日の提案・手で畳んだもの・後の日に検証されたものは消さないこと。
func TestPersistRetro_SameDayRerunKeepsDecidedAndOtherDays(t *testing.T) {
	db := openTestDB(t)

	otherDay, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-28", Title: "前日の提案", Status: model.ActionOpen,
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}
	dropped, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-29", Title: "手で畳んだ提案", Status: model.ActionDropped,
		Verdict: "手動で見送り（2026-08-29）",
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}
	// 後の日の振り返りが検証したもの。当日分でも、その日の実行結果ではなくその後の判断。
	verifiedLater, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-29", Title: "後日検証された提案", Status: model.ActionOpen,
		Verdict: "まだ実行されていない", VerifiedOn: "2026-08-30",
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}
	stale, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-29", Title: "前回実行の提案", Status: model.ActionOpen,
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}

	if err := PersistRetro(db, "2026-08-29", &Retro{Proposals: []Proposal{{Title: "今回の提案"}}}); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	ids := map[int64]bool{}
	for _, a := range actions {
		ids[a.ID] = true
	}
	if !ids[otherDay] {
		t.Error("別の日に作られた提案が消えている")
	}
	if !ids[dropped] {
		t.Error("手で畳んだ提案が消えている（人の判断が回し直しで失われる）")
	}
	if !ids[verifiedLater] {
		t.Error("後の日に検証された提案が消えている（検証結果が失われる）")
	}
	if ids[stale] {
		t.Error("前回実行の提案が残っている（回し直しで積み上がる）")
	}
	if len(actions) != 4 {
		t.Fatalf("len(actions) = %d, want 4: %+v", len(actions), actions)
	}
}

// 回し直した結果、提案が 1 件も出なかった場合も前回実行の提案は残らないこと。
func TestPersistRetro_SameDayRerunWithoutProposalsClearsPrevious(t *testing.T) {
	db := openTestDB(t)

	if err := PersistRetro(db, "2026-08-29", &Retro{Proposals: []Proposal{{Title: "提案A"}}}); err != nil {
		t.Fatalf("1回目の PersistRetro() error = %v", err)
	}
	if err := PersistRetro(db, "2026-08-29", &Retro{}); err != nil {
		t.Fatalf("2回目の PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("len(actions) = %d, want 0: %+v", len(actions), actions)
	}
}

func TestPersistRetro_UpdatesActionStatusFromVerdicts(t *testing.T) {
	db := openTestDB(t)

	id, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-20",
		Title:     "既存の提案",
		Status:    model.ActionOpen,
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}

	r := &Retro{
		Verifications: []ActionVerdict{
			{ActionID: id, Title: "既存の提案", Status: "done", Verdict: "今日のセッションで実行が確認できた"},
		},
	}

	if err := PersistRetro(db, "2026-08-29", r); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(actions))
	}
	if actions[0].Status != model.ActionDone {
		t.Errorf("actions[0].Status = %q, want %q", actions[0].Status, model.ActionDone)
	}
	if actions[0].Verdict != "今日のセッションで実行が確認できた" {
		t.Errorf("actions[0].Verdict = %q", actions[0].Verdict)
	}
	if actions[0].VerifiedOn != "2026-08-29" {
		t.Errorf("actions[0].VerifiedOn = %q, want 2026-08-29", actions[0].VerifiedOn)
	}
}

func TestPersistRetro_IgnoresInvalidStatusButKeepsOthers(t *testing.T) {
	db := openTestDB(t)

	id1, err := db.CreateAction(&model.Action{CreatedOn: "2026-08-20", Title: "提案1", Status: model.ActionOpen})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}
	id2, err := db.CreateAction(&model.Action{CreatedOn: "2026-08-20", Title: "提案2", Status: model.ActionOpen})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}

	r := &Retro{
		Verifications: []ActionVerdict{
			{ActionID: id1, Title: "提案1", Status: "not-a-real-status", Verdict: "不正な値"},
			{ActionID: id2, Title: "提案2", Status: "dropped", Verdict: "もう関係ない"},
		},
	}

	if err := PersistRetro(db, "2026-08-29", r); err != nil {
		t.Fatalf("PersistRetro() error = %v", err)
	}

	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	byID := map[int64]model.Action{}
	for _, a := range actions {
		byID[a.ID] = a
	}
	if byID[id1].Status != model.ActionOpen {
		t.Errorf("不正な status は無視され、元の状態のままであるべき: %q", byID[id1].Status)
	}
	if byID[id2].Status != model.ActionDropped {
		t.Errorf("actions[id2].Status = %q, want %q", byID[id2].Status, model.ActionDropped)
	}
}

func TestPersistRetro_NilRetroIsNoop(t *testing.T) {
	db := openTestDB(t)
	if err := PersistRetro(db, "2026-08-29", nil); err != nil {
		t.Fatalf("PersistRetro(nil) error = %v", err)
	}
	actions, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("len(actions) = %d, want 0", len(actions))
	}
}
