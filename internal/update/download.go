package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// maxAssetBytes はダウンロードを打ち切るサイズ上限。バイナリは 30MB 程度なので
// 十分な余裕があり、宛先が壊れて無限にデータを返す事故でディスクを埋めない。
const maxAssetBytes = 200 << 20

// downloadTimeout はバイナリ 1 本のダウンロードに許す時間。
// バージョン確認（defaultHTTPTimeout）よりずっと長くてよい。
const downloadTimeout = 5 * time.Minute

// AssetName はリリースアセットのファイル名を返す。
// 名前は .github/workflows/release.yml が作る形と一致していること。
func AssetName(goos, goarch string) string {
	name := fmt.Sprintf("insights-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// assetURL は指定タグのアセット URL を組み立てる。
//
// /releases/latest/download/<name> という固定 URL も使えるが、あえてタグを含む形にする。
// 確認したタグとダウンロードするバイナリを一致させるためで、確認とダウンロードの間に
// 新しいリリースが出ても、報告した版と違うものを入れることがない。
func (c *Client) assetURL(tag, name string) string {
	return fmt.Sprintf("%s/releases/download/%s/%s", c.baseURL(), tag, name)
}

// downloadClient はアセット取得用の HTTP クライアント。
//
// httpClient() と分けているのは、あちらがリダイレクトを追わない設定だから。
// GitHub のリリースアセットは実体の配信ホストへリダイレクトされるため、
// 追わないクライアントで取りに行くと本文が空になる。
func (c *Client) downloadClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: downloadTimeout}
}

// DownloadTo はタグ tag のバイナリを destPath に書き出し、添付の .sha256 と照合する。
// 照合に失敗した場合は destPath を消してエラーを返す（壊れたファイルを残さない）。
func (c *Client) DownloadTo(ctx context.Context, tag, destPath string) error {
	name := AssetName(runtime.GOOS, runtime.GOARCH)

	want, err := c.fetchChecksum(ctx, tag, name)
	if err != nil {
		return err
	}

	got, err := c.fetchBinary(ctx, c.assetURL(tag, name), destPath)
	if err != nil {
		_ = os.Remove(destPath)
		return err
	}

	if !strings.EqualFold(got, want) {
		_ = os.Remove(destPath)
		return fmt.Errorf("ダウンロードしたバイナリのチェックサムが一致しません（期待 %s / 実際 %s）", want, got)
	}

	// 実行権を明示的に付け直す。destPath が既存ファイル（呼び出し側が作った一時ファイル）
	// の場合、O_CREATE のパーミッション指定は効かず 0o600 のまま残るため、
	// この後の試し実行が「権限がありません」で落ちる。
	if err := os.Chmod(destPath, 0o755); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("実行権の設定に失敗しました (%s): %w", destPath, err)
	}
	return nil
}

// fetchChecksum は <asset>.sha256 を取得して 16 進のハッシュだけを返す。
// 中身は sha256sum の出力（"<hex>  <filename>"）。
func (c *Client) fetchChecksum(ctx context.Context, tag, name string) (string, error) {
	url := c.assetURL(tag, name+".sha256")
	body, err := c.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("チェックサムの取得に失敗しました: %w", err)
	}
	defer body.Close()

	// チェックサムファイルは 1 行しかないので、まるごと読んでよい。
	data, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "", fmt.Errorf("チェックサムの読み込みに失敗しました: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("チェックサムファイルが空です: %s", url)
	}
	sum := fields[0]
	if len(sum) != sha256.Size*2 {
		return "", fmt.Errorf("チェックサムの形式が不正です: %q", sum)
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return "", fmt.Errorf("チェックサムの形式が不正です: %q", sum)
	}
	return sum, nil
}

// fetchBinary は url の内容を destPath に書き出し、書きながら計算した sha256 を返す。
// 一度に全部メモリへ載せず、io.Copy でハッシュとファイルの両方へ流す。
func (c *Client) fetchBinary(ctx context.Context, url, destPath string) (string, error) {
	body, err := c.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("バイナリの取得に失敗しました: %w", err)
	}
	defer body.Close()

	// 0o755 で作る。Unix では実行権が要る（Windows では無視される）。
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("ダウンロード先の作成に失敗しました (%s): %w", destPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, maxAssetBytes)); err != nil {
		return "", fmt.Errorf("バイナリの書き込みに失敗しました (%s): %w", destPath, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("ダウンロード先のクローズに失敗しました (%s): %w", destPath, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get は url へ GET し、200 なら本文を返す。呼び出し側が Close する。
func (c *Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("リクエストの作成に失敗しました: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.downloadClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: 予期しない応答 %s", url, resp.Status)
	}
	return resp.Body, nil
}
