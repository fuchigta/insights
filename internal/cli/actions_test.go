package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/store"
)

// --- テスト用ヘルパ ---

// runActionsCLI は NewRootCommand + newActionsCommand を組み合わせて実行する。
// root.go 自体は変更していないため、この組み立てはテスト側で毎回行う。
func runActionsCLI(t *testing.T, configPath, dbPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand("test")
	root.AddCommand(newActionsCommand())

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	fullArgs := append([]string{"--config", configPath, "--db", dbPath, "actions"}, args...)
	root.SetArgs(fullArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// seedActions は open/done/dropped の状態が異なる改善提案を 1 件ずつ DB に入れる。
func seedActions(t *testing.T, dbPath string) (openID, doneID, droppedID int64) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	openID, err = db.CreateAction(&model.Action{
		CreatedOn: "2026-01-01", Title: "open のタイトル", Detail: "詳細1", Category: "process",
		Status: model.ActionOpen,
	})
	if err != nil {
		t.Fatalf("CreateAction(open): %v", err)
	}

	doneID, err = db.CreateAction(&model.Action{
		CreatedOn: "2026-01-02", Title: "done のタイトル", Detail: "詳細2", Category: "cost",
		Status: model.ActionDone, Verdict: "改善が確認できた", VerifiedOn: "2026-01-10",
	})
	if err != nil {
		t.Fatalf("CreateAction(done): %v", err)
	}

	droppedID, err = db.CreateAction(&model.Action{
		CreatedOn: "2026-01-03", Title: "dropped のタイトル", Detail: "詳細3", Category: "workflow",
		Status: model.ActionDropped,
	})
	if err != nil {
		t.Fatalf("CreateAction(dropped): %v", err)
	}

	return openID, doneID, droppedID
}

// --- テスト本体 ---

func TestActionsList_DefaultShowsOpenOnlyWithSummary(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	seedActions(t, dbPath)

	stdout, _, err := runActionsCLI(t, configPath, dbPath, "--json")
	if err != nil {
		t.Fatalf("actions 実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	var payload actionsListResult
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v (stdout=%s)", jsonErr, stdout)
	}
	if payload.Total != 3 {
		t.Errorf("Total = %d, want 3", payload.Total)
	}
	if payload.Shown != 1 {
		t.Errorf("Shown = %d, want 1 (open のみ)", payload.Shown)
	}
	if len(payload.Actions) != 1 || payload.Actions[0].Status != "open" {
		t.Errorf("引数なしでは open のみが表示されるはず: %+v", payload.Actions)
	}

	counts := map[string]int{}
	for _, c := range payload.StatusSummary {
		counts[c.Status] = c.Count
	}
	if counts["open"] != 1 || counts["done"] != 1 || counts["dropped"] != 1 || counts["expired"] != 0 {
		t.Errorf("状態別サマリが不正です: %+v", payload.StatusSummary)
	}
}

func TestActionsList_AllShowsEverything(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	seedActions(t, dbPath)

	stdout, _, err := runActionsCLI(t, configPath, dbPath, "list", "--all", "--json")
	if err != nil {
		t.Fatalf("actions list --all 実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	var payload actionsListResult
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v", jsonErr)
	}
	if payload.Shown != 3 {
		t.Errorf("Shown = %d, want 3 (--all)", payload.Shown)
	}
}

func TestActionsList_StatusFilter(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	seedActions(t, dbPath)

	stdout, _, err := runActionsCLI(t, configPath, dbPath, "--status", "done", "--json")
	if err != nil {
		t.Fatalf("実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	var payload actionsListResult
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v", jsonErr)
	}
	if payload.Shown != 1 || len(payload.Actions) != 1 || payload.Actions[0].Status != "done" {
		t.Errorf("--status done の絞り込みが不正: %+v", payload)
	}
}

func TestActionsList_InvalidStatusIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	seedActions(t, dbPath)

	if _, _, err := runActionsCLI(t, configPath, dbPath, "--status", "bogus"); err == nil {
		t.Fatal("未知の --status はエラーになるはず")
	}
}

func TestActionsList_EmptyDBShowsGuidance(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	stdout, _, err := runActionsCLI(t, configPath, dbPath)
	if err != nil {
		t.Fatalf("actions 実行に失敗しました: %v", err)
	}
	if !strings.Contains(stdout, "まだ改善提案がありません") {
		t.Errorf("案内メッセージが含まれていません: %s", stdout)
	}
}

func TestActionsShow_NotFoundIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	seedActions(t, dbPath)

	_, _, err := runActionsCLI(t, configPath, dbPath, "show", "999999")
	if err == nil {
		t.Fatal("存在しない ID はエラーになるはず")
	}
}

func TestActionsShow_Found(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	_, doneID, _ := seedActions(t, dbPath)

	stdout, _, err := runActionsCLI(t, configPath, dbPath, "show", strconv.FormatInt(doneID, 10), "--json")
	if err != nil {
		t.Fatalf("actions show 実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	var payload actionDetailView
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v (stdout=%s)", jsonErr, stdout)
	}
	if payload.ID != doneID {
		t.Errorf("ID = %d, want %d", payload.ID, doneID)
	}
	if payload.Status != "done" {
		t.Errorf("Status = %q, want %q", payload.Status, "done")
	}
	if payload.Verdict == "" {
		t.Errorf("Verdict が空です（検証所見が表示されていません）")
	}
	if payload.Detail == "" {
		t.Errorf("Detail が空です（詳細が表示されていません）")
	}
}
