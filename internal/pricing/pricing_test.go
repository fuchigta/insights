package pricing

import (
	"math"
	"testing"

	"github.com/fuchigta/insights/internal/model"
)

// テスト用の許容誤差。
const epsilon = 1e-9

func TestLoad_EmbeddedTableIsPriced(t *testing.T) {
	// prices.json は実際の Anthropic API 単価に差し替え済みなので、
	// 主要モデルには単価が入っており UnpricedModels() は空になるはずである。
	tbl, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if tbl.Version == "" {
		t.Fatal("Version が空: prices.json の version が読み込めていない")
	}

	wantRates := map[string]Rate{
		"claude-opus-5":    {Input: 5.0, Output: 25.0, CacheWrite5m: 6.25, CacheWrite1h: 10.0, CacheRead: 0.5},
		"claude-sonnet-5":  {Input: 3.0, Output: 15.0, CacheWrite5m: 3.75, CacheWrite1h: 6.0, CacheRead: 0.3},
		"claude-haiku-4-5": {Input: 1.0, Output: 5.0, CacheWrite5m: 1.25, CacheWrite1h: 2.0, CacheRead: 0.1},
	}
	for name, want := range wantRates {
		r, ok := tbl.rates[name]
		if !ok {
			t.Errorf("埋め込み prices.json にモデルが無い: %s", name)
			continue
		}
		if r != want {
			t.Errorf("rates[%q] = %+v, want %+v", name, r, want)
		}
	}

	if unpriced := tbl.UnpricedModels(); len(unpriced) != 0 {
		t.Errorf("UnpricedModels() = %v, want 空（全モデルに実際の単価が設定済み）", unpriced)
	}
}

func TestRate_TableDriven(t *testing.T) {
	overrides := map[string]Rate{
		"claude-sonnet-5": {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
		// 日付無しの基底名のみ登録し、フォールバック解決を検証する。
		"claude-haiku-4-5": {Input: 1, Output: 5, CacheWrite5m: 1.25, CacheWrite1h: 2, CacheRead: 0.1},
		// 完全一致が優先されることを検証するため、あえて日付付きの名前も別単価で登録する。
		"claude-haiku-4-5-20260101": {Input: 999, Output: 999, CacheWrite5m: 999, CacheWrite1h: 999, CacheRead: 999},
	}
	tbl, err := Load(overrides)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	tests := []struct {
		name      string
		model     string
		wantKnown bool
		wantInput float64
	}{
		{"既知モデルの完全一致", "claude-sonnet-5", true, 3},
		{"未知モデル", "totally-unknown-model", false, 0},
		{"日付サフィックス付きは完全一致優先", "claude-haiku-4-5-20260101", true, 999},
		{"日付サフィックスを剥がした前方一致でフォールバック", "claude-haiku-4-5-20251225", true, 1},
		{"前方一致でも解決できない日付なしの未知モデル", "claude-unknown-model-20251225", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := tbl.Rate(tt.model)
			if ok != tt.wantKnown {
				t.Fatalf("Rate(%q) known = %v, want %v", tt.model, ok, tt.wantKnown)
			}
			if !tt.wantKnown {
				return
			}
			if r.Input != tt.wantInput {
				t.Errorf("Rate(%q).Input = %v, want %v", tt.model, r.Input, tt.wantInput)
			}
		})
	}
}

func TestCost_MixedCategories(t *testing.T) {
	tbl, err := Load(map[string]Rate{
		"test-model": {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	u := model.Usage{
		InputTokens:     1_000_000,
		OutputTokens:    500_000,
		ThinkingTokens:  100_000,
		CacheCreation5m: 200_000,
		CacheCreation1h: 50_000,
		CacheRead:       2_000_000,
	}

	got, known := tbl.Cost("test-model", u)
	if !known {
		t.Fatal("Cost() known = false, want true")
	}

	// thinking_tokens は output_tokens の内訳であり、すでに OutputTokens=500,000 に
	// 含まれているため加算しない。カテゴリごとに手計算した内訳:
	//   input:      1,000,000 tok * $3.00 / 1M = 3.00
	//   output:       500,000 tok * $15.00/ 1M = 7.50 （ThinkingTokens=100,000 は無視する）
	//   cache 5m:     200,000 tok * $3.75/ 1M  = 0.75
	//   cache 1h:      50,000 tok * $6.00/ 1M  = 0.30
	//   cache read: 2,000,000 tok * $0.30/ 1M  = 0.60
	//   合計: 3.00 + 7.50 + 0.75 + 0.30 + 0.60 = 12.15
	want := 12.15
	if math.Abs(got-want) > epsilon {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestCost_UnknownModelDoesNotSilentlyZero(t *testing.T) {
	tbl, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got, known := tbl.Cost("nope-not-registered", model.Usage{InputTokens: 1_000_000})
	if known {
		t.Fatal("Cost() known = true for unregistered model, want false")
	}
	if got != 0 {
		t.Errorf("Cost() usd = %v for unknown model, want 0", got)
	}
}

func TestCost_ZeroRateIsKnownButZeroCost(t *testing.T) {
	// 埋め込み表の主要モデルには実際の単価が入るようになったため、単価が全項目 0 の
	// モデルは overrides で注入して作る。登録済みモデルであれば known=true のまま
	// コストだけが 0 になり、未知モデル(known=false)とは区別されなければならない。
	tbl, err := Load(map[string]Rate{
		"zero-rate-model": {},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got, known := tbl.Cost("zero-rate-model", model.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !known {
		t.Fatal("Cost() known = false for registered (but unpriced) model, want true")
	}
	if got != 0 {
		t.Errorf("Cost() usd = %v, want 0 (単価未設定)", got)
	}
}

func TestUnpricedModels_ExcludesPricedOverride(t *testing.T) {
	tbl, err := Load(map[string]Rate{
		"priced-model": {Input: 1},
		"zero-model":   {},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	names := tbl.UnpricedModels()
	foundZero, foundPriced := false, false
	for _, n := range names {
		if n == "zero-model" {
			foundZero = true
		}
		if n == "priced-model" {
			foundPriced = true
		}
	}
	if !foundZero {
		t.Error("zero-model が UnpricedModels() に含まれていない")
	}
	if foundPriced {
		t.Error("priced-model が UnpricedModels() に含まれてしまっている")
	}
}

func TestCost_SyntheticModelIsNonBillable(t *testing.T) {
	// "<synthetic>" は API 呼び出しを伴わない合成メッセージ用の擬似モデル名。
	// 未知モデルとして警告されず、常に known=true かつコスト 0 を返す必要がある。
	tbl, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got, known := tbl.Cost("<synthetic>", model.Usage{
		InputTokens:     1_000_000,
		OutputTokens:    1_000_000,
		CacheCreation5m: 1_000_000,
		CacheCreation1h: 1_000_000,
		CacheRead:       1_000_000,
	})
	if !known {
		t.Fatal("Cost() known = false for <synthetic>, want true")
	}
	if got != 0 {
		t.Errorf("Cost() usd = %v for <synthetic>, want 0", got)
	}

	for _, name := range tbl.UnpricedModels() {
		if name == "<synthetic>" {
			t.Error("<synthetic> が UnpricedModels() に含まれてしまっている（非課金の擬似モデルは対象外のはず）")
		}
	}
}

func TestCost_ThinkingTokensDoNotAffectCost(t *testing.T) {
	// thinking_tokens は output_tokens の内訳であり、Cost() は ThinkingTokens を
	// 一切使わない仕様。二重計上の回帰を防ぐため、ThinkingTokens だけを変えても
	// コストが変わらないことを検証する。
	tbl, err := Load(map[string]Rate{
		"test-model": {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	base := model.Usage{
		InputTokens:     1_000_000,
		OutputTokens:    500_000,
		CacheCreation5m: 200_000,
		CacheCreation1h: 50_000,
		CacheRead:       2_000_000,
	}

	withoutThinking := base
	withoutThinking.ThinkingTokens = 0

	withThinking := base
	withThinking.ThinkingTokens = 10_000

	gotWithout, knownWithout := tbl.Cost("test-model", withoutThinking)
	gotWith, knownWith := tbl.Cost("test-model", withThinking)

	if !knownWithout || !knownWith {
		t.Fatalf("Cost() known = (%v, %v), want (true, true)", knownWithout, knownWith)
	}
	if math.Abs(gotWithout-gotWith) > epsilon {
		t.Errorf("ThinkingTokens の値でコストが変化した: ThinkingTokens=0 -> %v, ThinkingTokens=10000 -> %v", gotWithout, gotWith)
	}
}
