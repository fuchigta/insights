// Package update は insights 自身のバージョン確認と自己更新を扱う。
//
// リリースは .github/workflows/release.yml が GitHub Releases に置いており、
// アセット名はバージョンを含まない固定形（insights-<goos>-<goarch>[.exe]）で、
// それぞれに .sha256 が添えられている。本パッケージはこの規約に乗るだけで、
// GitHub API は使わない（理由は LatestTag のコメント）。
//
// HTTP の宛先は Client のフィールドで差し替えられる。テストは httptest で
// リリース相当の応答を組み立て、実際の GitHub には一切触れない。
package update

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// DefaultBaseURL はリリースを取りに行く既定の宛先。
const DefaultBaseURL = "https://github.com/fuchigta/insights"

// DevVersion は -ldflags でバージョンが埋め込まれなかったときの値
// （cmd/insights/main.go の var version の初期値と一致させる）。
const DevVersion = "dev"

// defaultHTTPTimeout はバージョン確認 1 回に許す時間。
// 通常のコマンド実行の裏で走らせるため、長く待たない。
const defaultHTTPTimeout = 5 * time.Second

// Client はリリース情報の取得と自己更新を行う。ゼロ値でも使える
// （BaseURL と HTTP は既定値にフォールバックする）。
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Result はバージョン確認の結果。
type Result struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

// httpClient はリダイレクトを自動で追わないクライアントを返す。
// LatestTag が Location ヘッダそのものを読むため、追われると困る。
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Check は最新タグを取得し、current と比べた結果を返す。
func (c *Client) Check(ctx context.Context, current string) (Result, error) {
	latest, err := c.LatestTag(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Current:         current,
		Latest:          latest,
		UpdateAvailable: IsNewer(latest, current),
		ReleaseURL:      c.baseURL() + "/releases/tag/" + latest,
	}, nil
}

// LatestTag は最新リリースのタグ名を返す。
//
// GitHub API（/repos/.../releases/latest）ではなく、Web の /releases/latest が返す
// リダイレクト先（/releases/tag/<tag>）を読む。API の未認証レート制限は IP あたり
// 60 req/h で、NAT や CI の共有 IP では枯渇しうるのに対し、この経路には制限が無い。
// リリースノート本文は取れないが、通知に必要なのはタグとリリースページの URL だけ。
func (c *Client) LatestTag(ctx context.Context) (string, error) {
	url := c.baseURL() + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("リクエストの作成に失敗しました: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("最新リリースの確認に失敗しました: %w", err)
	}
	defer resp.Body.Close()

	// 300 番台ならリダイレクト先、200 なら（クライアントが追った後の）最終 URL を見る。
	// 呼び出し側が独自の http.Client を渡してリダイレクトを追う場合にも壊れないようにする。
	var target string
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		target = resp.Header.Get("Location")
	case resp.StatusCode == http.StatusOK && resp.Request != nil && resp.Request.URL != nil:
		target = resp.Request.URL.String()
	default:
		return "", fmt.Errorf("最新リリースの確認に失敗しました: 予期しない応答 %s", resp.Status)
	}

	tag := tagFromReleaseURL(target)
	if tag == "" {
		return "", fmt.Errorf("最新リリースのタグを特定できませんでした（リダイレクト先: %q）", target)
	}
	return tag, nil
}

// tagFromReleaseURL は .../releases/tag/<tag> の形から <tag> を取り出す。
// 形が違えば空文字を返す（推測でタグらしきものを拾わない）。
func tagFromReleaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	// クエリやフラグメントは付かない想定だが、付いても落とす。
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSuffix(raw, "/")

	dir, tag := path.Split(raw)
	if tag == "" || !strings.HasSuffix(strings.TrimSuffix(dir, "/"), "/releases/tag") {
		return ""
	}
	return tag
}

// userAgent は GitHub 側のログに何が来ているか分かる程度の識別子。
func userAgent() string {
	return fmt.Sprintf("insights/%s (%s/%s)", ResolveVersion(DevVersion), runtime.GOOS, runtime.GOARCH)
}

// ResolveVersion は -ldflags で埋め込まれたバージョンを、表示用の値に整える。
//
// リリースバイナリは -X main.version=<tag> でタグが入るが、`go install` で入れた
// 場合はそれが無く "dev" のままになる。その場合はモジュールのビルド情報
// （go install が記録するモジュールバージョン）にフォールバックする。
// これをしないと go install の利用者は常に "dev" と表示され、更新確認も働かない。
func ResolveVersion(embedded string) string {
	if embedded != "" && embedded != DevVersion {
		return embedded
	}
	if v := moduleVersion(); v != "" {
		return v
	}
	return DevVersion
}

// moduleVersion はビルド情報に記録されたモジュールバージョンを返す。
// ローカルの `go build` では "(devel)" になるため、その場合は空を返す。
func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return ""
	}
	v := bi.Main.Version
	if v == "" || v == "(devel)" {
		return ""
	}
	return v
}
