package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/store"
)

// このファイルは judge_test.go / daily_test.go / run_test.go が共有するテストヘルパを集める。
// claude を実際に呼ぶテストは書かない方針のため、AI 呼び出しはすべて judge.Judge のフェイク
// 実装で差し替える。

// newTempDB は t.TempDir() 上に新しい DB を開き、テスト終了時に Close する。
// 実際の ~/.insights には一切触れない。
func newTempDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := store.Open(filepath.Join(dir, "insights.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	return d
}

// testSessionSpec は saveTestSession への入力をまとめたもの。
type testSessionSpec struct {
	SessionID       string
	IsSidechain     bool
	ParentSessionID string
	ProjectPath     string
	ProjectLabel    string
	Worktree        string
	Title           string
	FirstPrompt     string
	StartedAt       time.Time
	EndedAt         time.Time
	CostUSD         float64
	CostKnown       bool
}

// saveTestSession は 1 メッセージの usage を持つ最小限のセッションを DB に保存する。
// judge/daily のテストで「対象セッション」を用意するための共通ヘルパ。
func saveTestSession(t *testing.T, db *store.DB, spec testSessionSpec) {
	t.Helper()

	if spec.EndedAt.IsZero() {
		spec.EndedAt = spec.StartedAt.Add(5 * time.Minute)
	}
	if spec.ProjectPath == "" {
		spec.ProjectPath = "/proj"
	}
	if spec.ProjectLabel == "" {
		spec.ProjectLabel = "proj"
	}

	sess := &model.Session{
		Source:          "claude-code",
		SessionID:       spec.SessionID,
		ProjectPath:     spec.ProjectPath,
		ProjectLabel:    spec.ProjectLabel,
		Worktree:        spec.Worktree,
		Entrypoint:      "cli",
		IsSidechain:     spec.IsSidechain,
		ParentSessionID: spec.ParentSessionID,
		StartedAt:       spec.StartedAt,
		EndedAt:         spec.EndedAt,
		FirstPrompt:     spec.FirstPrompt,
		Title:           spec.Title,
		ContentHash:     "hash-" + spec.SessionID,
		Messages: []model.Message{
			{Seq: 1, Timestamp: spec.StartedAt, Role: model.RoleUser, Text: spec.FirstPrompt},
			{
				Seq: 2, Timestamp: spec.StartedAt.Add(time.Minute), Role: model.RoleAssistant,
				Model: "claude-sonnet-5", Text: "done",
				Usage: &model.Usage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	costs := []store.UsageCost{{Seq: 2, CostUSD: spec.CostUSD, Known: spec.CostKnown}}
	if err := db.SaveSession(sess, costs); err != nil {
		t.Fatalf("SaveSession(%s) error = %v", spec.SessionID, err)
	}
}

// validEvalJSON は model.Eval として妥当な JSON を返す。judge.Judge のフェイク実装が
// セッション評価リクエストへの応答として使う。
func validEvalJSON(outcome string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"underlying_goal": "テストのゴール",
		"goal_category": "feature",
		"outcome": %q,
		"artifact_value": "durable",
		"intervention_cost": {"level": "low", "evidence": "少ない介入"},
		"rework": {"occurred": false, "cause": ""},
		"model_fit": {"verdict": "appropriate", "reason": "妥当"},
		"ownership": {"level": "understood", "reason": "理解済み"},
		"learning_value": "some",
		"friction": [],
		"outcome_summary": "テストの要約",
		"confidence": "high"
	}`, outcome))
}

// validDailyJSON / validRetroJSON は rollup.Synthesize が期待する JSON Schema
// （internal/rollup/synth.go の dailySchema / retroSchema）を満たす応答。
// rollup パッケージの非公開スキーマ変数には依存せず、フィールド一覧をそのまま複製している。
func validDailyJSON() json.RawMessage {
	return json.RawMessage(`{
		"headline": "テスト用の日報見出し",
		"body": "テスト用の日報本文。",
		"highlights": ["ハイライト1", "ハイライト2"]
	}`)
}

func validRetroJSON() json.RawMessage {
	return json.RawMessage(`{
		"body": "テスト用の振り返り本文。",
		"cost_observation": "コストは概ね妥当だった。",
		"proposals": [],
		"verifications": [],
		"outliers": []
	}`)
}

// isolateCodexSource は codex ソースのルートを空の一時ディレクトリに固定する。
//
// codex ソースは既定で有効で、root が空だと $CODEX_HOME（無ければ ~/.codex）に
// 解決される。テストでこれを放置すると、テストを走らせた人の実際の Codex ログを
// 読み込んでしまい、結果が実行環境によって変わる。
func isolateCodexSource(t *testing.T, cfg *config.Config) {
	t.Helper()
	cfg.Sources.Codex.Root = filepath.Join(t.TempDir(), "codex-home")
}
