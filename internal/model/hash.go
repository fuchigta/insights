package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ContentHash は正規化後の全メッセージ（Role/Model/ToolName/Text）から SHA-256 を計算する。
// フィールド間・メッセージ間を制御文字で区切り、値の中身が偶然一致しても衝突しないようにする。
//
// この値は評価キャッシュの冪等キー（Session.ContentHash）になるため、ログソースが
// 違っても同じ規則で計算する必要がある。ソース側の実装ごとに書くとずれるので、
// 正規化モデルと同じ場所に置いている。
func ContentHash(messages []Message) string {
	h := sha256.New()
	for _, m := range messages {
		fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1f%s\x1e", m.Role, m.Model, m.ToolName, m.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}
