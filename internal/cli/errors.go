package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// PrintResult は --json フラグの有無に応じて出力を切り替える共通ヘルパ。
// --json が指定されていれば payload をそのまま JSON エンコードして出力し、
// そうでなければ human に人間向けの整形出力を書かせる。
// 他のサブコマンド（ingest/judge/daily/... 予定）も同じ形で再利用する。
func PrintResult(cmd *cobra.Command, human func(w io.Writer) error, payload any) error {
	if JSONOutput(cmd) {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return fmt.Errorf("JSON の出力に失敗しました: %w", err)
		}
		return nil
	}
	return human(cmd.OutOrStdout())
}

// JSONOutput はルートの永続フラグ --json が指定されたかを返す。
// フラグが見つからない場合（テストなどで root を経由しない場合）は false を返す。
func JSONOutput(cmd *cobra.Command) bool {
	v, err := cmd.Flags().GetBool("json")
	if err != nil {
		return false
	}
	return v
}
