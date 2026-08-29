package rollup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/model"
)

// fakeJudge は judge.Judge のテスト用フェイク実装。実際の claude サブプロセスは呼ばない。
// System プロンプトの内容（daily.md か retro.md か）に応じて応答を切り替える。
type fakeJudge struct {
	dailyResponse json.RawMessage
	dailyErr      error
	retroResponse json.RawMessage
	retroErr      error

	calls []judge.Request
}

func (f *fakeJudge) Name() string     { return "fake-judge" }
func (f *fakeJudge) Available() error { return nil }
func (f *fakeJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	f.calls = append(f.calls, req)
	// System プロンプトの中身で日報／振り返りのどちらの呼び出しかを判別する
	// （このテストファイルは package rollup 内なので、非公開のテンプレート変数を直接参照できる）。
	if req.System == retroPromptTemplate {
		if f.retroErr != nil {
			return nil, f.retroErr
		}
		return f.retroResponse, nil
	}
	if f.dailyErr != nil {
		return nil, f.dailyErr
	}
	return f.dailyResponse, nil
}

func validDailyJSON() json.RawMessage {
	return json.RawMessage(`{
		"headline": "APIの不具合を修正した",
		"body": "プロジェクトAでバグ修正を行った。",
		"highlights": ["バグ修正", "テスト追加"]
	}`)
}

// validRetroJSON は project_reviews のキーが sampleDaily() の ByProject の
// project_path（"/repo/proj-a", "/repo/proj-b"）とちょうど一致するレスポンス。
func validRetroJSON() json.RawMessage {
	return json.RawMessage(`{
		"headline": "コストに見合う価値はおおむね出せた",
		"verdict": "mixed",
		"body": "全体としてはキャッシュを有効に使えていた。",
		"cost_observation": "s1 のコストが最も高かったが、成果と釣り合っている。",
		"proposals": [
			{"title": "PRごとにレビュー観点をチェックリスト化する", "detail": "次回のPRからチェックリストを使う", "category": "process"}
		],
		"verifications": [
			{"action_id": 1, "title": "既存の提案", "status": "done", "verdict": "今日のセッションで実行が確認できた"}
		],
		"outliers": [],
		"project_reviews": {
			"/repo/proj-a": {
				"verdict": "worth_it",
				"summary": "s1でバグ修正が完了し価値が出た。",
				"improvement": ""
			},
			"/repo/proj-b": {
				"verdict": "mixed",
				"summary": "s2は途中で止まった。",
				"improvement": "着手前にゴールを明文化する"
			}
		}
	}`)
}

func sampleDaily() *Daily {
	return &Daily{
		Date: "2026-08-29",
		Sessions: []SessionCard{
			{SessionID: "s1", ProjectLabel: "proj-a", CostUSD: 1.0, Priced: true},
			{SessionID: "s2", ProjectLabel: "proj-b", CostUSD: 0.5, Priced: true},
		},
		Facets: Facets{
			Outcome: map[string]int{"achieved": 1, "partial": 1},
		},
		ByProject: []ProjectStat{
			{ProjectPath: "/repo/proj-a", ProjectLabel: "proj-a", CostUSD: 1.0},
			{ProjectPath: "/repo/proj-b", ProjectLabel: "proj-b", CostUSD: 0.5},
		},
	}
}

func TestSynthesize_Success(t *testing.T) {
	fj := &fakeJudge{
		dailyResponse: validDailyJSON(),
		retroResponse: validRetroJSON(),
	}
	d := sampleDaily()

	err := Synthesize(context.Background(), fj, d, SynthInput{
		GlobalGoal:  "品質重視",
		OpenActions: []model.Action{{ID: 1, Title: "既存の提案", CreatedOn: "2026-08-20"}},
		Model:       "claude-sonnet-5",
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	if d.Narrative.Headline != "APIの不具合を修正した" {
		t.Errorf("Narrative.Headline = %q, want %q", d.Narrative.Headline, "APIの不具合を修正した")
	}
	if len(d.Narrative.Highlights) != 2 {
		t.Errorf("len(Narrative.Highlights) = %d, want 2", len(d.Narrative.Highlights))
	}

	if d.Retro.Headline == "" {
		t.Error("Retro.Headline が空")
	}
	if d.Retro.Verdict != "mixed" {
		t.Errorf("Retro.Verdict = %q, want %q", d.Retro.Verdict, "mixed")
	}
	if d.Retro.CostObservation == "" {
		t.Error("Retro.CostObservation が空")
	}
	if len(d.Retro.Proposals) != 1 {
		t.Errorf("len(Retro.Proposals) = %d, want 1", len(d.Retro.Proposals))
	}
	if len(d.Retro.Verifications) != 1 || d.Retro.Verifications[0].Status != "done" {
		t.Errorf("Retro.Verifications = %+v, want 1 件 status=done", d.Retro.Verifications)
	}
	if len(d.Retro.ProjectReviews) != 2 {
		t.Errorf("len(Retro.ProjectReviews) = %d, want 2", len(d.Retro.ProjectReviews))
	}

	if len(fj.calls) != 2 {
		t.Fatalf("judge.Evaluate 呼び出し回数 = %d, want 2 (日報・振り返りで別々に呼ぶ)", len(fj.calls))
	}
	if fj.calls[0].System == fj.calls[1].System {
		t.Error("日報と振り返りで同じ System プロンプトが使われている（別々の呼び出しであるべき）")
	}
}

// TestSynthesize_ProjectReviewsReflected は Retro.ProjectReviews が
// project_path をキーに Daily.ByProject[].Review に反映されることを確認する。
func TestSynthesize_ProjectReviewsReflected(t *testing.T) {
	fj := &fakeJudge{
		dailyResponse: validDailyJSON(),
		retroResponse: validRetroJSON(),
	}
	d := sampleDaily()

	if err := Synthesize(context.Background(), fj, d, SynthInput{Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	if len(d.ByProject) != 2 {
		t.Fatalf("len(d.ByProject) = %d, want 2", len(d.ByProject))
	}

	var projA, projB *ProjectStat
	for i := range d.ByProject {
		switch d.ByProject[i].ProjectPath {
		case "/repo/proj-a":
			projA = &d.ByProject[i]
		case "/repo/proj-b":
			projB = &d.ByProject[i]
		}
	}
	if projA == nil || projB == nil {
		t.Fatalf("ByProject に想定した project_path が無い: %+v", d.ByProject)
	}

	if projA.Review.Verdict != "worth_it" {
		t.Errorf("proj-a Review.Verdict = %q, want %q", projA.Review.Verdict, "worth_it")
	}
	if projA.Review.Summary == "" {
		t.Error("proj-a Review.Summary が空")
	}
	if projB.Review.Verdict != "mixed" {
		t.Errorf("proj-b Review.Verdict = %q, want %q", projB.Review.Verdict, "mixed")
	}
	if projB.Review.Improvement == "" {
		t.Error("proj-b Review.Improvement が空。レスポンスに含まれているので反映されるべき")
	}
}

// TestSynthesize_ProjectReviewsUnmatchedKeyLogsWarning は、AI が返した
// project_reviews のキーが既知の project_path と一致しない場合に、
// 反映をスキップしつつ警告ログを出すことを確認する。
func TestSynthesize_ProjectReviewsUnmatchedKeyLogsWarning(t *testing.T) {
	retro := json.RawMessage(`{
		"headline": "判断材料が乏しい",
		"verdict": "insufficient_data",
		"body": "",
		"cost_observation": "",
		"proposals": [],
		"verifications": [],
		"outliers": [],
		"project_reviews": {
			"/repo/does-not-exist": {
				"verdict": "insufficient_data",
				"summary": "存在しないプロジェクトへの言及",
				"improvement": ""
			}
		}
	}`)
	fj := &fakeJudge{
		dailyResponse: validDailyJSON(),
		retroResponse: retro,
	}
	d := sampleDaily()

	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(orig)

	if err := Synthesize(context.Background(), fj, d, SynthInput{Model: "claude-sonnet-5"}); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	for _, p := range d.ByProject {
		if p.Review.Verdict != "" {
			t.Errorf("project_path=%q の Review が書き換わっている（一致しないキーなので無視されるべき）: %+v", p.ProjectPath, p.Review)
		}
	}

	logged := buf.String()
	if !strings.Contains(logged, "/repo/does-not-exist") {
		t.Errorf("警告ログに一致しなかった project_path が含まれていない: %s", logged)
	}
}

func TestSynthesize_DailyFailsRetroSucceeds(t *testing.T) {
	fj := &fakeJudge{
		dailyErr:      fmt.Errorf("boom"),
		retroResponse: validRetroJSON(),
	}
	d := sampleDaily()

	err := Synthesize(context.Background(), fj, d, SynthInput{Model: "claude-sonnet-5"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want error (日報呼び出しが失敗しているため)")
	}

	// 日報は空のまま。
	if d.Narrative.Headline != "" {
		t.Errorf("Narrative.Headline = %q, want 空（失敗したので書き換わらない）", d.Narrative.Headline)
	}
	// しかし振り返りの結果は残っている。
	if d.Retro.CostObservation == "" {
		t.Error("Retro.CostObservation が空。片方失敗してももう片方の結果は残るべき")
	}
	if d.Retro.Headline == "" {
		t.Error("Retro.Headline が空。片方失敗してももう片方の結果は残るべき")
	}
}

func TestSynthesize_RetroFailsDailySucceeds(t *testing.T) {
	fj := &fakeJudge{
		dailyResponse: validDailyJSON(),
		retroErr:      fmt.Errorf("boom"),
	}
	d := sampleDaily()

	err := Synthesize(context.Background(), fj, d, SynthInput{Model: "claude-sonnet-5"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want error (振り返り呼び出しが失敗しているため)")
	}

	if d.Narrative.Headline == "" {
		t.Error("Narrative.Headline が空。日報の呼び出しは成功しているので結果が残るべき")
	}
	if d.Retro.CostObservation != "" {
		t.Errorf("Retro.CostObservation = %q, want 空（失敗したので書き換わらない）", d.Retro.CostObservation)
	}
	for _, p := range d.ByProject {
		if p.Review.Verdict != "" {
			t.Errorf("project_path=%q の Review が書き換わっている。振り返りが失敗したので反映されないべき", p.ProjectPath)
		}
	}
}

func TestSynthesize_MalformedJSON(t *testing.T) {
	fj := &fakeJudge{
		dailyResponse: json.RawMessage(`{not valid json`),
		retroResponse: validRetroJSON(),
	}
	d := sampleDaily()

	err := Synthesize(context.Background(), fj, d, SynthInput{Model: "claude-sonnet-5"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want error (壊れた JSON のため)")
	}
	if d.Narrative.Headline != "" {
		t.Errorf("Narrative.Headline = %q, want 空", d.Narrative.Headline)
	}
	// retro 側は壊れていないので反映される。
	if d.Retro.CostObservation == "" {
		t.Error("Retro.CostObservation が空。正常だった振り返りの結果は残るべき")
	}
}

func TestSynthesize_NoSessionsSkipsJudge(t *testing.T) {
	fj := &fakeJudge{}
	d := &Daily{Date: "2026-08-29"} // Sessions が空

	if err := Synthesize(context.Background(), fj, d, SynthInput{}); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(fj.calls) != 0 {
		t.Errorf("judge.Evaluate 呼び出し回数 = %d, want 0（セッションが無い日は呼ばない）", len(fj.calls))
	}
}

// TestSchemas_AreValidJSON は dailySchema / retroSchema が構文的に正しい JSON であることを確認する。
// `claude -p --json-schema` にそのまま渡す想定のスキーマなので、壊れた JSON では困る。
func TestSchemas_AreValidJSON(t *testing.T) {
	if !json.Valid(dailySchema) {
		t.Error("dailySchema が不正な JSON")
	}
	if !json.Valid(retroSchema) {
		t.Error("retroSchema が不正な JSON")
	}

	var retroParsed map[string]any
	if err := json.Unmarshal(retroSchema, &retroParsed); err != nil {
		t.Fatalf("retroSchema のパースに失敗: %v", err)
	}
	props, ok := retroParsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("retroSchema.properties が無い")
	}
	for _, key := range []string{"headline", "verdict", "project_reviews"} {
		if _, ok := props[key]; !ok {
			t.Errorf("retroSchema.properties に %q が無い", key)
		}
	}
	required, ok := retroParsed["required"].([]any)
	if !ok {
		t.Fatal("retroSchema.required が無い")
	}
	foundHeadline, foundVerdict, foundProjectReviews := false, false, false
	for _, r := range required {
		switch r {
		case "headline":
			foundHeadline = true
		case "verdict":
			foundVerdict = true
		case "project_reviews":
			foundProjectReviews = true
		}
	}
	if !foundHeadline || !foundVerdict || !foundProjectReviews {
		t.Errorf("retroSchema.required に headline/verdict/project_reviews が揃っていない: %v", required)
	}
}

func TestSynthesize_NilDaily(t *testing.T) {
	fj := &fakeJudge{}
	if err := Synthesize(context.Background(), fj, nil, SynthInput{}); err == nil {
		t.Error("Synthesize(nil daily) error = nil, want error")
	}
}
