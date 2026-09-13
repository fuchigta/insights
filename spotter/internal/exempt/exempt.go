// Package exempt はコミットメッセージ本文のトレーラによる検査の免除判定を扱う。
//
// 環境変数ではなくコミットメッセージにしているのは、ローカルで通した判断が CI でも
// そのまま通る必要があるため（docs/hooks-extraction.md 参照）。
package exempt

import (
	"fmt"
	"regexp"
	"strings"
)

// Config は 1 つの検査インスタンスの免除設定。
type Config struct {
	// Enable が false なら、メッセージの内容に関わらず免除されない。
	Enable bool
	// Trailer はトレーラ名（例: "Doc-Sync"）。
	Trailer string
}

// Check はメッセージ本文に Config.Trailer のスキップトレーラがあるかを調べる。
// 既存のシェル実装と異なり、理由が空のスキップは免除として認めない
// （CLAUDE.md が「理由を添えて書く」運用を前提にしているため）。
func Check(cfg Config, message string) (skip bool, reason string, err error) {
	if !cfg.Enable {
		return false, "", nil
	}
	if cfg.Trailer == "" {
		return false, "", fmt.Errorf("exempt: trailer が空です")
	}

	re, err := regexp.Compile(`(?i)^` + regexp.QuoteMeta(cfg.Trailer) + `:\s*skip\s+(\S.*)$`)
	if err != nil {
		return false, "", fmt.Errorf("exempt: トレーラ名 %q の正規表現コンパイルに失敗しました: %w", cfg.Trailer, err)
	}

	for _, line := range strings.Split(message, "\n") {
		if m := re.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			return true, strings.TrimSpace(m[1]), nil
		}
	}
	return false, "", nil
}
