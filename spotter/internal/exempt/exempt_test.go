package exempt

import "testing"

func TestCheck(t *testing.T) {
	cfg := Config{Enable: true, Trailer: "Doc-Sync"}

	tests := []struct {
		name       string
		message    string
		wantSkip   bool
		wantReason string
	}{
		{"理由つきスキップは免除される", "fix: 何か\n\nDoc-Sync: skip 内部リファクタリングのみ", true, "内部リファクタリングのみ"},
		{"大文字小文字は無視する", "fix: 何か\n\ndoc-sync: SKIP 理由", true, "理由"},
		{"理由が無ければ免除されない", "fix: 何か\n\nDoc-Sync: skip", false, ""},
		{"理由が空白のみなら免除されない", "fix: 何か\n\nDoc-Sync: skip   ", false, ""},
		{"トレーラが無ければ免除されない", "fix: 何か\n\n本文のみ", false, ""},
		{"別のトレーラ名には反応しない", "fix: 何か\n\nUnwanted-Files: skip 理由", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip, reason, err := Check(cfg, tt.message)
			if err != nil {
				t.Fatalf("Check() error: %v", err)
			}
			if skip != tt.wantSkip || reason != tt.wantReason {
				t.Errorf("Check() = (%v, %q), want (%v, %q)", skip, reason, tt.wantSkip, tt.wantReason)
			}
		})
	}
}

func TestCheckDisabled(t *testing.T) {
	cfg := Config{Enable: false, Trailer: "Doc-Sync"}
	skip, _, err := Check(cfg, "Doc-Sync: skip 理由")
	if err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if skip {
		t.Errorf("Enable: false のときは免除されないはず")
	}
}
