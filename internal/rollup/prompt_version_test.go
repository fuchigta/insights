package rollup

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// promptFingerprint は日報・振り返りプロンプト（prompts/daily.md, prompts/retro.md）と
// 出力スキーマ（dailySchema, retroSchema）の内容から求めた指紋。
//
// internal/judge/prompts の TestPromptVersionIsBumpedWhenContentChanges と同じ考え方で、
// PromptVersion（キャッシュキーに使われる）を上げずにプロンプトやスキーマだけ変更すると
// 古い結果が使い回されてしまう事故を防ぐ。内容が変わったらこのテストが落ちるので、
// PromptVersion を上げてから下の値を更新すること。
const promptFingerprint = "fddebc56e51fbd2e2129ae513fef5a4d68bbc28c5d745e0c454b959a3731eb3f"

func TestPromptVersionIsBumpedWhenContentChanges(t *testing.T) {
	h := sha256.New()
	h.Write([]byte(dailyPromptTemplate))
	h.Write([]byte(retroPromptTemplate))
	h.Write(dailySchema)
	h.Write(retroSchema)
	got := hex.EncodeToString(h.Sum(nil))

	if promptFingerprint == "" {
		t.Fatalf("promptFingerprint が未設定です。次の値を設定してください: %s (PromptVersion=%s)", got, PromptVersion)
	}
	if got != promptFingerprint {
		t.Errorf(`日報・振り返りのプロンプトまたはスキーマが変更されています。
  PromptVersion を上げてから promptFingerprint を更新してください。
  現在の PromptVersion: %s
  新しい指紋:           %s`, PromptVersion, got)
	}
}
