package model

import "testing"

func TestIsInteractiveEntrypoint(t *testing.T) {
	cases := []struct {
		entrypoint string
		want       bool
	}{
		{entrypoint: "cli", want: true},
		{entrypoint: "vscode", want: true},
		{entrypoint: "", want: true}, // 不明なら対話として扱う
		{entrypoint: "sdk-cli", want: false},
		{entrypoint: " sdk-cli ", want: false},
	}

	for _, tc := range cases {
		if got := IsInteractiveEntrypoint(tc.entrypoint); got != tc.want {
			t.Errorf("IsInteractiveEntrypoint(%q) = %v, want %v", tc.entrypoint, got, tc.want)
		}
	}
}
