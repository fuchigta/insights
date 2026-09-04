package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTagFromReleaseURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "https://github.com/fuchigta/insights/releases/tag/v1.2.3", want: "v1.2.3"},
		{in: "/releases/tag/v1.2.3", want: "v1.2.3"},
		{in: "/releases/tag/v1.2.3/", want: "v1.2.3"},
		{in: "https://github.com/fuchigta/insights/releases/tag/v1.2.3?foo=1", want: "v1.2.3"},
		// 形が違うものからタグらしきものを推測で拾わない。
		{in: "https://github.com/fuchigta/insights/releases", want: ""},
		{in: "https://github.com/login?return_to=%2Ffuchigta", want: ""},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		if got := tagFromReleaseURL(tt.in); got != tt.want {
			t.Errorf("tagFromReleaseURL(%q) = %q, 期待 %q", tt.in, got, tt.want)
		}
	}
}

func TestLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/releases/tag/v1.4.0", http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	got, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if got != "v1.4.0" {
		t.Errorf("LatestTag = %q, 期待 %q", got, "v1.4.0")
	}
}

// TestLatestTagUnexpectedResponse は、リリースが 1 つも無い等でリダイレクトされない
// ときに、タグらしき文字列をでっち上げずエラーにすることを確かめる。
func TestLatestTagUnexpectedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	if _, err := c.LatestTag(context.Background()); err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/v2.0.0", http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	res, err := c.Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Error("UpdateAvailable = false, 期待 true")
	}
	if res.Latest != "v2.0.0" || res.Current != "v1.0.0" {
		t.Errorf("Check = %+v", res)
	}
	if want := srv.URL + "/releases/tag/v2.0.0"; res.ReleaseURL != want {
		t.Errorf("ReleaseURL = %q, 期待 %q", res.ReleaseURL, want)
	}
}

func TestAssetName(t *testing.T) {
	// .github/workflows/release.yml が作るアセット名と一致していること。
	// ここがずれると、リリースはできているのに更新だけ 404 で落ちる。
	tests := map[string]string{
		"linux/amd64":   "insights-linux-amd64",
		"darwin/arm64":  "insights-darwin-arm64",
		"windows/amd64": "insights-windows-amd64.exe",
	}
	for in, want := range tests {
		var goos, goarch string
		for i := range in {
			if in[i] == '/' {
				goos, goarch = in[:i], in[i+1:]
			}
		}
		if got := AssetName(goos, goarch); got != want {
			t.Errorf("AssetName(%q, %q) = %q, 期待 %q", goos, goarch, got, want)
		}
	}
}

func TestResolveVersion(t *testing.T) {
	if got := ResolveVersion("v1.2.3"); got != "v1.2.3" {
		t.Errorf("ResolveVersion(\"v1.2.3\") = %q", got)
	}
	// テストバイナリのビルド情報にモジュールバージョンは入らないため、
	// dev のままになる（go install 経由のフォールバックはここでは働かない）。
	if got := ResolveVersion(DevVersion); got != DevVersion {
		t.Errorf("ResolveVersion(%q) = %q, 期待 %q", DevVersion, got, DevVersion)
	}
}

func TestDetectInstallMethod(t *testing.T) {
	if got := DetectInstallMethod("v1.2.3"); got != MethodRelease {
		t.Errorf("DetectInstallMethod(\"v1.2.3\") = %q, 期待 %q", got, MethodRelease)
	}
	// テストバイナリは ldflags もモジュールバージョンも持たないので開発ビルド扱い。
	// これにより、テスト中に更新確認がネットワークへ出ることがない。
	if got := DetectInstallMethod(DevVersion); got != MethodDevBuild {
		t.Errorf("DetectInstallMethod(%q) = %q, 期待 %q", DevVersion, got, MethodDevBuild)
	}
	// タグとして解釈できない値は、比較の基準が無いので更新対象にしない。
	if got := DetectInstallMethod("test"); got != MethodDevBuild {
		t.Errorf("DetectInstallMethod(\"test\") = %q, 期待 %q", got, MethodDevBuild)
	}
}
