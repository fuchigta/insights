package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/gitutil"
	"github.com/fuchigta/spotter/internal/hooks"
)

func newDoctorCommand() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "設定済みの検査一覧と、フックが有効化されているかを表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd.OutOrStdout(), configPath)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", config.DefaultPath, "設定ファイルのパス")

	return cmd
}

func runDoctor(stdout io.Writer, configPath string) error {
	fmt.Fprintf(stdout, "設定: %s\n", configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stdout, "  読み込みに失敗しました: %v\n", err)
		return fmt.Errorf("doctor: %w", err)
	}

	keys := make([]string, 0, len(cfg.Checks))
	for k := range cfg.Checks {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		fmt.Fprintln(stdout, "  検査は 1 つも設定されていません")
	}

	buildFailed := false
	for _, key := range keys {
		cc := cfg.Checks[key]
		runner, err := buildRunner(cfg, key, cc)
		if err != nil {
			fmt.Fprintf(stdout, "  - %s: エラー（%v）\n", key, err)
			buildFailed = true
			continue
		}
		fmt.Fprintf(stdout, "  - %s（type=%s, granularity=%s）\n", key, cc.Type, runner.Granularity())
	}

	fmt.Fprintln(stdout, "フック:")

	repo := gitutil.New(repoRoot)
	status, err := hooks.Inspect(repo)
	if err != nil {
		return fmt.Errorf("doctor: %w", err)
	}

	if status.HooksPath == "" {
		fmt.Fprintln(stdout, "  core.hooksPath: (未設定。既定の hooks ディレクトリを使用)")
	} else {
		fmt.Fprintf(stdout, "  core.hooksPath: %s\n", status.HooksPath)
	}

	switch {
	case !status.HookFileExists:
		fmt.Fprintf(stdout, "  %s: 無し（`spotter install` で作成できます）\n", status.HookFile)
	case status.Managed:
		fmt.Fprintf(stdout, "  %s: あり（spotter を呼び出しています）\n", status.HookFile)
	default:
		fmt.Fprintf(stdout, "  %s: あり（spotter は未設定。`spotter install` で追記できます）\n", status.HookFile)
	}

	if buildFailed {
		return ErrCheckFailed
	}
	return nil
}
