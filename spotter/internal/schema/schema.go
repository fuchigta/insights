// Package schema は command 型検査（外部コマンド検査）に渡せる Options を検証する。
//
// docs/hooks-extraction.md の「schema は 2 つの書き方をサポートする」の通り、
// `simple`（フィールド名 → 型・必須かどうかの簡易記法）と `json-schema`（フル
// JSON Schema）のどちらか一方を扱う。json-schema 側の検証には
// santhosh-tekuri/jsonschema（依存ゼロ、Draft 2020-12 対応）を使う。
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fuchigta/spotter/internal/config"
)

// resourceURL は json-schema をコンパイルするための仮想的なリソース URL。
// 外部から読み込むことは無いので固定値でよい。
const resourceURL = "spotter://schema"

// Schema はコンパイル済みで検証に使える状態の schema。
// ゼロ値は「検証しない」（常に nil を返す）schema として振る舞う。
type Schema struct {
	simple map[string]config.FieldSpec
	js     *jsonschema.Schema
}

// Compile は cfg をコンパイルする。cfg が nil、または simple/json-schema の
// どちらも指定されていない場合は「検証しない」Schema を返す。
// simple と json-schema が両方指定されている場合のエラーは config.Load
// （validateTypeConfig）が既に検出しているので、ここでは想定していない。
func Compile(cfg *config.SchemaConfig) (*Schema, error) {
	if cfg == nil {
		return &Schema{}, nil
	}
	if len(cfg.Simple) > 0 {
		return &Schema{simple: cfg.Simple}, nil
	}
	if len(cfg.JSONSchema) > 0 {
		compiled, err := compileJSONSchema(cfg.JSONSchema)
		if err != nil {
			return nil, err
		}
		return &Schema{js: compiled}, nil
	}
	return &Schema{}, nil
}

func compileJSONSchema(raw map[string]any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("schema: json-schema のエンコードに失敗しました: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("schema: json-schema の解析に失敗しました: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(resourceURL, doc); err != nil {
		return nil, fmt.Errorf("schema: json-schema の登録に失敗しました: %w", err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("schema: json-schema のコンパイルに失敗しました: %w", err)
	}
	return compiled, nil
}

// Validate は options を検証する。nil の *Schema（Compile を経ていない）や、
// 「検証しない」Schema に対しては常に nil を返す。
func (s *Schema) Validate(options map[string]any) error {
	if s == nil {
		return nil
	}
	if s.js != nil {
		return s.validateJSONSchema(options)
	}
	if s.simple != nil {
		return s.validateSimple(options)
	}
	return nil
}

func (s *Schema) validateJSONSchema(options map[string]any) error {
	data, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("schema: options のエンコードに失敗しました: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("schema: options の解析に失敗しました: %w", err)
	}
	if err := s.js.Validate(doc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return nil
}

func (s *Schema) validateSimple(options map[string]any) error {
	for name, spec := range s.simple {
		v, ok := options[name]
		if !ok {
			if spec.Required {
				return fmt.Errorf("schema: %s は必須です", name)
			}
			continue
		}
		if err := checkFieldType(name, spec, v); err != nil {
			return err
		}
	}
	for name := range options {
		if _, ok := s.simple[name]; !ok {
			return fmt.Errorf("schema: 未知のオプション %s です", name)
		}
	}
	return nil
}

func checkFieldType(name string, spec config.FieldSpec, v any) error {
	switch spec.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("schema: %s は string である必要があります", name)
		}
	case "integer":
		if !isInteger(v) {
			return fmt.Errorf("schema: %s は integer である必要があります", name)
		}
	case "number":
		if !isNumber(v) {
			return fmt.Errorf("schema: %s は number である必要があります", name)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("schema: %s は boolean である必要があります", name)
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("schema: %s は array である必要があります", name)
		}
		for i, item := range arr {
			if err := checkFieldType(fmt.Sprintf("%s[%d]", name, i), config.FieldSpec{Type: spec.Items}, item); err != nil {
				return err
			}
		}
	case "":
		return fmt.Errorf("schema: %s の type が指定されていません", name)
	default:
		return fmt.Errorf("schema: %s の type %q は未対応です", name, spec.Type)
	}
	return nil
}

func isInteger(v any) bool {
	switch n := v.(type) {
	case int:
		return true
	case int64:
		return true
	case float64:
		return n == float64(int64(n))
	default:
		return false
	}
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, float64:
		return true
	default:
		return false
	}
}
