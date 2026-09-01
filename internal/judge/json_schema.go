package judge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RequiredFields は JSON Schema（トップレベルの "required" 配列）から必須フィールド名を
// 取り出す。schema が空、または "required" を持たない場合は nil を返す（検証をスキップする）。
func RequiredFields(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil
	}
	return s.Required
}

// ValidateRequired は data（トップレベルが JSON オブジェクトであること）が required に
// 挙げられたキーをすべて持ち、かつそれらが null でないことを検証する。
// ネストしたオブジェクトの中まで検証する完全な JSON Schema バリデータではなく、
// 「スキーマの必須フィールドを満たさない場合に 1 回だけ再試行する」ための
// 実用十分なチェックにとどめている（標準ライブラリのみで完結させるため）。
func ValidateRequired(data json.RawMessage, required []string) error {
	if len(required) == 0 {
		return nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("JSON オブジェクトとして解釈できませんでした: %w", err)
	}

	var missing []string
	for _, f := range required {
		v, ok := obj[f]
		if !ok || string(v) == "null" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("必須フィールドが不足しています: %s", strings.Join(missing, ", "))
	}
	return nil
}
