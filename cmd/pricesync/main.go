// pricesync は LiteLLM の価格表と手動の上書きから、埋め込み価格表 prices.json を生成する。
// 変換の中身は internal/pricing/litellm にあり、ここは入出力をつなぐだけ。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuchigta/insights/internal/pricing/litellm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		litellmPath = flag.String("litellm", "", "LiteLLM の価格表 JSON のパス（必須）")
		manualPath  = flag.String("manual", "internal/pricing/prices_manual.json", "手動の上書きファイルのパス")
		outPath     = flag.String("out", "internal/pricing/prices.json", "出力先（既存ファイルがあれば、消えるモデルの警告に使う）")
		version     = flag.String("version", time.Now().UTC().Format("2006-01-02"), "価格表の version（YYYY-MM-DD）")
	)
	flag.Parse()
	if *litellmPath == "" {
		return errors.New("-litellm を指定してください")
	}

	src, err := os.ReadFile(*litellmPath)
	if err != nil {
		return fmt.Errorf("LiteLLM の価格表を読めません: %w", err)
	}
	synced, err := litellm.Convert(src)
	if err != nil {
		return err
	}

	var manual litellm.Manual
	if err := readJSON(*manualPath, &manual); err != nil {
		return fmt.Errorf("手動の上書きファイルを読めません: %w", err)
	}

	// 出力先がまだ無いのは正常（初回生成）。
	var existing litellm.Table
	if err := readJSON(*outPath, &existing); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("既存の価格表を読めません: %w", err)
	}

	models, missing := litellm.Merge(synced, manual, existing.Models)
	for _, name := range missing {
		fmt.Fprintf(os.Stderr, "警告: %s は LiteLLM にも prices_manual.json にも無いため、既存の単価をそのまま残しました\n", name)
	}

	nonBillable := manual.NonBillable
	if nonBillable == nil {
		nonBillable = []string{}
	}
	out, err := litellm.Render(litellm.Table{
		Note:        litellm.Note,
		Version:     *version,
		NonBillable: nonBillable,
		Models:      models,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, out, 0o644); err != nil {
		return fmt.Errorf("価格表を書き込めません: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%d モデルを書き出しました: %s\n", len(models), *outPath)
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
