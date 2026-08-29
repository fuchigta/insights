package render_test

// labels_test.go は internal/render の日本語ラベル変換（labels.go）を検証する。
//
// labels.go の変換関数（outcomeJP など）はパッケージ非公開のため直接は呼べない。
// ここでは公開 API（RenderRetro）の出力を通して、既知の値が日本語に変換されること、
// 未知の値が来ても落ちずに元の文字列がそのまま出ることを確認する。

import (
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/rollup"
)

// TestFacetLabelsJapanese は Facets の既知の列挙値が振り返り Markdown 上で
// 日本語ラベルに変換されて出力されることを確認する。
func TestFacetLabelsJapanese(t *testing.T) {
	d := &rollup.Daily{
		Date: "2026-08-29",
		Facets: rollup.Facets{
			Outcome:          map[string]int{"achieved": 1},
			ArtifactValue:    map[string]int{"durable": 1},
			InterventionCost: map[string]int{"low": 1},
			ModelFit:         map[string]int{"appropriate": 1},
			Ownership:        map[string]int{"understood": 1},
			LearningValue:    map[string]int{"high": 1},
			GoalCategory:     map[string]int{"feature": 1},
			Confidence:       map[string]int{"low": 1},
		},
	}
	md, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}
	s := string(md)

	wantJP := []string{"達成", "資産として残る", "低い（ほぼ任せられた）", "適切", "理解して検収", "大きな学び", "機能追加"}
	for _, w := range wantJP {
		if !strings.Contains(s, w) {
			t.Errorf("日本語ラベル %q が出力に見つかりません:\n%s", w, s)
		}
	}

	// 元の英語列挙値がそのまま（日本語化されずに）残っていないことを確認する
	// （facet の分布セクションでの話。AI 生成の自由記述本文は対象外）。
	forbidden := []string{"achieved:", "durable:", "appropriate:", "understood:"}
	for _, f := range forbidden {
		if strings.Contains(s, f) {
			t.Errorf("英語の列挙値 %q がそのまま出力されています:\n%s", f, s)
		}
	}
}

// TestVerdictLabelsJapanese は Retro.Verdict / ProjectReview.Verdict が
// 日本語に変換されることを確認する。
func TestVerdictLabelsJapanese(t *testing.T) {
	d := &rollup.Daily{
		Date: "2026-08-29",
		Retro: rollup.Retro{
			Headline: "テスト",
			Verdict:  "worth_it",
		},
		ByProject: []rollup.ProjectStat{
			{
				ProjectPath:   "p1",
				ProjectLabel:  "プロジェクト1",
				AchievedRatio: -1,
				Review:        rollup.ProjectReview{Verdict: "not_worth_it"},
			},
		},
	}
	md, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "コストに見合った") {
		t.Errorf("Retro.Verdict の日本語ラベルが見つかりません:\n%s", s)
	}
	if !strings.Contains(s, "コストに見合わなかった") {
		t.Errorf("ProjectReview.Verdict の日本語ラベルが見つかりません:\n%s", s)
	}
	if strings.Contains(s, "worth_it") || strings.Contains(s, "not_worth_it") {
		t.Errorf("Verdict の生の英語値が出力に残っています:\n%s", s)
	}
}

// TestActionStatusLabelJapanese は model.ActionStatus が日本語に変換されることを確認する。
func TestActionStatusLabelJapanese(t *testing.T) {
	d := &rollup.Daily{
		Date: "2026-08-29",
		Retro: rollup.Retro{
			Verifications: []rollup.ActionVerdict{
				{ActionID: 1, Title: "提案A", Status: string(model.ActionDropped), Verdict: "根拠"},
			},
		},
	}
	md, err := render.RenderRetro(d)
	if err != nil {
		t.Fatalf("RenderRetro が失敗しました: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "見送り") {
		t.Errorf("ActionStatus の日本語ラベルが見つかりません:\n%s", s)
	}
	if strings.Contains(s, "dropped") {
		t.Errorf("ActionStatus の生の英語値が出力に残っています:\n%s", s)
	}
}

// TestUnknownEnumValuesDoNotPanic は変換表に無い未知の列挙値が来ても
// RenderDaily / RenderRetro が落ちず、元の文字列がそのまま出力されることを確認する。
func TestUnknownEnumValuesDoNotPanic(t *testing.T) {
	unknownOutcome := "totally-unknown-outcome-xyz"
	d := &rollup.Daily{
		Date: "2026-08-29",
		Facets: rollup.Facets{
			Outcome: map[string]int{unknownOutcome: 1},
		},
		Retro: rollup.Retro{
			Verdict: "totally-unknown-verdict",
		},
		ByProject: []rollup.ProjectStat{
			{
				ProjectPath:   "p1",
				ProjectLabel:  "未知プロジェクト",
				AchievedRatio: -1,
				Review:        rollup.ProjectReview{Verdict: "totally-unknown-verdict-2"},
				Highlights: []rollup.SessionCard{
					{
						SessionID: "s1",
						Eval:      &model.Eval{Outcome: unknownOutcome},
					},
				},
			},
		},
	}

	var mdRes, retroRes []byte
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("未知の列挙値でパニックしました: %v", r)
			}
		}()
		mdRes, err = render.RenderDaily(d)
		if err != nil {
			t.Fatalf("RenderDaily が失敗しました: %v", err)
		}
		retroRes, err = render.RenderRetro(d)
		if err != nil {
			t.Fatalf("RenderRetro が失敗しました: %v", err)
		}
	}()

	if !strings.Contains(string(retroRes), unknownOutcome) {
		t.Errorf("未知の値 %q が出力から失われています（フォールバックで元の文字列を残すべき）", unknownOutcome)
	}
	_ = mdRes
}

// TestFrontMatterMinimal は Markdown 本文のフロントマターが最小限のフィールドに
// 留まっていること（旧仕様のような巨大な全量データを含まないこと）を確認する。
func TestFrontMatterMinimal(t *testing.T) {
	d := &rollup.Daily{Date: "2026-08-29"}
	md, err := render.RenderDaily(d)
	if err != nil {
		t.Fatalf("RenderDaily が失敗しました: %v", err)
	}

	fm, err := render.ParseFrontMatter(md)
	if err != nil {
		t.Fatalf("フロントマターのパースに失敗しました: %v", err)
	}
	if fm.Meta == "" {
		t.Errorf("フロントマターにサイドカーへの相対パスがありません")
	}

	// フロントマター部分だけを取り出して、by_model / by_project のような
	// 完全な構造化データのキーが含まれていないことを確認する。
	raw := string(md)
	end := strings.Index(raw, "\n---\n")
	if end < 0 {
		t.Fatalf("フロントマターの終端が見つかりません")
	}
	fmBlock := raw[:end]

	forbidden := []string{"by_model", "by_project", "cost_by_model", "judge_session_ids"}
	for _, f := range forbidden {
		if strings.Contains(fmBlock, f) {
			t.Errorf("最小フロントマターに完全な構造化データのキー %q が含まれています:\n%s", f, fmBlock)
		}
	}
}
