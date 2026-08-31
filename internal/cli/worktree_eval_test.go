package cli

import (
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/judge/prompts"
)

// ワークツリーで動いたサブエージェントは個別に評価する。ワークツリーは並列に本作業を
// 進めるために切られるので、親の「委譲 N 件」に埋めるとその日の実作業が評価されない。
// 一方、ワークツリーでない通常のサブエージェントは従来どおり対象外のままであること。
func TestPrepareEvalTargets_WorktreeSidechainIsEvaluated(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "parent", ProjectPath: "/proj", Title: "親", FirstPrompt: "頼む",
		StartedAt: base,
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "wt-child", ProjectPath: "/proj", Worktree: "feat-x",
		IsSidechain: true, ParentSessionID: "parent", Title: "ワークツリーでの並行作業",
		FirstPrompt: "実装する", StartedAt: base.Add(time.Minute),
	})
	saveTestSession(t, db, testSessionSpec{
		SessionID: "plain-child", ProjectPath: "/proj",
		IsSidechain: true, ParentSessionID: "parent", Title: "ふつうの委譲",
		FirstPrompt: "調べる", StartedAt: base.Add(2 * time.Minute),
	})

	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	usage, err := db.UsageInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("UsageInRange() error = %v", err)
	}

	targets, sidechainExcluded, _, _, _, err := prepareEvalTargets(db, rows, usage, false, prompts.PromptVersion)
	if err != nil {
		t.Fatalf("prepareEvalTargets() error = %v", err)
	}

	got := map[string]bool{}
	for _, r := range targets {
		got[r.SessionID] = true
	}
	if !got["parent"] {
		t.Error("親セッションが評価対象に入っていません")
	}
	if !got["wt-child"] {
		t.Error("ワークツリーのサブエージェントが評価対象に入っていません")
	}
	if got["plain-child"] {
		t.Error("ワークツリーでないサブエージェントまで評価対象に入っています")
	}
	if sidechainExcluded != 1 {
		t.Errorf("sidechainExcluded = %d, want 1（ワークツリーでない 1 件だけ）", sidechainExcluded)
	}
}
