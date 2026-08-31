package render_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/render"
)

// FrontMatter / MiniFrontMatter の yaml タグは「サイドカー YAML だけから rollup.Point 等を
// 再構成できる」という契約そのもので、format.go のコメントは「既存の yaml タグの変更・削除は
// 行わないこと（行うと、書き出し済みの過去サイドカー YAML からの再集計が壊れる）。フィールドの
// 追加は問題ない」と宣言している。しかしこの宣言はコメントでしか守られておらず、リファクタで
// タグが変わっても機械的には誰も気付けない。
//
// ここでは reflect で各構造体の現在の yaml タグ集合を集め、golden（このテスト内の定数リスト）
// が「現在のタグ集合の部分集合であること」を確認する。golden にあるタグが 1 つでも消えていたら
// （改名も「旧タグの削除」として現れる）落ちる。逆に新しいタグの追加は golden の部分集合関係を
// 壊さないので通る。
//
// FrontMatter が参照するネスト構造体（ModelCost / FacetCount / ModelUsageFM / ProjectStatFM）も、
// FrontMatter コメントが明示的に名指しして「サイドカー YAML だけから復元できる」契約に含めている
// （モデル別トークン量・プロジェクト別集計）ため、同じ扱いで対象に含める。

// yamlTagsOf は v（構造体値）の直下のフィールドが持つ yaml タグ名の集合を返す。
// ",omitempty" 等のオプションは切り落とし、"-"（出力しない）指定は無視する。
func yamlTagsOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	rt := reflect.TypeOf(v)
	if rt.Kind() != reflect.Struct {
		t.Fatalf("yamlTagsOf: 構造体ではありません: %v", rt)
	}
	tags := make(map[string]bool)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		raw, ok := f.Tag.Lookup("yaml")
		if !ok {
			t.Fatalf("yamlTagsOf: フィールド %s に yaml タグがありません（契約対象外のフィールドが紛れ込んでいます）", f.Name)
		}
		name := strings.Split(raw, ",")[0]
		if name == "-" {
			continue
		}
		tags[name] = true
	}
	return tags
}

// assertGoldenTagsSubset は golden の全タグが got に含まれることを確認する。
// got 側に golden にない余分なタグ（＝追加）があっても失敗させない。
func assertGoldenTagsSubset(t *testing.T, structName string, got map[string]bool, golden []string) {
	t.Helper()
	var missing []string
	for _, want := range golden {
		if !got[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf(
			"%s の yaml タグが golden から消えています（改名 or 削除）: %v\n"+
				"現在のタグ: %v\n"+
				"これは書き出し済みの過去サイドカー YAML からの再集計を壊す可能性があります。"+
				"どうしても変更が必要なら、format.go のコメントとこのテストの golden を"+
				"合わせて更新し、後方互換の扱いを検討してください。",
			structName, missing, sortedKeys(got),
		)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestFrontMatter_YAMLTagsContract は FrontMatter（サイドカー YAML 本体）の yaml タグを
// golden と突き合わせる。
func TestFrontMatter_YAMLTagsContract(t *testing.T) {
	golden := []string{
		"date",
		"generated_at",
		"sessions",
		"duration_minutes",
		"cost_usd",
		"cost_by_model",
		"outcome",
		"model_fit",
		"ownership",
		"achieved_ratio",
		"interactive_sessions",
		"automated_sessions",
		"sidechain_sessions",
		"unpriced_events",
		"artifact_value",
		"intervention_cost",
		"learning_value",
		"goal_category",
		"confidence",
		"rework_occurred",
		"by_model",
		"by_project",
		"prompt_version",
		"unknown_models",
		"unevaluated_sessions",
		"missing_transcripts",
		"judge_cost_usd",
		"judge_session_ids",
	}
	assertGoldenTagsSubset(t, "FrontMatter", yamlTagsOf(t, render.FrontMatter{}), golden)
}

// TestMiniFrontMatter_YAMLTagsContract は MiniFrontMatter（Markdown 本文埋め込み分）の
// yaml タグを golden と突き合わせる。
func TestMiniFrontMatter_YAMLTagsContract(t *testing.T) {
	golden := []string{
		"date",
		"sessions",
		"duration_minutes",
		"cost_usd",
		"achieved_ratio",
		"prompt_version",
		"meta",
	}
	assertGoldenTagsSubset(t, "MiniFrontMatter", yamlTagsOf(t, render.MiniFrontMatter{}), golden)
}

// TestFrontMatterNested_YAMLTagsContract は FrontMatter が参照するネスト構造体の yaml タグを
// golden と突き合わせる。これらも FrontMatter のコメントが名指しする契約（モデル別集計・
// プロジェクト別集計を含めた再構成）の一部なので、単独の型として検証する。
func TestFrontMatterNested_YAMLTagsContract(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		golden []string
	}{
		{
			name:   "ModelCost",
			value:  render.ModelCost{},
			golden: []string{"model", "cost_usd"},
		},
		{
			name:   "FacetCount",
			value:  render.FacetCount{},
			golden: []string{"key", "count"},
		},
		{
			name:  "ModelUsageFM",
			value: render.ModelUsageFM{},
			golden: []string{
				"model",
				"sessions",
				"responses",
				"input_tokens",
				"output_tokens",
				"cache_read_tokens",
				"cache_write_tokens",
				"cost_usd",
				"priced",
			},
		},
		{
			name:  "ProjectStatFM",
			value: render.ProjectStatFM{},
			golden: []string{
				"project_path",
				"project_label",
				"sessions",
				"duration_minutes",
				"cost_usd",
				"goal",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGoldenTagsSubset(t, tc.name, yamlTagsOf(t, tc.value), tc.golden)
		})
	}
}
