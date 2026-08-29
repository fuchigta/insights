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
