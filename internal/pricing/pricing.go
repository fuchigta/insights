// Package pricing はモデル別の単価表を保持し、トークン使用量から USD 建てのコストを算出する。
// 単価は prices.json に埋め込まれるが、現時点では実際の単価が確定していないため全項目 0.0 の
// プレースホルダになっている（値は insights config doctor が別途警告する）。
package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/fuchigta/insights/internal/model"
)

//go:embed prices.json
var pricesFS embed.FS

// Rate は 1 モデルの単価。単位は USD / 1M トークン。
type Rate struct {
	Input        float64 // 入力トークン単価
	Output       float64 // 出力トークン単価（thinking トークンも同じ単価で扱う）
	CacheWrite5m float64 // 5分キャッシュ書き込み単価
	CacheWrite1h float64 // 1時間キャッシュ書き込み単価
	CacheRead    float64 // キャッシュ読み取り単価
}

// isZero は Rate の全項目が 0 かどうかを返す。UnpricedModels の判定に使う。
func (r Rate) isZero() bool {
	return r.Input == 0 && r.Output == 0 && r.CacheWrite5m == 0 && r.CacheWrite1h == 0 && r.CacheRead == 0
}

// Table はモデル名から単価を引く表。
type Table struct {
	Version string
	rates   map[string]Rate
	// nonBillable は課金対象外の擬似モデル名の集合。Claude Code のログには
	// API 呼び出しを伴わない合成メッセージ（"<synthetic>"）が混ざるため、
	// これらを「未知モデル」として警告しないよう区別する。
	nonBillable map[string]struct{}
}

// rawRate は prices.json 内の 1 モデル分の JSON 表現。
type rawRate struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	CacheRead    float64 `json:"cache_read"`
}

// rawTable は prices.json 全体の JSON 表現。_note は人間向けの注記で表には反映しない。
type rawTable struct {
	Note        string             `json:"_note"`
	Version     string             `json:"version"`
	NonBillable []string           `json:"non_billable"`
	Models      map[string]rawRate `json:"models"`
}

// Load は埋め込まれた prices.json を読み込み、overrides をマージして Table を返す。
// overrides に同名モデルがあれば埋め込み値を上書きし、埋め込み表に無いモデルは新規追加される。
func Load(overrides map[string]Rate) (*Table, error) {
	data, err := pricesFS.ReadFile("prices.json")
	if err != nil {
		return nil, fmt.Errorf("prices.json の読み込みに失敗: %w", err)
	}

	var raw rawTable
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("prices.json のパースに失敗: %w", err)
	}

	t := &Table{
		Version:     raw.Version,
		rates:       make(map[string]Rate, len(raw.Models)+len(overrides)),
		nonBillable: make(map[string]struct{}, len(raw.NonBillable)),
	}
	for _, name := range raw.NonBillable {
		t.nonBillable[name] = struct{}{}
	}
	for name, r := range raw.Models {
		t.rates[name] = Rate{
			Input:        r.Input,
			Output:       r.Output,
			CacheWrite5m: r.CacheWrite5m,
			CacheWrite1h: r.CacheWrite1h,
			CacheRead:    r.CacheRead,
		}
	}
	for name, r := range overrides {
		t.rates[name] = r
	}
	return t, nil
}

// dateSuffixRE は末尾の "-YYYYMMDD" 形式の日付サフィックスにマッチする。
// 例: "claude-haiku-4-5-20251001" -> "-20251001"
var dateSuffixRE = regexp.MustCompile(`-\d{8}$`)

// stripDateSuffix はモデル名末尾の日付サフィックスを取り除く。無ければそのまま返す。
func stripDateSuffix(modelName string) string {
	return dateSuffixRE.ReplaceAllString(modelName, "")
}

// bracketSuffixRE は "claude-opus-4-6[1m]" のような、Claude Code がモデル指定に付ける
// 角括弧の接尾辞（文脈長の指定など）にマッチする。
var bracketSuffixRE = regexp.MustCompile(`\[[^\]]*\]$`)

// familyVersionRE は "claude-<系列>-" に続く世代番号だけの残り（"5"、"5-5"、"4-6" など）にマッチする。
// 日付付き（8 桁）やサイズ違いの派生は「その系列の最新」の候補にしない。
var familyVersionRE = regexp.MustCompile(`^\d{1,2}(-\d{1,2})*$`)

// ResolveAlias は Claude Code のモデルエイリアス（"haiku" / "sonnet" / "opus" など）を、
// 単価表にあるその系列の最新世代のモデル名に解決する。"[1m]" のような接尾辞は外す。
// エイリアスでなければ（単価表で引ける名前なら）接尾辞を外した名前をそのまま返す。
// 解決できなければ ok=false。
//
// エイリアスの指す先は Claude Code 側の更新で変わるため、対応表を持つと古くなる。
// 単価表（LiteLLM から週 1 回同期している）の中で世代番号が最大のものを採ることで、
// 単価表の更新に合わせて追従させる。
func (t *Table) ResolveAlias(name string) (string, bool) {
	name = strings.TrimSpace(bracketSuffixRE.ReplaceAllString(strings.TrimSpace(name), ""))
	if name == "" {
		return "", false
	}
	if _, ok := t.Rate(name); ok {
		return name, true
	}
	if strings.Contains(name, "-") {
		return "", false
	}

	prefix := "claude-" + strings.ToLower(name) + "-"
	var best string
	var bestVer []int
	for registered := range t.rates {
		rest, ok := strings.CutPrefix(registered, prefix)
		if !ok || !familyVersionRE.MatchString(rest) {
			continue
		}
		ver := parseVersion(rest)
		if best == "" || compareVersion(ver, bestVer) > 0 {
			best, bestVer = registered, ver
		}
	}
	return best, best != ""
}

// parseVersion は "5-5" のような世代番号を数値の列にする（familyVersionRE に一致済みの前提）。
func parseVersion(s string) []int {
	parts := strings.Split(s, "-")
	out := make([]int, len(parts))
	for i, p := range parts {
		for _, c := range p {
			out[i] = out[i]*10 + int(c-'0')
		}
	}
	return out
}

// compareVersion は世代番号の列を比べる。"5" と "5-0" は同じ、"5-5" は "5" より新しい。
func compareVersion(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// Rate はモデル名から単価を引く。
//
// 解決順序:
//  1. 完全一致
//  2. 日付サフィックスを剥がした名前での完全一致
//  3. 日付サフィックスを剥がした名前を対象にした、登録済みモデル名の前方一致
//     （複数該当する場合は最長一致を採用する）
//
// いずれにも一致しなければ未知として ok=false を返す。
func (t *Table) Rate(modelName string) (Rate, bool) {
	if _, ok := t.nonBillable[modelName]; ok {
		return Rate{}, true
	}
	if r, ok := t.rates[modelName]; ok {
		return r, true
	}

	base := stripDateSuffix(modelName)
	if base != modelName {
		if r, ok := t.rates[base]; ok {
			return r, true
		}
	}

	var (
		best      Rate
		bestFound bool
		bestLen   int
	)
	for name, r := range t.rates {
		if len(name) <= bestLen {
			continue
		}
		if isVariantOf(base, name) {
			best, bestFound, bestLen = r, true, len(name)
		}
	}
	return best, bestFound
}

// isVariantOf は modelName が registered そのものか、registered に "-" 区切りの
// 派生（サイズ違い・日付など）を足したものかを返す。
//
// 単純な前方一致にしないのは、世代が上がっただけの別モデルを取り違えるため。
// "gpt-5" が登録済みのとき、素の前方一致では "gpt-5.6-sol" まで拾ってしまい、
// 3 世代前の安い単価で黙って計算される。世代の区切り（"."）をまたぐ一致は
// 認めないことで、知らないモデルは「単価未登録」として警告に出せる
// ——このツールでは、それらしい金額が出るより未知だと分かるほうが有用なので。
//
// Claude は世代そのものを "-" で区切る（"claude-opus-5" と "claude-opus-5-5" は別世代で、
// 単価も違う）。"-" の直後が 1〜2 桁の数字だけの区切りは世代番号とみなし、派生として
// 認めない。日付サフィックス（8 桁）は呼び出し前に剥がしてあるので、ここには来ない。
func isVariantOf(modelName, registered string) bool {
	if registered == "" || len(modelName) < len(registered) || modelName[:len(registered)] != registered {
		return false
	}
	if len(modelName) == len(registered) {
		return true
	}
	if modelName[len(registered)] != '-' {
		return false
	}
	return !isGenerationSegment(modelName[len(registered)+1:])
}

// isGenerationSegment は rest の先頭の "-" 区切り 1 つ分が、世代番号（1〜2 桁の数字）かを返す。
func isGenerationSegment(rest string) bool {
	seg := rest
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		seg = rest[:i]
	}
	if len(seg) == 0 || len(seg) > 2 {
		return false
	}
	for _, c := range seg {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Cost はモデルとトークン使用量から USD 建てのコストを算出する。
//
// thinking トークンを output に加算してはいけない。ログの thinking_tokens は
// output_tokens_details 配下の「内訳」であり、すでに output_tokens に含まれている
// （実データ例: output_tokens=155 のうち thinking_tokens=27）。加算すると過大請求になる。
//
// 未知モデルの場合は known=false を返し、呼び出し側で 0 円として黙って合算しないようにする。
func (t *Table) Cost(modelName string, u model.Usage) (usd float64, known bool) {
	r, ok := t.Rate(modelName)
	if !ok {
		return 0, false
	}

	const perMillion = 1_000_000.0
	usd = float64(u.InputTokens)*r.Input/perMillion +
		float64(u.OutputTokens)*r.Output/perMillion +
		float64(u.CacheCreation5m)*r.CacheWrite5m/perMillion +
		float64(u.CacheCreation1h)*r.CacheWrite1h/perMillion +
		float64(u.CacheRead)*r.CacheRead/perMillion
	return usd, true
}

// UnpricedModels は単価が全項目 0 のモデル名を返す（doctor が「単価未設定」を警告するために使う）。
func (t *Table) UnpricedModels() []string {
	var names []string
	for name, r := range t.rates {
		if r.isZero() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
