package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CacheFileName は更新確認の結果を覚えておくファイル名。
// 設定ファイルと同じディレクトリ（既定 ~/.insights）に置く。
//
// DB ではなくファイルにしているのは、更新確認が DB を開かないコマンド
// （config init / doctor / skill など）でも走るため。設定ファイルの隣に置くことで、
// --config を一時ディレクトリに向けるテストがそのまま隔離される。
const CacheFileName = "update-check.json"

// Cache は前回の更新確認の記録。
type Cache struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// Fresh は前回の確認から interval が経っていない（＝もう一度問い合わせる必要が無い）かを返す。
func (c Cache) Fresh(now time.Time, interval time.Duration) bool {
	if c.CheckedAt.IsZero() || interval <= 0 {
		return false
	}
	// 時計が巻き戻った場合（CheckedAt が未来）も「新しい」とみなす。
	// 巻き戻りのたびに毎回問い合わせに行くよりは、1 周期黙っているほうが害が小さい。
	return now.Sub(c.CheckedAt) < interval
}

// LoadCache は dir 配下のキャッシュを読む。無い・壊れている場合はゼロ値と ok=false を返す
// （更新確認はあくまで補助機能なので、読めないことをエラーとして持ち上げない）。
func LoadCache(dir string) (Cache, bool) {
	data, err := os.ReadFile(filepath.Join(dir, CacheFileName))
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, false
	}
	return c, true
}

// SaveCache は dir 配下へキャッシュを書く。書き込みは一時ファイル経由の置き換えで行う
// （複数の insights が同時に走っても、途中まで書けた JSON を残さないため）。
func SaveCache(dir string, c Cache) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("キャッシュ先の作成に失敗しました (%s): %w", dir, err)
	}

	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("キャッシュのシリアライズに失敗しました: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".insights-update-check-*")
	if err != nil {
		return fmt.Errorf("一時ファイルの作成に失敗しました: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("一時ファイルへの書き込みに失敗しました: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("一時ファイルのクローズに失敗しました: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, CacheFileName)); err != nil {
		return fmt.Errorf("キャッシュの書き込みに失敗しました: %w", err)
	}
	ok = true
	return nil
}
