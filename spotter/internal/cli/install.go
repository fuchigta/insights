package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/fuchigta/spotter/internal/gitutil"
	"github.com/fuchigta/spotter/internal/hooks"
)

func newInstallCommand() *cobra.Command {
	var (
		printOnly bool
		hooksDir  string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "commit-msg フックを設置する（core.hooksPath の設定、または既存フックへの追記）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if printOnly {
				fmt.Fprintln(cmd.OutOrStdout(), hooks.InvocationLine)
				return nil
			}
			return runInstall(cmd.OutOrStdout(), hooksDir)
		},
	}

	cmd.Flags().BoolVar(&printOnly, "print", false,
		"呼び出し行だけを出力する（何も変更しない。lefthook などの既存フックランナーに貼る用）")
	cmd.Flags().StringVar(&hooksDir, "hooks-dir", ".githooks",
		"core.hooksPath が未設定のときに新規作成するディレクトリ")

	return cmd
}

func runInstall(stdout io.Writer, hooksDir string) error {
	repo := gitutil.New(repoRoot)

	result, err := hooks.Install(repo, hooksDir)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}

	switch result.Outcome {
	case hooks.OutcomeAlready:
		fmt.Fprintf(stdout, "既に設置済みです: %s\n", result.HookFile)
	case hooks.OutcomeCreated:
		fmt.Fprintf(stdout, "commit-msg フックを新規作成しました: %s\n", result.HookFile)
	case hooks.OutcomeAppended:
		fmt.Fprintf(stdout, "既存の commit-msg フックに追記しました: %s\n", result.HookFile)
	}

	if result.HooksPathChanged {
		fmt.Fprintf(stdout, "core.hooksPath を %s に設定しました\n", result.HooksPath)
	} else if result.HooksPath != "" {
		fmt.Fprintf(stdout, "core.hooksPath は %s のまま変更していません\n", result.HooksPath)
	}

	return nil
}
