package litellm

import (
	"strings"
	"testing"
)

const sample = `{
  "sample_spec": {"litellm_provider": "one of https://docs.litellm.ai/docs/providers", "mode": "one of chat", "input_cost_per_token": 0.0},
  "claude-a": {"litellm_provider": "anthropic", "mode": "chat",
    "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05,
    "cache_read_input_token_cost": 3e-07,
    "cache_creation_input_token_cost": 3.75e-06,
    "cache_creation_input_token_cost_above_1hr": 6e-06,
    "input_cost_per_token_above_200k_tokens": 6e-06},
  "claude-nocache1h": {"litellm_provider": "anthropic", "mode": "chat",
    "input_cost_per_token": 1e-06, "output_cost_per_token": 5e-06},
  "gpt-x": {"litellm_provider": "openai", "mode": "responses",
    "input_cost_per_token": 1.25e-06, "output_cost_per_token": 1e-05,
    "cache_read_input_token_cost": 1.25e-07,
    "cache_creation_input_token_cost": 1.5e-06},
  "gpt-embed": {"litellm_provider": "openai", "mode": "embedding", "input_cost_per_token": 1e-07, "output_cost_per_token": 0},
  "azure/gpt-x": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 1e-06},
  "claude-bedrock": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 1e-06},
  "other-model": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 1e-06},
  "gpt-cheap": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-07, "output_cost_per_token": 4e-07, "cache_read_input_token_cost": 1e-08}
}`

func TestConvert(t *testing.T) {
	got, err := Convert([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Rate{
		"claude-a":         {Input: 3, Output: 15, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.3},
		"claude-nocache1h": {Input: 1, Output: 5, CacheWrite5m: 1.25, CacheWrite1h: 2},
		// openai はキャッシュ書き込みを常に 0 にする（二重計上の回避）。
		"gpt-x":     {Input: 1.25, Output: 10, CacheRead: 0.125},
		"gpt-cheap": {Input: 0.1, Output: 0.4, CacheRead: 0.01},
	}
	if len(got) != len(want) {
		t.Fatalf("採用数 = %d, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
}

func TestConvert_InvalidJSON(t *testing.T) {
	if _, err := Convert([]byte("[")); err == nil {
		t.Fatal("不正な JSON でエラーにならない")
	}
}

func TestMerge(t *testing.T) {
	synced := map[string]Rate{"a": {Input: 1}, "b": {Input: 2}}
	manual := Manual{Models: map[string]Rate{"b": {Input: 9}, "c": {}}}
	existing := map[string]Rate{"a": {Input: 100}, "old": {Input: 5}}

	models, missing := Merge(synced, manual, existing)
	if models["a"].Input != 1 {
		t.Errorf("a は同期結果が優先されるはず: %+v", models["a"])
	}
	if models["b"].Input != 9 {
		t.Errorf("b は manual が優先されるはず: %+v", models["b"])
	}
	if _, ok := models["c"]; !ok {
		t.Error("manual のみのモデルが残っていない")
	}
	if models["old"].Input != 5 {
		t.Errorf("old は既存値のまま残るはず: %+v", models["old"])
	}
	if len(missing) != 1 || missing[0] != "old" {
		t.Errorf("missing = %v, want [old]", missing)
	}
}

func TestRender(t *testing.T) {
	out, err := Render(Table{
		Note:        "n",
		Version:     "2026-01-01",
		NonBillable: []string{"<synthetic>"},
		Models:      map[string]Rate{"b": {Input: 1}, "a": {Input: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasSuffix(s, "}\n") {
		t.Error("末尾改行が無い")
	}
	if !strings.Contains(s, `"<synthetic>"`) {
		t.Error("HTML エスケープされている")
	}
	if strings.Index(s, `"a"`) > strings.Index(s, `"b"`) {
		t.Error("モデルがキー昇順でない")
	}
	if strings.Index(s, `"_note"`) > strings.Index(s, `"version"`) || strings.Index(s, `"version"`) > strings.Index(s, `"non_billable"`) {
		t.Error("トップレベルの順序が _note / version / non_billable / models でない")
	}
}
