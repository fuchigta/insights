package claudecli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ExtractJSON は Claude の応答テキストから JSON オブジェクトを寛容に取り出す。
// モデルは ```json フェンスで囲む、前後に短い説明文を付ける、といった振る舞いを
// することがあるため、「最初の { から、それに対応する（＝波括弧の対応が取れた）
// 最後の } まで」を文字列として抜き出す。この方式なら、コードフェンスの行
// （``` や ```json）は '{' を含まないため自然に読み飛ばされ、抜き出した後に
// フェンスを別途取り除く処理は不要になる。
//
// 波括弧の対応付けは JSON 文字列リテラル内の '{' '}' を誤ってカウントしない
// よう、ダブルクォート文字列とエスケープを認識しながら行う。
func ExtractJSON(s string) (json.RawMessage, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, errors.New("応答に JSON オブジェクトの開始 '{' が見つかりませんでした")
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if !json.Valid([]byte(candidate)) {
					return nil, fmt.Errorf("抽出した範囲が有効な JSON ではありません: %s", truncateForError(candidate))
				}
				return json.RawMessage(candidate), nil
			}
		}
	}

	return nil, errors.New("対応する閉じ括弧 '}' が見つからず、JSON として不完全でした")
}

func truncateForError(s string) string {
	const maxLen = 200
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "…"
}
