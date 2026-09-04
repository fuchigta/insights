// Package cli は insights の cobra コマンド木を組み立てる。
// サブコマンドは機能ごとにファイルを分け、後続の実装（ingest/judge/daily/...）が
// 追加しやすい形にしている。
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/update"
	"github.com/spf13/cobra"
)

// ErrDoctorProblems は config doctor が致命的な設定エラー（Validate() が非空）を
// 検出したときに返す。診断結果は既に出力済みなので、main はメッセージを
// 追加で出さずに終了コード 1 で終了すればよい。
var ErrDoctorProblems = errors.New("設定に致命的な問題があります")

// contextKey は context に載せる値の衝突を避けるための非公開型。
type contextKey int

const stateContextKey contextKey = iota

// cliState は PersistentPreRunE でロードした設定と、その解決済みパスを保持する。
type cliState struct {
	config     *config.Config
	configPath string
	// rawVersion は -ldflags で埋め込まれた生のバージョン。表示用に解決した値ではなく
	// 生の値を持つのは、導入方法の判定（update.DetectInstallMethod）が
	// 「ldflags が入っていないこと」自体を材料にするため。
	rawVersion string
	// updateCh は裏で走らせている更新確認の受け口。確認しない場合は nil。
	updateCh <-chan update.Result
}

// NewRootCommand は insights のルートコマンドを構築する。
// version は -ldflags で埋め込まれたビルドバージョン。
func NewRootCommand(version string) *cobra.Command {
	var (
		configPath string
		dbPath     string
		verbose    bool
		jsonOutput bool
	)

	root := &cobra.Command{
		Use:   "insights",
		Short: "コーディングエージェント利用の価値を振り返る CLI",
		Long:  "セッションログを集約・評価し、日々のAI利用が生んだ価値とコストを振り返るための CLI。",
		// go install で入れた場合は -ldflags が無く version が "dev" のままになるため、
		// モジュールのビルド情報にフォールバックした値を表示する。
		Version:       update.ResolveVersion(version),
		SilenceUsage:  true,
		SilenceErrors: true, // エラー表示は cmd/insights/main.go に一任する
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

			resolvedPath, err := resolveConfigFlagPath(configPath)
			if err != nil {
				return fmt.Errorf("--config の解決に失敗しました: %w", err)
			}

			cfg, err := config.Load(resolvedPath)
			if err != nil {
				return fmt.Errorf("設定の読み込みに失敗しました: %w", err)
			}

			if dbPath != "" {
				expanded, err := config.ExpandPath(dbPath)
				if err != nil {
					return fmt.Errorf("--db の解決に失敗しました: %w", err)
				}
				cfg.Database = expanded
			}

			state := &cliState{config: cfg, configPath: resolvedPath, rawVersion: version}
			cmd.SetContext(context.WithValue(cmd.Context(), stateContextKey, state))

			// 更新確認はここで開始だけして待たない（結果は PersistentPostRunE で拾う）。
			startUpdateCheck(cmd, state)
			return nil
		},
		// PersistentPostRunE は RunE が成功したときだけ走る。失敗の上に
		// 更新通知を重ねないための性質なので、意図的にここへ置いている。
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			state, _ := cmd.Context().Value(stateContextKey).(*cliState)
			finishUpdateCheck(cmd, state)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "設定ファイルのパス (既定: ~/.insights/config.yaml)")
	root.PersistentFlags().StringVar(&dbPath, "db", "", "DB パスの上書き")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "詳細ログを表示する (slog Debug レベル)")
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "機械可読な JSON で出力する")

	root.AddCommand(newConfigCommand())
	root.AddCommand(newIngestCommand())
	root.AddCommand(newJudgeCommand())
	root.AddCommand(newDailyCommand())
	root.AddCommand(newReportCommand())
	root.AddCommand(newRunCommand())
	root.AddCommand(newActionsCommand())
	root.AddCommand(newSkillCommand())
	root.AddCommand(newUpdateCommand(version))

	return root
}

// resolveConfigFlagPath は --config フラグの値を解決する。空なら DefaultPath()。
func resolveConfigFlagPath(flagValue string) (string, error) {
	if flagValue == "" {
		return config.DefaultPath()
	}
	return config.ExpandPath(flagValue)
}

// ConfigFromContext は PersistentPreRunE でロードされた設定を取り出す。
// サブコマンドの RunE から呼ぶことを想定している。
func ConfigFromContext(cmd *cobra.Command) (*config.Config, error) {
	state, ok := cmd.Context().Value(stateContextKey).(*cliState)
	if !ok || state == nil || state.config == nil {
		return nil, errors.New("設定がコンテキストにロードされていません（PersistentPreRunE が実行されていない可能性があります）")
	}
	return state.config, nil
}

// rawVersionFromContext は -ldflags で埋め込まれた生のバージョンを返す。
// 取り出せない場合（root を経由しないテストなど）は空文字を返す。
func rawVersionFromContext(cmd *cobra.Command) string {
	state, ok := cmd.Context().Value(stateContextKey).(*cliState)
	if !ok || state == nil {
		return ""
	}
	return state.rawVersion
}

// ConfigPathFromContext は実際に読み込みを試みた設定ファイルの解決済みパスを返す。
func ConfigPathFromContext(cmd *cobra.Command) (string, error) {
	state, ok := cmd.Context().Value(stateContextKey).(*cliState)
	if !ok || state == nil {
		return "", errors.New("設定パスがコンテキストにロードされていません")
	}
	return state.configPath, nil
}
