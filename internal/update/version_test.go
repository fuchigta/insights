package update

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		want semver
	}{
		{in: "v1.2.3", ok: true, want: semver{major: 1, minor: 2, patch: 3}},
		{in: "1.2.3", ok: true, want: semver{major: 1, minor: 2, patch: 3}},
		{in: "v0.0.1", ok: true, want: semver{major: 0, minor: 0, patch: 1}},
		{in: "v1.2.3-rc.1", ok: true, want: semver{major: 1, minor: 2, patch: 3, pre: "rc.1"}},
		{in: "v1.2.3+build.5", ok: true, want: semver{major: 1, minor: 2, patch: 3}},
		{in: "dev"},
		{in: "(devel)"},
		{in: ""},
		{in: "v1.2"},
		{in: "v1.2.3.4"},
		{in: "v1.2.x"},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in)
		if ok != tt.ok {
			t.Errorf("parseVersion(%q) の ok = %v, 期待 %v", tt.in, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("parseVersion(%q) = %+v, 期待 %+v", tt.in, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{name: "patch が上がっている", latest: "v1.2.4", current: "v1.2.3", want: true},
		{name: "minor が上がっている", latest: "v1.3.0", current: "v1.2.9", want: true},
		{name: "major が上がっている", latest: "v2.0.0", current: "v1.9.9", want: true},
		{name: "同じ", latest: "v1.2.3", current: "v1.2.3"},
		{name: "こちらが新しい", latest: "v1.2.3", current: "v1.3.0"},
		// プレリリースは、数字が大きくても更新先として提示しない。
		{name: "最新がプレリリース", latest: "v1.3.0-rc.1", current: "v1.2.0"},
		// 逆にプレリリースを使っている人には、同じ番号の正式版を新しいものとして出す。
		{name: "プレリリースから正式版へ", latest: "v1.3.0", current: "v1.3.0-rc.1", want: true},
		// 開発ビルドには更新を勧めない（比較できないものを推測で埋めない）。
		{name: "現在が dev", latest: "v1.2.3", current: "dev"},
		{name: "最新が解釈できない", latest: "latest", current: "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewer(tt.latest, tt.current); got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, 期待 %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}
