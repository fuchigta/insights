// Package version は spotter 自身のビルドバージョンと、設定の required_version を
// 比較する。
//
// docs/hooks-extraction.md §5 の通り、検査が増えたのに手元のバイナリが古いままだと
// 「手元で通ったものは CI でも通る」という前提が崩れる。required_version はこれを
// 防ぐための下限バージョン指定で、満たさないバイナリでは検査自体を実行させない。
package version

import (
	"fmt"
	"regexp"
	"strconv"
)

// Dev は -ldflags でバージョンが埋め込まれていない（go install や go run で
// ビルドされた）ことを示す既定値。
const Dev = "dev"

var pattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// parse は "v1.2.3"（v は省略可、末尾に -rc1 等が付いていても無視する）を
// [major, minor, patch] に分解する。
func parse(s string) ([3]int, error) {
	m := pattern.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, fmt.Errorf("version: %q はバージョン文字列として解釈できません", s)
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, fmt.Errorf("version: %q の解析に失敗しました: %w", s, err)
		}
		v[i] = n
	}
	return v, nil
}

// Compare は a と b を比較する。a < b なら負、a == b なら 0、a > b なら正を返す。
func Compare(a, b string) (int, error) {
	va, err := parse(a)
	if err != nil {
		return 0, err
	}
	vb, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if va[i] != vb[i] {
			return va[i] - vb[i], nil
		}
	}
	return 0, nil
}

// Satisfies は current が required 以上のバージョンかどうかを返す。
//
// current が正式なバージョン文字列として解釈できない場合（go install / go run で
// ビルドされ Dev のままの場合など）は、ソースからビルドされたものであり判定の
// しようが無いため、判定不能として true（満たしているとみなす）を返す。
// required 自体が不正な形式の場合は、設定の誤りとしてエラーを返す。
func Satisfies(current, required string) (bool, error) {
	if required == "" {
		return true, nil
	}
	if _, err := parse(required); err != nil {
		return false, fmt.Errorf("required_version: %w", err)
	}
	cmp, err := Compare(current, required)
	if err != nil {
		// current 側が解釈できないのは判定不能。エラーにはしない。
		return true, nil
	}
	return cmp >= 0, nil
}
