package render_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/rollup"
)

// -update を付けて実行すると golden ファイルを再生成する。
//
//	go test ./internal/render/... -run Golden -update
var update = flag.Bool("update", false, "update golden files in testdata/")

func loadDaily(t *testing.T, path string) *rollup.Daily {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testdata の読み込みに失敗しました: %v", err)
	}
	var d rollup.Daily
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("testdata の JSON デコードに失敗しました: %v", err)
	}
	return &d
}

func compareGolden(t *testing.T, goldenPath string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("golden ファイルの書き込みに失敗しました: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden ファイルの読み込みに失敗しました: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("golden ファイルと一致しません: %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

func TestRenderDailyGolden(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))
	got, err := render.RenderDaily(d)
	if err != nil {
		t.Fatalf("RenderDaily が失敗しました: %v", err)
	}
	compareGolden(t, filepath.Join("testdata", "sample_daily.golden.md"), got)
}

func TestRenderRetroGolden(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))
	got, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}
	compareGolden(t, filepath.Join("testdata", "sample_retro.golden.md"), got)
}

// TestRenderEmptyDay は空の日（セッション0件）でも落ちず、意味の通る Markdown を
// 出力することを確認する。Daily が nil の場合、各スライスが nil の場合も対象にする。
func TestRenderEmptyDay(t *testing.T) {
	cases := map[string]*rollup.Daily{
		"nil":  nil,
		"zero": {},
		"emptyDate": {
			Date: "2026-08-30",
		},
	}

	for name, d := range cases {
		d := d
		t.Run(name, func(t *testing.T) {
			daily, err := render.RenderDaily(d)
			if err != nil {
				t.Fatalf("RenderDaily(%s) がエラーになりました: %v", name, err)
			}
			if len(daily) == 0 {
				t.Fatalf("RenderDaily(%s) が空を返しました", name)
			}
			if _, err := render.ParseFrontMatter(daily); err != nil {
				t.Fatalf("RenderDaily(%s) のフロントマターがパースできません: %v", name, err)
			}

			retro, err := render.RenderRetro(d)
			if err != nil {
				t.Fatalf("RenderRetro(%s) がエラーになりました: %v", name, err)
			}
			if len(retro) == 0 {
				t.Fatalf("RenderRetro(%s) が空を返しました", name)
			}
			fm, err := render.ParseFrontMatter(retro)
			if err != nil {
				t.Fatalf("RenderRetro(%s) のフロントマターがパースできません: %v", name, err)
			}
			if fm.AchievedRatio != -1 {
				t.Errorf("評価済みセッションが0件の日の achieved_ratio は -1 であるべきですが %v でした", fm.AchievedRatio)
			}
			if fm.Sessions != 0 {
				t.Errorf("空の日の sessions は 0 であるべきですが %d でした", fm.Sessions)
			}
		})
	}
}

// TestWriteReports は WriteReports が t.TempDir() 配下に規定パスでファイルを書けることを確認する。
func TestWriteReports(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))
	outDir := t.TempDir()

	dailyPath, retroPath, err := render.WriteReports(outDir, d)
	if err != nil {
		t.Fatalf("WriteReports が失敗しました: %v", err)
	}

	wantDaily := filepath.Join(outDir, "daily", "2026-08-29.md")
	wantRetro := filepath.Join(outDir, "retro", "2026-08-29.md")
	if dailyPath != wantDaily {
		t.Errorf("dailyPath = %q, want %q", dailyPath, wantDaily)
	}
	if retroPath != wantRetro {
		t.Errorf("retroPath = %q, want %q", retroPath, wantRetro)
	}

	dailyContent, err := os.ReadFile(dailyPath)
	if err != nil {
		t.Fatalf("日報ファイルが読めません: %v", err)
	}
	retroContent, err := os.ReadFile(retroPath)
	if err != nil {
		t.Fatalf("振り返りファイルが読めません: %v", err)
	}
	if len(dailyContent) == 0 || len(retroContent) == 0 {
		t.Fatalf("書き出されたファイルが空です")
	}
}

// TestWriteReportsEmptyDay は空の日でも WriteReports が落ちないことを確認する。
func TestWriteReportsEmptyDay(t *testing.T) {
	outDir := t.TempDir()
	dailyPath, retroPath, err := render.WriteReports(outDir, nil)
	if err != nil {
		t.Fatalf("WriteReports(nil) が失敗しました: %v", err)
	}
	if _, err := os.Stat(dailyPath); err != nil {
		t.Errorf("日報ファイルが作成されていません: %v", err)
	}
	if _, err := os.Stat(retroPath); err != nil {
		t.Errorf("振り返りファイルが作成されていません: %v", err)
	}
}

// pointFromFrontMatter は FrontMatter（サイドカー YAML の復元結果）から rollup.Point を
// 再構成する。サイドカーだけから Point が復元できることを示すための、テスト専用の変換。
func pointFromFrontMatter(fm *render.FrontMatter) rollup.Point {
	costByModel := make(map[string]float64, len(fm.CostByModel))
	for _, mc := range fm.CostByModel {
		costByModel[mc.Model] = mc.CostUSD
	}
	toMap := func(fcs []render.FacetCount) map[string]int {
		m := make(map[string]int, len(fcs))
		for _, fc := range fcs {
			m[fc.Key] = fc.Count
		}
		return m
	}
	return rollup.Point{
		Date:            fm.Date,
		Sessions:        fm.Sessions,
		DurationMinutes: fm.DurationMinutes,
		CostUSD:         fm.CostUSD,
		CostByModel:     costByModel,
		Outcome:         toMap(fm.Outcome),
		ModelFit:        toMap(fm.ModelFit),
		Ownership:       toMap(fm.Ownership),
		AchievedRatio:   fm.AchievedRatio,
	}
}

// TestSidecarRoundTrip は RenderMeta が生成したサイドカー YAML を ParseSidecar でパースし、
// rollup.Point を再構成した際に元の値と一致することを確認する。
// これが成立することが「サイドカー YAML だけから再集計できる」という要件の担保になる
// （これまで Markdown フロントマターに対して行っていたラウンドトリップテストを、
// フロントマターの縮小に伴いサイドカー YAML 側に置き換えたもの）。
func TestSidecarRoundTrip(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))

	// 期待値は testdata/sample_daily.json の内容から手計算した値
	// （render 側のロジックを経由せず、独立に検証するため）。
	wantPoint := rollup.Point{
		Date:            "2026-08-29",
		Sessions:        4,
		DurationMinutes: 95.75,
		CostUSD:         0.1754,
		CostByModel: map[string]float64{
			"claude-sonnet-5":            0.1254,
			"claude-opus-5":              0.05,
			"totally-unknown-model-9000": 0,
		},
		Outcome:       map[string]int{"achieved": 1, "partial": 1},
		ModelFit:      map[string]int{"appropriate": 1, "over": 1},
		Ownership:     map[string]int{"understood": 1, "partial": 1},
		AchievedRatio: 0.5, // achieved 1 / 評価済み 2
	}

	metaBytes, err := render.RenderMeta(d)
	if err != nil {
		t.Fatalf("RenderMeta が失敗しました: %v", err)
	}
	fm, err := render.ParseSidecar(metaBytes)
	if err != nil {
		t.Fatalf("サイドカー YAML のパースに失敗しました: %v", err)
	}

	gotPoint := pointFromFrontMatter(fm)
	if gotPoint.Date != wantPoint.Date {
		t.Errorf("Date = %q, want %q", gotPoint.Date, wantPoint.Date)
	}
	if gotPoint.Sessions != wantPoint.Sessions {
		t.Errorf("Sessions = %d, want %d", gotPoint.Sessions, wantPoint.Sessions)
	}
	if gotPoint.DurationMinutes != wantPoint.DurationMinutes {
		t.Errorf("DurationMinutes = %v, want %v", gotPoint.DurationMinutes, wantPoint.DurationMinutes)
	}
	if gotPoint.CostUSD != wantPoint.CostUSD {
		t.Errorf("CostUSD = %v, want %v", gotPoint.CostUSD, wantPoint.CostUSD)
	}
	if gotPoint.AchievedRatio != wantPoint.AchievedRatio {
		t.Errorf("AchievedRatio = %v, want %v", gotPoint.AchievedRatio, wantPoint.AchievedRatio)
	}
	for model, cost := range wantPoint.CostByModel {
		if gotPoint.CostByModel[model] != cost {
			t.Errorf("CostByModel[%q] = %v, want %v", model, gotPoint.CostByModel[model], cost)
		}
	}
	for key, count := range wantPoint.Outcome {
		if gotPoint.Outcome[key] != count {
			t.Errorf("Outcome[%q] = %d, want %d", key, gotPoint.Outcome[key], count)
		}
	}
	for key, count := range wantPoint.ModelFit {
		if gotPoint.ModelFit[key] != count {
			t.Errorf("ModelFit[%q] = %d, want %d", key, gotPoint.ModelFit[key], count)
		}
	}
	for key, count := range wantPoint.Ownership {
		if gotPoint.Ownership[key] != count {
			t.Errorf("Ownership[%q] = %d, want %d", key, gotPoint.Ownership[key], count)
		}
	}

	// モデル別トークン量・プロジェクト別集計・Meta も復元できることを確認する。
	gotModels := make([]string, 0, len(fm.ByModel))
	for _, m := range fm.ByModel {
		gotModels = append(gotModels, m.Model)
	}
	sort.Strings(gotModels)
	wantModels := []string{"claude-opus-5", "claude-sonnet-5", "totally-unknown-model-9000"}
	if len(gotModels) != len(wantModels) {
		t.Fatalf("ByModel の件数 = %d, want %d", len(gotModels), len(wantModels))
	}
	for i := range wantModels {
		if gotModels[i] != wantModels[i] {
			t.Errorf("ByModel[%d] = %q, want %q", i, gotModels[i], wantModels[i])
		}
	}
	for _, m := range fm.ByModel {
		if m.Model == "claude-sonnet-5" {
			if m.InputTokens != 1050 || m.OutputTokens != 2080 {
				t.Errorf("claude-sonnet-5 のトークン量が復元できません: %+v", m)
			}
		}
		if m.Model == "totally-unknown-model-9000" && m.Priced {
			t.Errorf("未知モデルの Priced が true になっています")
		}
	}

	if len(fm.ByProject) != 3 {
		t.Errorf("ByProject の件数 = %d, want 3", len(fm.ByProject))
	}

	if fm.PromptVersion != "2026-01-01" {
		t.Errorf("PromptVersion = %q, want %q", fm.PromptVersion, "2026-01-01")
	}
	if len(fm.UnknownModels) != 1 || fm.UnknownModels[0] != "totally-unknown-model-9000" {
		t.Errorf("UnknownModels = %v, want [totally-unknown-model-9000]", fm.UnknownModels)
	}
	if fm.UnevaluatedSessions != 2 {
		t.Errorf("UnevaluatedSessions = %d, want 2", fm.UnevaluatedSessions)
	}
	if fm.JudgeCostUSD != 0.0123 {
		t.Errorf("JudgeCostUSD = %v, want 0.0123", fm.JudgeCostUSD)
	}
}

// TestSidecarRoundTripEmptyDay は Daily が nil / 空でも RenderMeta -> ParseSidecar が
// 落ちず、achieved_ratio が -1 として復元できることを確認する。
func TestSidecarRoundTripEmptyDay(t *testing.T) {
	for name, d := range map[string]*rollup.Daily{"nil": nil, "zero": {}} {
		d := d
		t.Run(name, func(t *testing.T) {
			metaBytes, err := render.RenderMeta(d)
			if err != nil {
				t.Fatalf("RenderMeta(%s) が失敗しました: %v", name, err)
			}
			fm, err := render.ParseSidecar(metaBytes)
			if err != nil {
				t.Fatalf("ParseSidecar(%s) が失敗しました: %v", name, err)
			}
			if fm.AchievedRatio != -1 {
				t.Errorf("AchievedRatio = %v, want -1", fm.AchievedRatio)
			}
			if fm.Sessions != 0 {
				t.Errorf("Sessions = %d, want 0", fm.Sessions)
			}
		})
	}
}

// TestDailyAndRetroFrontMatterMatch は日報・振り返りが同じ最小フロントマターを
// 出力することを確認する（どちらか一方が消えても、少なくとも最小限の情報は
// 両方から同じように読み取れるようにする要件）。
func TestDailyAndRetroFrontMatterMatch(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))

	dailyMD, err := render.RenderDaily(d)
	if err != nil {
		t.Fatalf("RenderDaily が失敗しました: %v", err)
	}
	retroMD, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}

	dailyFM, err := render.ParseFrontMatter(dailyMD)
	if err != nil {
		t.Fatalf("日報のフロントマターがパースできません: %v", err)
	}
	retroFM, err := render.ParseFrontMatter(retroMD)
	if err != nil {
		t.Fatalf("振り返りのフロントマターがパースできません: %v", err)
	}

	if !reflect.DeepEqual(dailyFM, retroFM) {
		t.Errorf("日報と振り返りのフロントマターが一致しません:\n--- daily ---\n%+v\n--- retro ---\n%+v", dailyFM, retroFM)
	}
	if dailyFM.Meta == "" {
		t.Errorf("フロントマターにサイドカーへの相対パスがありません")
	}
}

// TestWriteReportsWithMeta は WriteReportsWithMeta がサイドカー YAML を
// <outDir>/meta/YYYY-MM-DD.yaml に書き出し、そのパスも返すことを確認する。
func TestWriteReportsWithMeta(t *testing.T) {
	d := loadDaily(t, filepath.Join("testdata", "sample_daily.json"))
	outDir := t.TempDir()

	dailyPath, retroPath, metaPath, err := render.WriteReportsWithMeta(outDir, d)
	if err != nil {
		t.Fatalf("WriteReportsWithMeta が失敗しました: %v", err)
	}

	wantMeta := filepath.Join(outDir, "meta", "2026-08-29.yaml")
	if metaPath != wantMeta {
		t.Errorf("metaPath = %q, want %q", metaPath, wantMeta)
	}
	metaContent, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("サイドカー YAML が読めません: %v", err)
	}
	if _, err := render.ParseSidecar(metaContent); err != nil {
		t.Errorf("サイドカー YAML がパースできません: %v", err)
	}

	// WriteReportsWithMeta も daily/retro の書き出しは WriteReports と同じであること。
	if _, err := os.Stat(dailyPath); err != nil {
		t.Errorf("日報ファイルが作成されていません: %v", err)
	}
	if _, err := os.Stat(retroPath); err != nil {
		t.Errorf("振り返りファイルが作成されていません: %v", err)
	}
}

// TestRolledUpCollapsedNotEnumerated は RolledUp のセッションが「その他 N 件」の
// 1 行に畳まれ、個々のセッション ID・内容が振り返りに列挙されないことを確認する
// （ユーザーレビュー: 価値に薄いセッションでレポートを埋めない）。
func TestRolledUpCollapsedNotEnumerated(t *testing.T) {
	d := &rollup.Daily{
		Date: "2026-08-29",
		ByProject: []rollup.ProjectStat{
			{
				ProjectPath:   "p1",
				ProjectLabel:  "テストプロジェクト",
				Sessions:      4,
				CostUSD:       0.55,
				AchievedRatio: -1,
				// Highlights を 1 件持たせる。個別に載せるセッションが 1 件も
				// 無いプロジェクトは「その他のプロジェクト」へ畳まれるため、
				// プロジェクト単位の RolledUp 表記を検証するにはここが必要。
				Highlights: []rollup.SessionCard{
					{
						SessionID:    "s-main",
						ProjectLabel: "テストプロジェクト",
						Title:        "主要な作業",
						CostUSD:      0.5,
						TotalCostUSD: 0.5,
						Priced:       true,
					},
				},
				RolledUp: rollup.RolledUpGroup{
					Sessions:        3,
					DurationMinutes: 12,
					CostUSD:         0.05,
					Reason:          "コスト・時間が小さいため",
				},
			},
		},
	}

	for _, tc := range []struct {
		name   string
		render func(*rollup.Daily) ([]byte, error)
	}{
		{"daily", render.RenderDaily},
		{"retro", render.RenderRetro},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.render(d)
			if err != nil {
				t.Fatalf("生成に失敗しました: %v", err)
			}
			s := string(out)
			if !strings.Contains(s, "その他 3 件") {
				t.Errorf("「その他 N 件」への畳み込み表記が見つかりません:\n%s", s)
			}
			if !strings.Contains(s, "コスト・時間が小さいため") {
				t.Errorf("RolledUp.Reason が出力に見つかりません:\n%s", s)
			}
		})
	}
}

// TestAchievedRatioNoDataNotZeroPercent は AchievedRatio == -1 のとき
// 「評価データなし」と表示され、「0%」や「0.0%」と誤読される表記にならないことを確認する。
func TestAchievedRatioNoDataNotZeroPercent(t *testing.T) {
	d := &rollup.Daily{
		Date: "2026-08-29",
		ByProject: []rollup.ProjectStat{
			{
				ProjectPath:   "p1",
				ProjectLabel:  "評価未実施プロジェクト",
				Sessions:      1,
				AchievedRatio: -1,
				// 個別に載せるセッションが無いプロジェクトは「その他の
				// プロジェクト」に畳まれ達成率の行が出ないため、1 件持たせる。
				Highlights: []rollup.SessionCard{
					{SessionID: "s1", ProjectLabel: "評価未実施プロジェクト", Title: "未評価の作業"},
				},
			},
		},
	}
	retro, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}
	s := string(retro)

	var achievedLine string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- 達成率:") {
			achievedLine = line
			break
		}
	}
	if achievedLine == "" {
		t.Fatalf("達成率の行が見つかりません:\n%s", s)
	}
	if !strings.Contains(achievedLine, "評価データなし") {
		t.Errorf("達成率の行に「評価データなし」が見つかりません: %q", achievedLine)
	}
	if strings.Contains(achievedLine, "%") {
		t.Errorf("AchievedRatio == -1 なのにパーセント表示になっています: %q", achievedLine)
	}
}
