package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/fuchigta/spotter/internal/cirange"
	"github.com/fuchigta/spotter/internal/gitutil"
)

func newRangeCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "range",
		Short: "CI 環境から比較対象の範囲（git の範囲式）を自動検出して出力する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRange(cmd.OutOrStdout(), provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "自動検出をスキップして明示する（github-actions | gitlab-ci）")

	return cmd
}

func runRange(stdout io.Writer, providerFlag string) error {
	provider := cirange.Provider(providerFlag)

	if provider == "" {
		detected, ok := cirange.Detect(os.Getenv)
		if !ok {
			return fmt.Errorf("range: CI 環境を自動検出できませんでした（GitHub Actions / GitLab CI のみ対応）。" +
				"--provider で明示するか、spotter check --range に自分で組み立てた範囲を渡してください")
		}
		provider = detected
	}

	repo := gitutil.New(repoRoot)
	rangeExpr, err := cirange.Resolve(provider, os.Getenv, repo.CommitExists)
	if err != nil {
		return fmt.Errorf("range: %w", err)
	}

	fmt.Fprintln(stdout, rangeExpr)
	return nil
}
