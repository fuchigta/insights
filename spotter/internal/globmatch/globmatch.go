// Package globmatch は POSIX シェルの case パターンと同じ意味論の glob マッチングを提供する。
//
// Go 標準の path.Match や filepath.Match は "*" が "/" に一致しない。移行元のシェルスクリプトは
// `case "$f" in $pat)` で判定しており、シェルの "*" は "/" にも一致するため、対応表のパターン
// （例: internal/source/*/*.go）が意図通りに発火しなくなる。この差を吸収するために自前で
// 実装する。
package globmatch

import (
	"fmt"
	"regexp"
	"strings"
)

// Match はシェルの case パターンと同じ意味論で name が pattern に一致するかを返す。
// "*" は "/" を含む任意の文字列に、"?" は任意の 1 文字に一致する。"[...]" は文字クラスとして
// 扱い、シェル同様に先頭の "!" を否定として扱う（正規表現の "^" に読み替える）。
//
// 同じパターンを繰り返し使う場合は Compile で事前にコンパイルしたものを使い回すこと。
func Match(pattern, name string) (bool, error) {
	re, err := Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(name), nil
}

// Compile は pattern を、Match と同じ意味論の *regexp.Regexp に変換する。
func Compile(pattern string) (*regexp.Regexp, error) {
	re, err := compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("globmatch: パターン %q のコンパイルに失敗しました: %w", pattern, err)
	}
	return re, nil
}

func compile(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")

	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i + 1
			neg := false
			if j < len(runes) && (runes[j] == '!' || runes[j] == '^') {
				neg = true
				j++
			}
			start := j
			for j < len(runes) && runes[j] != ']' {
				j++
			}
			if j >= len(runes) {
				// 対応する "]" が無い場合はリテラルの "[" として扱う。
				b.WriteString(`\[`)
				continue
			}
			b.WriteString("[")
			if neg {
				b.WriteString("^")
			}
			// レンジ指定（[a-z] など）を活かすため、クラス内は "\" だけをエスケープする。
			b.WriteString(strings.ReplaceAll(string(runes[start:j]), `\`, `\\`))
			b.WriteString("]")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")

	return regexp.Compile(b.String())
}
