package cli

import (
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/store"
)

// nowFunc は「現在時刻」の取得元。作業中かどうかの判定が時刻に依存するため、
// テストで差し替えられるよう変数にしてある。
var nowFunc = time.Now

// activeWindow は「作業中のセッション」を判定するための基準。
//
// ログには終了の記録が無いので、最後の記録（EndedAt）からの経過時間でしか終わったかを
// 判断できない。insights をそのセッションの中から実行した場合も、実行コマンド自体が
// 記録されるので、この判定に必ず引っかかる。
type activeWindow struct {
	Now   time.Time
	Grace time.Duration // 0 以下なら判定しない
}

// newActiveWindow は設定と --include-active から判定基準を作る。
// includeActive が立っていれば猶予を 0 にして、作業中の扱いを無効にする。
func newActiveWindow(cfg *config.Config, includeActive bool) activeWindow {
	if includeActive || cfg == nil {
		return activeWindow{}
	}
	return activeWindow{Now: nowFunc(), Grace: cfg.Judge.ActiveGrace.Duration}
}

// isActive は row が作業中（最後の記録から猶予以内）かを返す。
// EndedAt が不明なセッションは判断材料が無いので作業中とはみなさない。
func (w activeWindow) isActive(row store.SessionRow) bool {
	if w.Grace <= 0 || row.EndedAt.IsZero() {
		return false
	}
	return w.Now.Sub(row.EndedAt) < w.Grace
}
