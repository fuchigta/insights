// Package litellm は LiteLLM の価格表（model_prices_and_context_window.json）を、
// insights の埋め込み価格表（internal/pricing/prices.json）の形に変換する。
//
// 実行時には使われない。週 1 回のワークフロー（.github/workflows/prices-sync.yml）が
// cmd/pricesync 経由で呼び、差分を PR として人がレビューしてから取り込む。
package litellm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Rate は 1 モデルの単価。単位は USD / 1M トークン（prices.json と同じ）。
type Rate struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	CacheRead    float64 `json:"cache_read"`
}

// Table は prices.json 全体。フィールド順がそのまま出力順になる。
type Table struct {
	Note        string          `json:"_note"`
	Version     string          `json:"version"`
	NonBillable []string        `json:"non_billable"`
	Models      map[string]Rate `json:"models"`
}

// Manual は prices_manual.json。LiteLLM に無い・違う扱いにしたいモデルを置き、同期結果より優先する。
type Manual struct {
	Note        string          `json:"_note"`
	NonBillable []string        `json:"non_billable"`
	Models      map[string]Rate `json:"models"`
}

// rawEntry は LiteLLM の 1 モデル分のうち必要な項目。
// 単価の欠落（0 と区別したい）を扱うため、数値はポインタで受ける。
type rawEntry struct {
	Provider        string   `json:"litellm_provider"`
	Mode            string   `json:"mode"`
	Input           *float64 `json:"input_cost_per_token"`
	Output          *float64 `json:"output_cost_per_token"`
	CacheRead       *float64 `json:"cache_read_input_token_cost"`
	CacheCreation   *float64 `json:"cache_creation_input_token_cost"`
	CacheCreation1h *float64 `json:"cache_creation_input_token_cost_above_1hr"`
}

// perMillion は USD/トークン を USD/1M トークンに直し、浮動小数の誤差
// （1e-07 * 1e6 = 0.09999999999999999 など）を小数 6 桁で丸める。
func perMillion(perToken float64) float64 {
	return math.Round(perToken*1e6*1e6) / 1e6
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Convert は LiteLLM の JSON から採用対象のモデルだけを取り出して変換する。
//
// 採用条件は、キーに "/" を含まず（プロバイダ経由の別名を除く）、litellm_provider が
// anthropic か openai、キーが claude- か gpt- で始まり、mode が chat か responses のもの。
// 先頭の sample_spec のように型が合わない要素は黙って読み飛ばす。
//
// 長文の階層価格（*_above_200k_tokens など）は使わない。短文側だけを入れる方針で、
// 長い文脈では過小評価になる（docs/cost.md）。
func Convert(data []byte) (map[string]Rate, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("LiteLLM の価格表のパースに失敗: %w", err)
	}

	out := make(map[string]Rate)
	for name, msg := range top {
		if strings.Contains(name, "/") {
			continue
		}
		if !strings.HasPrefix(name, "claude-") && !strings.HasPrefix(name, "gpt-") {
			continue
		}
		var e rawEntry
		if err := json.Unmarshal(msg, &e); err != nil {
			// sample_spec など、モデルではない要素。
			continue
		}
		if e.Provider != "anthropic" && e.Provider != "openai" {
			continue
		}
		if e.Mode != "chat" && e.Mode != "responses" {
			continue
		}
		if e.Input == nil || e.Output == nil {
			continue
		}

		r := Rate{
			Input:     perMillion(*e.Input),
			Output:    perMillion(*e.Output),
			CacheRead: perMillion(val(e.CacheRead)),
		}
		// キャッシュ書き込みは anthropic のみ入れる。
		// openai を 0 にするのは、Codex の input_tokens がキャッシュ書き込みを含む合計で、
		// insights は書き込み分を通常の入力として課金している（internal/source/codex/parse.go の
		// convertUsage）ため。LiteLLM の openai の cache_creation_input_token_cost を入れると
		// 書き込みを二重に数える。公式料金ページにも書き込み単価は無い。
		if e.Provider == "anthropic" {
			if e.CacheCreation != nil {
				r.CacheWrite5m = perMillion(*e.CacheCreation)
			} else {
				// 5 分キャッシュの単価が無いときは、標準倍率の input x1.25 で補う。
				r.CacheWrite5m = perMillion(*e.Input * 1.25)
			}
			if e.CacheCreation1h != nil {
				r.CacheWrite1h = perMillion(*e.CacheCreation1h)
			} else {
				// 1 時間キャッシュの単価が無いときは、標準倍率の input x2 で補う。
				r.CacheWrite1h = perMillion(*e.Input * 2)
			}
		}
		out[name] = r
	}
	return out, nil
}

// Merge は LiteLLM 由来の単価に manual を重ねる（manual 優先）。
// existing にあって結果に無いモデルは黙って消さず、そのまま残して missing に名前を返す。
// いきなり単価未登録になるのを防ぎ、呼び出し側が警告を出せるようにするため。
func Merge(synced map[string]Rate, manual Manual, existing map[string]Rate) (models map[string]Rate, missing []string) {
	models = make(map[string]Rate, len(synced)+len(manual.Models))
	for k, v := range synced {
		models[k] = v
	}
	for k, v := range manual.Models {
		models[k] = v
	}
	for k, v := range existing {
		if _, ok := models[k]; !ok {
			models[k] = v
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return models, missing
}

// Note は生成される prices.json の _note。人間向けの注記で、表には反映されない。
const Note = "USD / 1M トークン。LiteLLM（BerriAI/litellm の model_prices_and_context_window.json）から " +
	"internal/pricing/prices_manual.json を重ねて cmd/pricesync が生成したもの。手で編集せず、" +
	"上書きしたいモデルは prices_manual.json を直すこと（週 1 回のワークフロー prices-sync.yml が PR で更新する）。" +
	"claude-* は Anthropic、gpt-* は OpenAI の API レート。Anthropic 側のキャッシュ書き込みは LiteLLM の値で、" +
	"無ければ 5 分は input x1.25、1 時間は input x2 の標準倍率で補う。OpenAI 側はキャッシュ書き込みを別建てで課金しない" +
	"（Codex の input_tokens は書き込み分を含む合計で、insights は通常の入力として課金する。入れると二重計上になる）ため " +
	"cache_write_* は 0 で、cache_read には cached input のレートが入る。gpt-*-pro はキャッシュ入力の提供が無いため " +
	"cache_read も 0。長文の階層価格（プロンプトが一定長を超えると単価が上がるもの）は扱わず短文側の単価を入れているため、" +
	"長い文脈では過小評価になる。fast mode のように単価が倍になる実行形態も区別できないので、その場合も過小評価になる。" +
	"契約単価が異なる場合は config の pricing.overrides で上書きする。non_billable は課金対象外の擬似モデル名で、" +
	"未知モデルとしては扱わない。"

// Render は Table を prices.json の形（indent 2・UTF-8 のまま・末尾改行あり）に整形する。
// モデルはキー昇順で決定的に出力される（encoding/json がマップのキーを並べ替える）。
func Render(t Table) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "<synthetic>" をそのまま出す
	enc.SetIndent("", "  ")
	if err := enc.Encode(t); err != nil {
		return nil, fmt.Errorf("価格表の出力に失敗: %w", err)
	}
	return buf.Bytes(), nil
}
