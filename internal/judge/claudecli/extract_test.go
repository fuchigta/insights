package claudecli

import (
	"encoding/json"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string // 期待する抽出結果（json.Valid で意味的に比較する）
		wantErr bool
	}{
		{
			name:  "素のJSON",
			input: `{"a":1,"b":"x"}`,
			want:  `{"a":1,"b":"x"}`,
		},
		{
			name:  "前後に空白",
			input: "  \n\t" + `{"a":1}` + "  \n",
			want:  `{"a":1}`,
		},
		{
			name:  "jsonフェンス付き",
			input: "```json\n{\"a\":1,\"nested\":{\"b\":2}}\n```",
			want:  `{"a":1,"nested":{"b":2}}`,
		},
		{
			name:  "フェンスのみ（言語指定なし）",
			input: "```\n{\"a\":1}\n```",
			want:  `{"a":1}`,
		},
		{
			name:  "前後に説明文",
			input: "評価結果は以下です:\n\n" + `{"a":1,"b":[1,2,3]}` + "\n\n以上です。",
			want:  `{"a":1,"b":[1,2,3]}`,
		},
		{
			name:  "文字列中に波括弧を含む",
			input: `{"note":"use { and } carefully","ok":true}`,
			want:  `{"note":"use { and } carefully","ok":true}`,
		},
		{
			name:  "文字列中にエスケープされたダブルクォート",
			input: `{"note":"she said \"hi { there\"","ok":true}`,
			want:  `{"note":"she said \"hi { there\"","ok":true}`,
		},
		{
			name:    "波括弧が開始しない",
			input:   "申し訳ありませんが、JSON を返せません。",
			wantErr: true,
		},
		{
			name:    "閉じ括弧がない（壊れたJSON）",
			input:   `{"a":1,"b":`,
			wantErr: true,
		},
		{
			name:    "波括弧の対応は取れるが中身が不正なJSON",
			input:   `{"a":1,,}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractJSON(%q) error = nil, wantErr", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractJSON(%q) unexpected error = %v", tt.input, err)
			}

			var gotVal, wantVal any
			if err := json.Unmarshal(got, &gotVal); err != nil {
				t.Fatalf("抽出結果が JSON として parse できない: %v (%s)", err, got)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantVal); err != nil {
				t.Fatalf("テストデータ不正: %v", err)
			}

			gotNorm, _ := json.Marshal(gotVal)
			wantNorm, _ := json.Marshal(wantVal)
			if string(gotNorm) != string(wantNorm) {
				t.Errorf("ExtractJSON(%q) = %s, want %s", tt.input, gotNorm, wantNorm)
			}
		})
	}
}
