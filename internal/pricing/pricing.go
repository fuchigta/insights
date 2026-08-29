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
		if base == name || (len(name) > 0 && len(base) >= len(name) && base[:len(name)] == name) {
			best, bestFound, bestLen = r, true, len(name)
		}
	}
	return best, bestFound
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
