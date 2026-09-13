// Command spotter はコミット前後の検査を実行する CLI。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuchigta/spotter/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		if !errors.Is(err, cli.ErrCheckFailed) {
			fmt.Fprintln(os.Stderr, "spotter:", err)
		}
		os.Exit(1)
	}
}
