// このファイルは `insights skill install|status|uninstall` を実装する。
// スキルの配置規約は internal/skill.Installer（エージェント別実装は自己登録方式）に
// 委譲し、ここでは CLI としての引数解釈・出力整形のみを担う。
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/fuchigta/insights/internal/skill"
	// internal/skill はインポート循環を避けるため自己登録方式を採っている
	// （internal/skill/registry.go 参照）。skill.ByAgent("claude-code") /
	// skill.ByAgent("codex") を解決できるよう、実装パッケージを副作用インポートして
	// レジストリに登録させる。
	_ "github.com/fuchigta/insights/internal/skill/claudecode"
	_ "github.com/fuchigta/insights/internal/skill/codex"
	"github.com/spf13/cobra"
)

// skillInstallResult は `insights skill install` の実行結果全体。
type skillInstallResult struct {
	Agent         string   `json:"agent"`
	Scope         string   `json:"scope"`
	Path          string   `json:"path"`
	Written       []string `json:"written"`
	PreviousState string   `json:"previous_state"`
}

// skillStatusView は `insights skill status` の実行結果全体。
type skillStatusView struct {
	Agent            string `json:"agent"`
	Scope            string `json:"scope"`
	Path             string `json:"path"`
	State            string `json:"state"`
	InstalledVersion string `json:"installed_version,omitempty"`
	BundledVersion   string `json:"bundled_version"`
}

// skillUninstallResult は `insights skill uninstall` の実行結果全体。
type skillUninstallResult struct {
	Agent        string `json:"agent"`
	Scope        string `json:"scope"`
	Path         string `json:"path"`
	WasInstalled bool   `json:"was_installed"`
}

// newSkillCommand は `insights skill` サブコマンド群を組み立てる。
func newSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "コーディングエージェント向けスキルの導入・状態確認・削除",
	}
	cmd.AddCommand(newSkillInstallCommand())
	cmd.AddCommand(newSkillStatusCommand())
	cmd.AddCommand(newSkillUninstallCommand())
	return cmd
}

// newSkillInstallCommand は `insights skill install` を組み立てる。
func newSkillInstallCommand() *cobra.Command {
	var (
		agent    string
		scopeStr string
		force    bool
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "スキルを導入する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillInstall(cmd, agent, scopeStr, force)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude-code", "対象のコーディングエージェント (claude-code|codex)")
	cmd.Flags().StringVar(&scopeStr, "scope", "user", "導入範囲 (user|project)")
	cmd.Flags().BoolVar(&force, "force", false, "手で書き換えられたスキルを上書きする")
	return cmd
}

// newSkillStatusCommand は `insights skill status` を組み立てる。
func newSkillStatusCommand() *cobra.Command {
	var (
		agent    string
		scopeStr string
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "スキルの導入状態を確認する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillStatus(cmd, agent, scopeStr)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude-code", "対象のコーディングエージェント (claude-code|codex)")
	cmd.Flags().StringVar(&scopeStr, "scope", "user", "導入範囲 (user|project)")
	return cmd
}

// newSkillUninstallCommand は `insights skill uninstall` を組み立てる。
func newSkillUninstallCommand() *cobra.Command {
	var (
		agent    string
		scopeStr string
	)

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "スキルを削除する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillUninstall(cmd, agent, scopeStr)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude-code", "対象のコーディングエージェント (claude-code|codex)")
	cmd.Flags().StringVar(&scopeStr, "scope", "user", "導入範囲 (user|project)")
	return cmd
}

// runSkillInstall は install サブコマンドの本体。
// State が StateModified のとき --force が無ければ、上書きに --force が必要である旨を
// 明示したエラーで止める（Installer.Install も同種のエラーを返すが、ここでは CLI の
// フラグ名 --force を含めたメッセージにするため、事前に Status を確認する）。
func runSkillInstall(cmd *cobra.Command, agent, scopeStr string, force bool) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	inst, err := resolveInstaller(agent)
	if err != nil {
		return err
	}
	sc, err := resolveScope(scopeStr)
	if err != nil {
		return err
	}

	if !force {
		st, err := inst.Status(sc)
		if err != nil {
			return fmt.Errorf("スキル状態の取得に失敗しました: %w", err)
		}
		if st.State == skill.StateModified {
			return fmt.Errorf(
				"%s の SKILL.md は手で編集されています。上書きするには --force を指定してください: %s",
				agent, st.Path,
			)
		}
	}

	result, err := inst.Install(sc, force)
	if err != nil {
		return fmt.Errorf("スキルの導入に失敗しました: %w", err)
	}

	view := skillInstallResult{
		Agent: agent, Scope: string(sc), Path: result.Path,
		Written: result.Written, PreviousState: string(result.From),
	}
	return PrintResult(cmd, func(w io.Writer) error {
		return renderSkillInstallHuman(w, view)
	}, view)
}

// runSkillStatus は status サブコマンドの本体。
func runSkillStatus(cmd *cobra.Command, agent, scopeStr string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	inst, err := resolveInstaller(agent)
	if err != nil {
		return err
	}
	sc, err := resolveScope(scopeStr)
	if err != nil {
		return err
	}

	st, err := inst.Status(sc)
	if err != nil {
		return fmt.Errorf("スキル状態の取得に失敗しました: %w", err)
	}

	view := skillStatusView{
		Agent: st.Agent, Scope: string(st.Scope), Path: st.Path,
		State: string(st.State), InstalledVersion: st.InstalledVersion, BundledVersion: st.BundledVersion,
	}
	return PrintResult(cmd, func(w io.Writer) error {
		return renderSkillStatusHuman(w, view)
	}, view)
}

// runSkillUninstall は uninstall サブコマンドの本体。
func runSkillUninstall(cmd *cobra.Command, agent, scopeStr string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	inst, err := resolveInstaller(agent)
	if err != nil {
		return err
	}
	sc, err := resolveScope(scopeStr)
	if err != nil {
		return err
	}

	statusBefore, err := inst.Status(sc)
	if err != nil {
		return fmt.Errorf("スキル状態の取得に失敗しました: %w", err)
	}

	if err := inst.Uninstall(sc); err != nil {
		return fmt.Errorf("スキルの削除に失敗しました: %w", err)
	}

	view := skillUninstallResult{
		Agent: agent, Scope: string(sc), Path: statusBefore.Path,
		WasInstalled: statusBefore.State != skill.StateAbsent,
	}
	return PrintResult(cmd, func(w io.Writer) error {
		return renderSkillUninstallHuman(w, view)
	}, view)
}

// resolveInstaller は agent 名から Installer を引く。未知の名前なら、利用可能な
// エージェント名を列挙したエラーにする。
func resolveInstaller(agent string) (skill.Installer, error) {
	inst, err := skill.ByAgent(agent)
	if err == nil {
		return inst, nil
	}

	available := skill.Installers()
	if len(available) == 0 {
		return nil, fmt.Errorf("未知のエージェントです: %q（利用可能なエージェントがありません）", agent)
	}
	names := make([]string, 0, len(available))
	for _, i := range available {
		names = append(names, i.Agent())
	}
	return nil, fmt.Errorf("未知のエージェントです: %q（利用可能なエージェント: %s）", agent, strings.Join(names, ", "))
}

// resolveScope は文字列を skill.Scope に検証しながら変換する。
func resolveScope(s string) (skill.Scope, error) {
	switch skill.Scope(s) {
	case skill.ScopeUser, skill.ScopeProject:
		return skill.Scope(s), nil
	default:
		return "", fmt.Errorf("--scope の値が不正です（user|project のいずれか）: %q", s)
	}
}

// skillStateJPLabel は導入状態の日本語表示。
var skillStateJPLabel = map[skill.State]string{
	skill.StateAbsent:   "未導入",
	skill.StateCurrent:  "最新",
	skill.StateOutdated: "旧版",
	skill.StateModified: "手で改変済み",
}

func skillStateJP(s string) string {
	if v, ok := skillStateJPLabel[skill.State(s)]; ok {
		return v
	}
	return s
}

func renderSkillInstallHuman(w io.Writer, r skillInstallResult) error {
	fmt.Fprintln(w, "=== insights skill install ===")
	fmt.Fprintf(w, "エージェント: %s / スコープ: %s\n", r.Agent, r.Scope)
	fmt.Fprintf(w, "配置先: %s\n", r.Path)
	for _, f := range r.Written {
		fmt.Fprintf(w, "  書き込み: %s\n", f)
	}
	fmt.Fprintf(w, "導入前の状態: %s\n", skillStateJP(r.PreviousState))
	return nil
}

func renderSkillStatusHuman(w io.Writer, r skillStatusView) error {
	fmt.Fprintln(w, "=== insights skill status ===")
	fmt.Fprintf(w, "エージェント: %s / スコープ: %s\n", r.Agent, r.Scope)
	fmt.Fprintf(w, "配置先: %s\n", r.Path)
	fmt.Fprintf(w, "状態: %s\n", skillStateJP(r.State))
	fmt.Fprintf(w, "導入済みバージョン: %s\n", orDash(r.InstalledVersion))
	fmt.Fprintf(w, "同梱バージョン: %s\n", orDash(r.BundledVersion))
	return nil
}

func renderSkillUninstallHuman(w io.Writer, r skillUninstallResult) error {
	fmt.Fprintln(w, "=== insights skill uninstall ===")
	fmt.Fprintf(w, "エージェント: %s / スコープ: %s\n", r.Agent, r.Scope)
	if !r.WasInstalled {
		fmt.Fprintln(w, "導入されていません。")
		return nil
	}
	fmt.Fprintf(w, "削除しました: %s\n", r.Path)
	return nil
}
