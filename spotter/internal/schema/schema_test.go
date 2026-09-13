package schema_test

import (
	"testing"

	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/schema"
)

func TestCompileNil(t *testing.T) {
	s, err := schema.Compile(nil)
	if err != nil {
		t.Fatalf("Compile(nil) error: %v", err)
	}
	if err := s.Validate(map[string]any{"任意のキー": "任意の値"}); err != nil {
		t.Errorf("検証しない schema のはずが Validate() がエラーを返した: %v", err)
	}
}

func TestSimpleValid(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		Simple: map[string]config.FieldSpec{
			"threshold": {Type: "integer", Required: true},
			"label":     {Type: "string"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"threshold": 10, "label": "x"}); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}

func TestSimpleMissingRequired(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		Simple: map[string]config.FieldSpec{
			"threshold": {Type: "integer", Required: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{}); err == nil {
		t.Fatal("必須フィールドが無いのに Validate() がエラーになりませんでした")
	}
}

func TestSimpleWrongType(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		Simple: map[string]config.FieldSpec{
			"threshold": {Type: "integer"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"threshold": "not-a-number"}); err == nil {
		t.Fatal("型が違うのに Validate() がエラーになりませんでした")
	}
}

func TestSimpleUnknownField(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		Simple: map[string]config.FieldSpec{
			"threshold": {Type: "integer"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"unknown": 1}); err == nil {
		t.Fatal("未知のフィールドがあるのに Validate() がエラーになりませんでした")
	}
}

func TestSimpleArray(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		Simple: map[string]config.FieldSpec{
			"paths": {Type: "array", Items: "string"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"paths": []any{"a", "b"}}); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
	if err := s.Validate(map[string]any{"paths": []any{"a", 1}}); err == nil {
		t.Fatal("要素の型が違うのに Validate() がエラーになりませんでした")
	}
}

func TestJSONSchemaValid(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		JSONSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "pattern": "^[a-z]+$"},
			},
			"required": []any{"pattern"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"pattern": "abc"}); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}

func TestJSONSchemaInvalid(t *testing.T) {
	s, err := schema.Compile(&config.SchemaConfig{
		JSONSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "pattern": "^[a-z]+$"},
			},
			"required": []any{"pattern"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if err := s.Validate(map[string]any{"pattern": "ABC"}); err == nil {
		t.Fatal("pattern に一致しないのに Validate() がエラーになりませんでした")
	}
	if err := s.Validate(map[string]any{}); err == nil {
		t.Fatal("required を満たさないのに Validate() がエラーになりませんでした")
	}
}
