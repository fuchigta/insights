// このファイルは、評価を実行する前に出す推定コストの算出を扱う。
//
// 以前は「1 セッションあたり固定値 × 件数」で出していたが、実費はセッションの長さで
// 大きく変動するため、対象が長いときは過小に、短いときは過大に出ていた。評価は API の
// 従量枠を消費し、枠は月あたり限られているので、見積もりの精度は「実行するかどうか」の
// 判断に直接効く。ここでは DB に残っている評価の実績（eval_runs）から算出する。
package cli

import (
	"math"
	"sort"

	"github.com/fuchigta/insights/internal/store"
)

const (
	// evalCostSampleLimit は見積もりに使う実績の件数（新しい順）。
	evalCostSampleLimit = 50

	// minSamplesForEstimate は実績を採用する最小件数。標本が少ないと外れ値 1 件で
	// 見積もりが跳ねるため、それ未満なら 1 段広い母集団か既定値に落とす。
	minSamplesForEstimate = 5

	// evalCostPercentile は実績から採る分位点。平均や中央値だと半分のケースで実費が
	// 見積もりを上回ってしまい、「この額までなら払う」という確認の意味が薄れる。
	// 安全側に倒して上側を採る。
	evalCostPercentile = 0.9
)

// セッション規模の区分。メッセージ数を使うのは、評価に渡す台本の長さ（＝入力トークン量）と
// よく相関し、取り込み時点で必ず分かる値だから。境界は実データの分布から決めた目安で、
// 厳密である必要はない（区分ごとに実績が貯まれば、境界の粗さは実績が吸収する）。
const (
	sizeBucketSmall  = "small"
	sizeBucketMedium = "medium"
	sizeBucketLarge  = "large"

	mediumMinMessages = 40
	largeMinMessages  = 150
)

// sessionSizeBucket はメッセージ数から規模の区分を返す。
func sessionSizeBucket(messages int) string {
	switch {
	case messages >= largeMinMessages:
		return sizeBucketLarge
	case messages >= mediumMinMessages:
		return sizeBucketMedium
	default:
		return sizeBucketSmall
	}
}

// evalCostEstimator は評価コストの実績を区分ごとに保持し、見積もりを返す。
type evalCostEstimator struct {
	model    string
	byBucket map[string][]float64 // 区分ごとの実コスト（昇順）
	all      []float64            // 区分によらない全実績（昇順）
}

// newEvalCostEstimator は DB の実績から見積もり器を作る。実績が引けなくても
// 見積もり自体は既定値で成立するため、エラーは返さず空の見積もり器を返す
// （見積もりのために評価を止めるのは本末転倒なので）。
func newEvalCostEstimator(db *store.DB, model string) *evalCostEstimator {
	e := &evalCostEstimator{model: model, byBucket: map[string][]float64{}}
	if db == nil {
		return e
	}
	samples, err := db.RecentEvalCostSamples(model, evalCostSampleLimit)
	if err != nil {
		return e
	}
	for _, s := range samples {
		bucket := sessionSizeBucket(s.MessageCount)
		e.byBucket[bucket] = append(e.byBucket[bucket], s.CostUSD)
		e.all = append(e.all, s.CostUSD)
	}
	for _, v := range e.byBucket {
		sort.Float64s(v)
	}
	sort.Float64s(e.all)
	return e
}

// perSession は 1 セッションあたりの見積もりを返す。第 2 戻り値は実績に基づくかどうか。
//
// 区分の実績 → 区分によらない実績 → 既定値、の順に落ちる。区分の実績が貯まるまでは
// 全体の実績で見積もるほうが、固定値よりは実態に近い。
func (e *evalCostEstimator) perSession(messages int) (float64, bool) {
	if v, ok := percentileOf(e.byBucket[sessionSizeBucket(messages)], evalCostPercentile); ok {
		return v, true
	}
	if v, ok := percentileOf(e.all, evalCostPercentile); ok {
		return v, true
	}
	return estimateCostPerSession(e.model), false
}

// estimateTargets は評価対象すべての見積もり合計と、そのうち実績で見積もれた件数を返す。
func (e *evalCostEstimator) estimateTargets(targets []store.SessionRow) (total float64, fromActual int) {
	for _, t := range targets {
		cost, actual := e.perSession(t.MessageCount)
		total += cost
		if actual {
			fromActual++
		}
	}
	return total, fromActual
}

// estimateBasis は見積もりの根拠を利用者に説明する 1 行を返す。
// 「何を根拠に出した数字か」が分からないと、確認そのものが儀式になってしまう。
func (e *evalCostEstimator) estimateBasis(fromActual, total int) string {
	switch {
	case fromActual == 0:
		return "見積もりの根拠: 実績がまだ無いため既定値（実測に基づく概算）を使っています"
	case fromActual < total:
		return "見積もりの根拠: 一部は過去の評価実績、残りは既定値です"
	default:
		return "見積もりの根拠: 過去の評価実績（モデル別・セッション規模別の上側 90 パーセンタイル）です"
	}
}

// percentileOf は昇順の値列から分位点を返す。標本が minSamplesForEstimate 未満なら
// 採用しない（第 2 戻り値 false）。分位点は最近傍順位法で、値を内挿しない
// ——実在した実績だけを見積もりに使うため。
func percentileOf(sorted []float64, p float64) (float64, bool) {
	if len(sorted) < minSamplesForEstimate {
		return 0, false
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx], true
}
