package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/store"
)

func TestSessionSizeBucket(t *testing.T) {
	tests := []struct {
		messages int
		want     string
	}{
		{0, sizeBucketSmall},
		{mediumMinMessages - 1, sizeBucketSmall},
		{mediumMinMessages, sizeBucketMedium},
		{largeMinMessages - 1, sizeBucketMedium},
		{largeMinMessages, sizeBucketLarge},
		{1000, sizeBucketLarge},
	}
	for _, tt := range tests {
		if got := sessionSizeBucket(tt.messages); got != tt.want {
			t.Errorf("sessionSizeBucket(%d) = %q, want %q", tt.messages, got, tt.want)
		}
	}
}

// TestPercentileOf は分位点の取り方を確かめる。値を内挿せず実在した実績を返すこと、
// 標本が少なすぎるときは採用しないことが要点。
func TestPercentileOf(t *testing.T) {
	if _, ok := percentileOf([]float64{0.1, 0.2, 0.3, 0.4}, evalCostPercentile); ok {
		t.Errorf("標本 4 件で採用された。minSamplesForEstimate = %d 未満は採用しないはず", minSamplesForEstimate)
	}

	// 10 件なら ceil(0.9*10)-1 = 8 番目（0 始まり）。
	sorted := []float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.09, 0.99}
	got, ok := percentileOf(sorted, evalCostPercentile)
	if !ok {
		t.Fatal("標本 10 件が採用されなかった")
	}
	if want := 0.09; got != want {
		t.Errorf("percentileOf() = %v, want %v（内挿せず実在の値を返すはず）", got, want)
	}
}

// TestEvalCostEstimator_FallsBackWithoutSamples は実績が無いときに既定値へ落ちることを確かめる。
// 実績が貯まるまでは見積もりが出せない、では確認そのものが成立しないため。
func TestEvalCostEstimator_FallsBackWithoutSamples(t *testing.T) {
	db := newTempDB(t)

	e := newEvalCostEstimator(db, "claude-sonnet-5")
	got, fromActual := e.perSession(10)
	if fromActual {
		t.Error("実績が無いのに実績由来と報告された")
	}
	if want := estimateCostPerSession("claude-sonnet-5"); got != want {
		t.Errorf("perSession() = %v, want %v（既定値）", got, want)
	}
}

// TestEvalCostEstimator_UsesActuals は実績が貯まったら実績で見積もることを確かめる。
// 固定値だと対象が長いときに過小、短いときに過大に出る、というのが実績を使う動機。
func TestEvalCostEstimator_UsesActuals(t *testing.T) {
	db := newTempDB(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	// saveTestSession が作るセッションは 2 メッセージなので small 区分に入る。
	costs := []float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.20}
	for i, c := range costs {
		id := fmt.Sprintf("sess-%d", i)
		saveTestSession(t, db, testSessionSpec{
			SessionID: id, ProjectPath: "/proj-a", FirstPrompt: "task",
			StartedAt: base.Add(time.Duration(i) * time.Minute), CostUSD: 0.01, CostKnown: true,
		})
		if err := db.SaveEvalRun(store.EvalRunRecord{
			SessionID: id, PromptVersion: "v1", Judge: "claude-cli",
			JudgeModel: "claude-sonnet-5", OK: true, CostUSD: c, RunSessionID: "run-" + id,
		}); err != nil {
			t.Fatalf("SaveEvalRun(%s) error = %v", id, err)
		}
	}

	e := newEvalCostEstimator(db, "claude-sonnet-5")
	got, fromActual := e.perSession(2)
	if !fromActual {
		t.Fatal("実績が 6 件あるのに既定値へ落ちた")
	}
	// 6 件なら ceil(0.9*6)-1 = 5 番目、つまり最大値。安全側に倒すという意図どおり。
	if want := 0.20; got != want {
		t.Errorf("perSession() = %v, want %v", got, want)
	}

	// 別モデルの実績は混ざらない（単価が違うので混ぜると見積もりが壊れる）。
	other := newEvalCostEstimator(db, "claude-haiku-4-5")
	if _, fromActual := other.perSession(2); fromActual {
		t.Error("別モデルの実績が使われた")
	}

	rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	total, actualCount := e.estimateTargets(rows)
	if actualCount != len(rows) {
		t.Errorf("estimateTargets() の実績由来件数 = %d, want %d", actualCount, len(rows))
	}
	if want := 0.20 * float64(len(rows)); total < want-1e-9 || total > want+1e-9 {
		t.Errorf("estimateTargets() = %v, want %v", total, want)
	}
}

// TestEvalCostEstimator_Basis は見積もりの根拠の説明が実態に合うことを確かめる。
// 「何を根拠にした数字か」が分からないと、確認が儀式になってしまう。
func TestEvalCostEstimator_Basis(t *testing.T) {
	e := &evalCostEstimator{model: "claude-sonnet-5", byBucket: map[string][]float64{}}
	tests := []struct {
		name       string
		fromActual int
		total      int
		want       string
	}{
		{"実績なし", 0, 5, "既定値"},
		{"一部だけ実績", 2, 5, "一部"},
		{"全部実績", 5, 5, "過去の評価実績"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.estimateBasis(tt.fromActual, tt.total)
			if !strings.Contains(got, tt.want) {
				t.Errorf("estimateBasis(%d, %d) = %q, want %q を含む", tt.fromActual, tt.total, got, tt.want)
			}
		})
	}
}
