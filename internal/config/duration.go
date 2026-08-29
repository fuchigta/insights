package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration は yaml 上で "180s" のような文字列として表現される time.Duration。
// 標準の time.Duration は yaml.v3 の既定デコードでは数値（ナノ秒）として
// 扱われてしまうため、文字列としてパース・出力するための薄いラッパー。
type Duration struct {
	time.Duration
}

// UnmarshalYAML は "180s" のような文字列を time.Duration としてパースする。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration は文字列である必要があります: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration の解析に失敗しました (%q): %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML は time.Duration.String() の形式（例 "3m0s"）で出力する。
func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}
