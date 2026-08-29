// Command insights はコーディングエージェント利用の価値振り返り CLI のエントリポイント。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuchigta/insights/internal/cli"
)

// version はビルド時に -ldflags "-X main.version=..." で差し替える。
var version = "dev"

func main() {
	root := cli.NewRootCommand(version)
	if err := root.Execute(); err != nil {
		// ErrDoctorProblems は診断結果を既に出力済みなので、二重にメッセージを出さない。
		if !errors.Is(err, cli.ErrDoctorProblems) {
			fmt.Fprintf(os.Stderr, "エラー: %s\n", err)
		}
		os.Exit(1)
	}
}
