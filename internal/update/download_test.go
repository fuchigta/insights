package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newReleaseServer は GitHub Releases 相当の応答を返すサーバを立てる。
//
//	/releases/latest                          -> /releases/tag/<tag> へリダイレクト
//	/releases/download/<tag>/<asset>          -> assetPath の中身
//	/releases/download/<tag>/<asset>.sha256   -> sha256sum 形式の 1 行
//
// sumOverride が空でなければ、その値をチェックサムとして返す（不一致の検証に使う）。
// テストが実際の GitHub に触れないための土台であり、すべての取得系テストがこれを通る。
func newReleaseServer(t *testing.T, tag, assetPath, sumOverride string) *httptest.Server {
	t.Helper()

	sum := sumOverride
	if sum == "" {
		sum = sha256OfFile(t, assetPath)
	}
	asset := AssetName(runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, assetPath)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+asset+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, asset)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("ファイルを開けません (%s): %v", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("ハッシュの計算に失敗しました (%s): %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeTempFile は content を持つファイルを作って、そのパスを返す。
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("ファイルの作成に失敗しました (%s): %v", path, err)
	}
	return path
}

func TestDownloadTo(t *testing.T) {
	dir := t.TempDir()
	src := writeTempFile(t, dir, "asset.bin", "新しいバイナリの中身")
	srv := newReleaseServer(t, "v1.0.0", src, "")

	dest := filepath.Join(t.TempDir(), "downloaded")
	c := &Client{BaseURL: srv.URL}
	if err := c.DownloadTo(context.Background(), "v1.0.0", dest); err != nil {
		t.Fatalf("DownloadTo: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ダウンロード結果を読めません: %v", err)
	}
	if string(got) != "新しいバイナリの中身" {
		t.Errorf("中身が一致しません: %q", string(got))
	}
}

// TestDownloadToChecksumMismatch は、チェックサムが合わないとき壊れたファイルを
// 残さずエラーにすることを確かめる。
func TestDownloadToChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	src := writeTempFile(t, dir, "asset.bin", "中身")
	// 実際とは別のハッシュを返させる。
	srv := newReleaseServer(t, "v1.0.0", src, strings.Repeat("0", sha256.Size*2))

	dest := filepath.Join(t.TempDir(), "downloaded")
	c := &Client{BaseURL: srv.URL}
	err := c.DownloadTo(context.Background(), "v1.0.0", dest)
	if err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	if !strings.Contains(err.Error(), "チェックサム") {
		t.Errorf("チェックサムのエラーを期待した: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("チェックサム不一致のファイルが残っている")
	}
}

// TestDownloadToMissingAsset は、そのプラットフォーム向けのアセットが無い
// リリースでも、黙って壊れたファイルを置かないことを確かめる。
func TestDownloadToMissingAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded")
	c := &Client{BaseURL: srv.URL}
	if err := c.DownloadTo(context.Background(), "v1.0.0", dest); err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("失敗したのにファイルが残っている")
	}
}
