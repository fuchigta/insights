package prompts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/judge/prompts"
	"github.com/fuchigta/insights/internal/model"
)

// TestSessionEvalSchemaMatchesEvalStruct は model.Eval と埋め込みスキーマの
// ずれを検出する。
//
// この 2 つは手で同期させており、ずれても実行時エラーにならない。スキーマは
// additionalProperties: false なので、構造体にだけあるフィールドはモデルが
// 出力できず、値が黙って欠落する。逆にスキーマにだけあるフィールドは
// デコード時に捨てられる。どちらも気づけないため、テストで固定する。
func TestSessionEvalSchemaMatchesEvalStruct(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(prompts.SessionEvalSchema(), &schema); err != nil {
		t.Fatalf("スキーマの JSON デコードに失敗しました: %v", err)
	}
	compare(t, "Eval", reflect.TypeOf(model.Eval{}), schema)
}

// compare は Go の構造体とスキーマのオブジェクトを再帰的に突き合わせる。
func compare(t *testing.T, path string, typ reflect.Type, schema map[string]any) {
	t.Helper()

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Errorf("%s: スキーマに properties がありません", path)
		return
	}

	// 構造体側の json タグを集める。
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		fields[tag] = f.Type
	}

	for name := range fields {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: 構造体にあるが スキーマに無い: %q（スキーマは additionalProperties:false なのでモデルが出力できません）", path, name)
		}
	}
	for name := range props {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s: スキーマにあるが 構造体に無い: %q（デコード時に捨てられます）", path, name)
		}
	}

	// required は全プロパティを列挙していること。省略されたフィールドは
	// 検証エラーにならず黙って欠落するため（記事および実装の確認による）。
	required := map[string]bool{}
	if raw, ok := schema["required"].([]any); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	for name := range props {
		if !required[name] {
			t.Errorf("%s: %q が required に含まれていません（欠落してもエラーにならず気づけません）", path, name)
		}
	}

	if v, ok := schema["additionalProperties"]; !ok || v != false {
		t.Errorf("%s: additionalProperties: false が指定されていません", path)
	}

	// 入れ子のオブジェクトを再帰的に確認する。
	for name, sub := range props {
		subSchema, ok := sub.(map[string]any)
		if !ok || subSchema["type"] != "object" {
			continue
		}
		ft, ok := fields[name]
		if !ok {
			continue
		}
		if ft.Kind() != reflect.Struct {
			t.Errorf("%s.%s: スキーマは object だが構造体は %s です", path, name, ft.Kind())
			continue
		}
		compare(t, path+"."+name, ft, subSchema)
	}
}

// promptFingerprint は評価プロンプトとスキーマの内容から求めた指紋。
//
// 評価結果は (session_id, prompt_version, content_hash) をキーにキャッシュされる。
// プロンプトやスキーマを変えたのに PromptVersion を据え置くと、古い評価が
// 再利用され続け「変更が効いていない」ことに気づけない。内容が変わったら
// このテストが落ちるので、PromptVersion を上げてから下の値を更新すること。
const promptFingerprint = "920542fe073f02a8767c698ecda40141d52e802f793969fad58d64bb19b3d197"

func TestPromptVersionIsBumpedWhenContentChanges(t *testing.T) {
	h := sha256.New()
	h.Write([]byte(prompts.SessionEvalPrompt()))
	h.Write(prompts.SessionEvalSchema())
	got := hex.EncodeToString(h.Sum(nil))

	if promptFingerprint == "" {
		t.Fatalf("promptFingerprint が未設定です。次の値を設定してください: %s (PromptVersion=%s)", got, prompts.PromptVersion)
	}
	if got != promptFingerprint {
		t.Errorf(`評価プロンプトまたはスキーマが変更されています。
  PromptVersion を上げてから promptFingerprint を更新してください。
  現在の PromptVersion: %s
  新しい指紋:           %s`, prompts.PromptVersion, got)
	}
}
