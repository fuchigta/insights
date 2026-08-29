package claudecli

import (
	"encoding/json"
	"testing"
)

func TestRequiredFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["a","b"],"properties":{}}`)
	got := requiredFields(schema)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("requiredFields() = %v, want [a b]", got)
	}

	if got := requiredFields(nil); got != nil {
		t.Errorf("requiredFields(nil) = %v, want nil", got)
	}
	if got := requiredFields(json.RawMessage(`{}`)); got != nil {
		t.Errorf("requiredFields({}) = %v, want nil", got)
	}
}

func TestValidateRequired(t *testing.T) {
	required := []string{"a", "b"}

	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"すべて揃っている", `{"a":1,"b":2,"c":3}`, false},
		{"1つ欠けている", `{"a":1}`, true},
		{"nullは欠けている扱い", `{"a":1,"b":null}`, true},
		{"オブジェクトでない", `[1,2,3]`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequired(json.RawMessage(tt.data), required)
			if tt.wantErr && err == nil {
				t.Fatalf("validateRequired(%s) error = nil, wantErr", tt.data)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateRequired(%s) unexpected error = %v", tt.data, err)
			}
		})
	}

	// required が空なら常に nil。
	if err := validateRequired(json.RawMessage(`{}`), nil); err != nil {
		t.Errorf("validateRequired with no required fields should be nil, got %v", err)
	}
}
