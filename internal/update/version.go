package update

import (
	"strconv"
	"strings"
)

// semver はリリースタグ（v1.2.3 / v1.2.3-rc.1）を比較可能な形に分解したもの。
//
// 外部の semver ライブラリを入れていないのは、比較するのが自分のリリースタグだけで、
// 形が release.yml（タグ push）に閉じているため。ビルドメタデータ（+build）や
// プレリリースの細かい優先順位まで実装する必要がない。
type semver struct {
	major int
	minor int
	patch int
	// pre はハイフン以降のプレリリース識別子（"rc.1" など）。無ければ空。
	pre string
}

// parseVersion は "v1.2.3" 形式のタグを分解する。解釈できなければ ok=false。
// "dev" や "(devel)" のような開発ビルドの値はここで弾かれる。
func parseVersion(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return semver{}, false
	}

	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	// ビルドメタデータは比較に影響しない（semver 仕様）ので捨てる。
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, true
}

// compareCore は major/minor/patch だけを比べる。a<b なら -1、a==b なら 0、a>b なら 1。
func (a semver) compareCore(b semver) int {
	switch {
	case a.major != b.major:
		return sign(a.major - b.major)
	case a.minor != b.minor:
		return sign(a.minor - b.minor)
	case a.patch != b.patch:
		return sign(a.patch - b.patch)
	default:
		return 0
	}
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}

// IsNewer は latest が current より新しく、かつ更新先として提示してよいかを返す。
//
// プレリリース（v1.3.0-rc.1）は、たとえ数字が大きくても更新先にしない。
// 安定版だけを使っている利用者に、試験版を勧めてしまわないため。
// 逆に current がプレリリースで latest が同じ番号の正式版なら「新しい」とみなす
// （semver の順序どおり。プレリリースは同じ番号の正式版より前）。
func IsNewer(latest, current string) bool {
	l, ok := parseVersion(latest)
	if !ok || l.pre != "" {
		return false
	}
	c, ok := parseVersion(current)
	if !ok {
		// current が "dev" などで比較できないときは更新を勧めない。
		// 開発ビルドを壊さないことを優先する。
		return false
	}

	if cmp := l.compareCore(c); cmp != 0 {
		return cmp > 0
	}
	// core が同じなら、current 側がプレリリースのときだけ latest が新しい。
	return c.pre != ""
}
