// Command spotter はコミット前後の検査を実行する CLI。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuchigta/spotter/internal/cli"
)

// version はビルド時に -ldflags "-X main.version=..." で差し替える。
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		if !errors.Is(err, cli.ErrCheckFailed) {
			fmt.Fprintln(os.Stderr, "spotter:", err)
		}
		os.Exit(1)
	}
}
