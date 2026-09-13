// Package cli は spotter コマンドの実装。
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/version"
)

// ErrCheckFailed は「実行はできたが 1 件以上の検査に違反があった」ことを表すセンチネル。
// main はこれと他のエラー（設定不備・git 実行失敗など）を区別して終了コードとメッセージを
// 出し分ける。
var ErrCheckFailed = errors.New("1 件以上の検査に失敗しました")

// buildVersion は NewRootCommand で渡されたバージョンを保持する。
// required_version（internal/config.Config.RequiredVersion）の判定に使う
// （check / doctor コマンドから参照する）。
var buildVersion = "dev"

// NewRootCommand は spotter のルートコマンドを組み立てる。version は
// -ldflags -X で埋め込まれたビルドバージョン（未指定・go install 時は "dev"）。
func NewRootCommand(version string) *cobra.Command {
	buildVersion = version

	root := &cobra.Command{
		Use:           "spotter",
		Short:         "コミット前後の検査を実行するツール",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newCheckCommand())
	root.AddCommand(newRangeCommand())
	root.AddCommand(newInstallCommand())
	root.AddCommand(newDoctorCommand())
	return root
}

// Execute はルートコマンドを実行する。
func Execute(v string) error {
	return NewRootCommand(v).Execute()
}

// checkRequiredVersion は cfg.RequiredVersion を手元の spotter が満たしているかを
// 確認する。満たしていなければ検査を実行させないため、check / doctor コマンドが
// config.Load の直後に呼ぶ。
func checkRequiredVersion(cfg *config.Config) error {
	ok, err := version.Satisfies(buildVersion, cfg.RequiredVersion)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("spotter のバージョン %s は required_version %s を満たしていません。バイナリを更新してください", buildVersion, cfg.RequiredVersion)
	}
	return nil
}
