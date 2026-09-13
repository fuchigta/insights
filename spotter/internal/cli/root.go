// Package cli は spotter コマンドの実装。
package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// ErrCheckFailed は「実行はできたが 1 件以上の検査に違反があった」ことを表すセンチネル。
// main はこれと他のエラー（設定不備・git 実行失敗など）を区別して終了コードとメッセージを
// 出し分ける。
var ErrCheckFailed = errors.New("1 件以上の検査に失敗しました")

// NewRootCommand は spotter のルートコマンドを組み立てる。
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "spotter",
		Short:         "コミット前後の検査を実行するツール",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newCheckCommand())
	return root
}

// Execute はルートコマンドを実行する。
func Execute() error {
	return NewRootCommand().Execute()
}
