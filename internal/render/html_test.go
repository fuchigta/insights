package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/rollup"
)

// loadSeries は testdata の JSON を rollup.Series にデコードする。
func loadSeries(t *testing.T, path string) *rollup.Series {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testdata の読み込みに失敗しました: %v", err)
	}
	var s rollup.Series
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("testdata の JSON デコードに失敗しました: %v", err)
	}
	return &s
}

// sampleHTMLOptions は golden テスト用のオプション。
// タイトルに日本語・<script>・Windows パスを含め、エスケープの正しさを検証する。
func sampleHTMLOptions() render.HTMLOptions {
	return render.HTMLOptions{
		Title: `insights <script>alert('xss')</script> C:\Users\fuchigta\projects\insights`,
	}
}

// TestRenderHTMLGolden は RenderHTML の出力をゴールデンファイルと比較する。
// -update を付けて実行すると再生成する: go test ./internal/render/... -run Golden -update
func TestRenderHTMLGolden(t *testing.T) {
	s := loadSeries(t, filepath.Join("testdata", "sample_series.json"))
	got, err := render.RenderHTML(s, sampleHTMLOptions())
	if err != nil {
		t.Fatalf("RenderHTML が失敗しました: %v", err)
	}
	compareGolden(t, filepath.Join("testdata", "sample_series.golden.html"), got)
}

// TestRenderHTMLNoExternalReferences は最重要要件（外部リソース参照ゼロ）を検証する。
// http:// / https:// / cdn への参照が一切含まれていないことを確認する。
func TestRenderHTMLNoExternalReferences(t *testing.T) {
	s := loadSeries(t, filepath.Join("testdata", "sample_series.json"))
	got, err := render.RenderHTML(s, sampleHTMLOptions())
	if err != nil {
		t.Fatalf("RenderHTML が失敗しました: %v", err)
	}
	html := string(got)

	forbidden := []string{"http://", "https://", "//cdn", "cdn.", "<script src", "@import url("}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(html), strings.ToLower(f)) {
			t.Errorf("生成された HTML に外部参照らしき文字列 %q が含まれています", f)
		}
	}
}

// TestRenderHTMLEscapesScript は <script> を含む入力が生の <script> として
// 出力されず、正しくエスケープされることを確認する。
func TestRenderHTMLEscapesScript(t *testing.T) {
	s := loadSeries(t, filepath.Join("testdata", "sample_series.json"))
	got, err := render.RenderHTML(s, sampleHTMLOptions())
	if err != nil {
		t.Fatalf("RenderHTML が失敗しました: %v", err)
	}
	html := string(got)

	if strings.Contains(html, "<script>alert('xss')</script>") {
		t.Errorf("タイトルの <script> が生のまま出力されています")
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Errorf("アクションタイトルの <script> が生のまま出力されています")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("エスケープされた &lt;script&gt; が見つかりません（エスケープ自体が行われていない可能性があります）")
	}
	// 唯一許容される <script> はページ内に無いこと（JS を使わない方針の確認）。
	if strings.Contains(html, "<script>") || strings.Contains(html, "<script ") {
		t.Errorf("ページ内に <script> タグが存在します（JS を使わない方針に反します）")
	}
}

// TestRenderHTMLWindowsPathAndJapanese は Windows パスのバックスラッシュや
// 日本語テキストがそのまま読める形で（エスケープ崩れなく）出力されることを確認する。
func TestRenderHTMLWindowsPathAndJapanese(t *testing.T) {
	s := loadSeries(t, filepath.Join("testdata", "sample_series.json"))
	got, err := render.RenderHTML(s, sampleHTMLOptions())
	if err != nil {
		t.Fatalf("RenderHTML が失敗しました: %v", err)
	}
	html := string(got)

	if !strings.Contains(html, `C:\Users\fuchigta\projects\insights`) {
		t.Errorf("Windows パスがタイトルに見つかりません")
	}
	if !strings.Contains(html, "見積もりダッシュボード") {
		t.Errorf("日本語のアクション本文が出力に見つかりません")
	}
}

// TestRenderHTMLNilAndEmpty は Series が nil / 空でもパニックせず、
// 意味の通る HTML を返すことを確認する。
func TestRenderHTMLNilAndEmpty(t *testing.T) {
	cases := map[string]*rollup.Series{
		"nil":   nil,
		"zero":  {},
		"empty": {From: "2026-08-01", To: "2026-08-01"},
	}
	for name, s := range cases {
		s := s
		t.Run(name, func(t *testing.T) {
			got, err := render.RenderHTML(s, render.HTMLOptions{})
			if err != nil {
				t.Fatalf("RenderHTML(%s) がエラーになりました: %v", name, err)
			}
			if len(got) == 0 {
				t.Fatalf("RenderHTML(%s) が空を返しました", name)
			}
			html := string(got)
			if !strings.Contains(html, "<!doctype html>") {
				t.Errorf("RenderHTML(%s) が完全な HTML ドキュメントになっていません", name)
			}
			if !strings.Contains(html, "</html>") {
				t.Errorf("RenderHTML(%s) が閉じられていません", name)
			}
		})
	}
}

// TestRenderHTMLSingleDay は 1 日分のみの Series でも「推移」チャートが壊れないことを確認する。
func TestRenderHTMLSingleDay(t *testing.T) {
	s := &rollup.Series{
		From: "2026-08-29",
		To:   "2026-08-29",
		Points: []rollup.Point{
			{
				Date:            "2026-08-29",
				Sessions:        1,
				DurationMinutes: 45,
				CostUSD:         0.1,
				CostByModel:     map[string]float64{"claude-sonnet-5": 0.1},
				Outcome:         map[string]int{"achieved": 1},
				ModelFit:        map[string]int{"appropriate": 1},
				Ownership:       map[string]int{"understood": 1},
				AchievedRatio:   1,
			},
		},
		ByModel: []rollup.ModelUsage{
			{Model: "claude-sonnet-5", Sessions: 1, Responses: 3, InputTokens: 1000, OutputTokens: 2000, CacheReadTokens: 500, CacheWriteTokens: 100, CostUSD: 0.1, Priced: true},
		},
	}
	got, err := render.RenderHTML(s, render.HTMLOptions{})
	if err != nil {
		t.Fatalf("RenderHTML(1日分) がエラーになりました: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("RenderHTML(1日分) が空を返しました")
	}
	if !strings.Contains(string(got), "</html>") {
		t.Errorf("RenderHTML(1日分) が閉じられていません")
	}
}

// TestWriteHTML は RenderHTML の結果をファイルへ書き出せることを確認する。
func TestWriteHTML(t *testing.T) {
	s := loadSeries(t, filepath.Join("testdata", "sample_series.json"))
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "report.html")

	if err := render.WriteHTML(path, s, sampleHTMLOptions()); err != nil {
		t.Fatalf("WriteHTML が失敗しました: %v", err)
	}

	fileContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("書き出された HTML が読めません: %v", err)
	}
	rendered, err := render.RenderHTML(s, sampleHTMLOptions())
	if err != nil {
		t.Fatalf("RenderHTML が失敗しました: %v", err)
	}
	if string(fileContent) != string(rendered) {
		t.Errorf("WriteHTML の内容が RenderHTML の結果と一致しません")
	}
}

// TestWriteHTMLNilSeries は nil Series でも WriteHTML が落ちないことを確認する。
func TestWriteHTMLNilSeries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := render.WriteHTML(path, nil, render.HTMLOptions{}); err != nil {
		t.Fatalf("WriteHTML(nil) が失敗しました: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("HTML ファイルが作成されていません: %v", err)
	}
}
