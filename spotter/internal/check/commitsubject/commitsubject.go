// Package commitsubject は Conventional Commits の subject 行を検証する検査
// （scripts/check-commit-subject.sh 相当）を実装する。
package commitsubject

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/config"
)

// Check は commit-subject 検査の 1 インスタンス。
type Check struct {
	pattern      *regexp.Regexp
	allowedTypes []string
}

// New は config.CheckConfig から Check を組み立てる。allowed_types は必須で、
// 空だと「常に不一致」という無意味な検査になってしまうため起動時に拒否する。
func New(cc config.CheckConfig) (*Check, error) {
	if len(cc.AllowedTypes) == 0 {
		return nil, fmt.Errorf("commitsubject: allowed_types が空です")
	}

	quoted := make([]string, len(cc.AllowedTypes))
	for i, t := range cc.AllowedTypes {
		if t == "" {
			return nil, fmt.Errorf("commitsubject: allowed_types に空文字は指定できません")
		}
		quoted[i] = regexp.QuoteMeta(t)
	}

	// type(scope)!: 説明 — scope は任意、! は破壊的変更、説明は 1 文字以上。
	pattern := fmt.Sprintf(`^(%s)(\([a-zA-Z0-9._/-]+\))?!?: .+`, strings.Join(quoted, "|"))
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("commitsubject: パターンのコンパイルに失敗しました: %w", err)
	}

	return &Check{pattern: re, allowedTypes: cc.AllowedTypes}, nil
}

// Granularity はコミットごとに 1 回ずつ見る。commit-subject の免除は既定で無効
// （config.ResolveExempt の defaultExemptEnable を参照）。
func (c *Check) Granularity() check.Granularity {
	return check.GranularityPerCommit
}

// Run は ctx.Message の 1 行目（subject）が Conventional Commits の形式に合うかを検証する。
func (c *Check) Run(ctx check.Context) ([]check.Violation, error) {
	subject := firstLine(ctx.Message)

	// git が自動生成するマージ・リバートのメッセージは対象外にする。
	if subject == "" || strings.HasPrefix(subject, "Merge ") || strings.HasPrefix(subject, "Revert ") {
		return nil, nil
	}

	if c.pattern.MatchString(subject) {
		return nil, nil
	}

	return []check.Violation{{Summary: fmt.Sprintf("不正なコミットメッセージです: %s", subject)}}, nil
}

func firstLine(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return strings.TrimRight(msg[:i], "\r")
	}
	return strings.TrimRight(msg, "\r")
}
