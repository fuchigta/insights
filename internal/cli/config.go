package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuchigta/insights/internal/config"
	"github.com/spf13/cobra"
)

// newConfigCommand は `insights config` サブコマンド群を組み立てる。
func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "設定ファイルの初期化・診断",
	}
	cmd.AddCommand(newConfigInitCommand())
	cmd.AddCommand(newConfigDoctorCommand())
	return cmd
}

// newConfigInitCommand は `insights config init` を組み立てる。
func newConfigInitCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "設定ファイルの雛形を既定パスに書き出す",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := ConfigPathFromContext(cmd)
			if err != nil {
				return err
			}

			if _, statErr := os.Stat(path); statErr == nil {
				if !force {
					return fmt.Errorf("設定ファイルは既に存在します: %s（上書きするには --force を指定してください）", path)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("設定ファイルの確認に失敗しました: %w", statErr)
			}

			if err := config.Default().Save(path); err != nil {
				return fmt.Errorf("設定ファイルの書き出しに失敗しました: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "設定ファイルを書き出しました: %s\n", path)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "既存の設定ファイルを上書きする")
	return cmd
}
