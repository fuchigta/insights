package rollup

import (
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/store"
)

// 作業中のため評価を保留したセッションは、時間・コストの集計には入り、カードには
// EvalPending が立ち、評価漏れ（UnevaluatedSessions）には数えない。
func TestBuildDaily_ActiveSessionIsPendingNotUnevaluated(t *testing.T) {
	base := mustTime(t, "2026-08-29T01:00:00Z")
	row := func(id string) store.SessionRow {
		return store.SessionRow{
			SessionID: id, ProjectPath: "/p", ProjectLabel: "p", Entrypoint: "cli",
			StartedAt: base, EndedAt: base.Add(20 * time.Minute),
		}
	}
	usage := func(id string) []store.UsageRow {
		return []store.UsageRow{{SessionID: id, Model: "claude-sonnet-5", CostUSD: 1.5, CostKnown: true}}
	}
	d, err := BuildDaily(DailyInput{
		Date:   "2026-08-29",
		Prices: testPrices(t),
		Sessions: []SessionData{
			{Row: row("active"), Usage: usage("active"), Active: true},
			{Row: row("missed"), Usage: usage("missed")},
		},
	})
	if err != nil {
		t.Fatalf("BuildDaily() error = %v", err)
	}
	if d.Meta.ActiveSessions != 1 || d.Meta.UnevaluatedSessions != 1 {
		t.Errorf("Meta = active %d / unevaluated %d, want 1 / 1", d.Meta.ActiveSessions, d.Meta.UnevaluatedSessions)
	}
	if d.Totals.CostUSD != 3.0 || d.Totals.DurationMinutes != 40 {
		t.Errorf("Totals = cost %v / %v 分, want 3.0 / 40（作業中も集計に含める）", d.Totals.CostUSD, d.Totals.DurationMinutes)
	}
	pending := map[string]bool{}
	for _, c := range d.Sessions {
		pending[c.SessionID] = c.EvalPending
	}
	if !pending["active"] || pending["missed"] {
		t.Errorf("EvalPending = %v, want active のみ true", pending)
	}
}
