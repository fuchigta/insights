package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if _, ok := LoadCache(dir); ok {
		t.Fatal("キャッシュが無いのに ok = true")
	}

	now := time.Now().Truncate(time.Second)
	if err := SaveCache(dir, Cache{CheckedAt: now, LatestVersion: "v1.2.3"}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	got, ok := LoadCache(dir)
	if !ok {
		t.Fatal("保存したキャッシュを読めない")
	}
	if got.LatestVersion != "v1.2.3" || !got.CheckedAt.Equal(now) {
		t.Errorf("LoadCache = %+v, 期待 %v / %s", got, now, "v1.2.3")
	}

	// 一時ファイルが残っていないこと（書き込みは rename での置き換え）。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != CacheFileName {
		t.Errorf("想定外のファイルが残っている: %v", entries)
	}
}

// TestLoadCacheBroken は、壊れたキャッシュをエラーとして持ち上げず、
// 「無かったこと」にして再取得へ倒すことを確かめる。
// 更新確認は補助機能であり、キャッシュの破損でコマンドを止めるべきではない。
func TestLoadCacheBroken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CacheFileName), []byte("{壊れている"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok := LoadCache(dir); ok {
		t.Error("壊れたキャッシュで ok = true")
	}
}

func TestCacheFresh(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		cache    Cache
		interval time.Duration
		want     bool
	}{
		{name: "未確認", cache: Cache{}, interval: time.Hour},
		{name: "間隔内", cache: Cache{CheckedAt: now.Add(-time.Minute)}, interval: time.Hour, want: true},
		{name: "間隔を過ぎている", cache: Cache{CheckedAt: now.Add(-2 * time.Hour)}, interval: time.Hour},
		{name: "間隔が 0", cache: Cache{CheckedAt: now}, interval: 0},
		// 時計が巻き戻った場合。毎回問い合わせに行くより 1 周期黙るほうが害が小さい。
		{name: "確認時刻が未来", cache: Cache{CheckedAt: now.Add(time.Hour)}, interval: time.Hour, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cache.Fresh(now, tt.interval); got != tt.want {
				t.Errorf("Fresh = %v, 期待 %v", got, tt.want)
			}
		})
	}
}
