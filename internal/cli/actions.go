// このファイルは `insights actions` を実装する。振り返り（daily/retro）が生成した
// 改善提案を確認し、状態を手で変えるためのコマンドで、AI 呼び出しは一切行わない。
package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/model"
	"github.com/spf13/cobra"
)

// actionsListVerdictMaxRunes は一覧表示で検証所見を切り詰める長さ（rune 数）。
const actionsListVerdictMaxRunes = 80

// actionStatusOrder はサマリ表示・全状態一覧の固定順序。
var actionStatusOrder = []model.ActionStatus{
	model.ActionOpen, model.ActionDone, model.ActionDropped, model.ActionExpired,
}

// actionStatusJPLabel は状態の日本語表示。
var actionStatusJPLabel = map[string]string{
	string(model.ActionOpen):    "未着手(open)",
	string(model.ActionDone):    "完了(done)",
	string(model.ActionDropped): "見送り(dropped)",
	string(model.ActionExpired): "期限切れ(expired)",
}

func actionStatusJP(s string) string {
	if v, ok := actionStatusJPLabel[s]; ok {
		return v
	}
	return s
}

// actionStatusCount は状態別の件数。
type actionStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// actionView は一覧表示用の 1 件分のビュー。
type actionView struct {
	ID         int64  `json:"id"`
	CreatedOn  string `json:"created_on"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Verdict    string `json:"verdict,omitempty"`
	VerifiedOn string `json:"verified_on,omitempty"`
}

// actionsListResult は `insights actions [list]` の実行結果全体。
type actionsListResult struct {
	// Filter は "open"（既定） | "all" | "status:<status>"。
	Filter        string              `json:"filter"`
	Total         int                 `json:"total"`
	Shown         int                 `json:"shown"`
	StatusSummary []actionStatusCount `json:"status_summary"`
	Actions       []actionView        `json:"actions"`
}

// actionDetailView は `insights actions show <ID>` の実行結果全体。
type actionDetailView struct {
	ID         int64  `json:"id"`
	CreatedOn  string `json:"created_on"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Category   string `json:"category"`
	Status     string `json:"status"`
	Verdict    string `json:"verdict"`
	VerifiedOn string `json:"verified_on"`
}

// newActionsCommand は `insights actions` を組み立てる。
// 引数なし（サブコマンド無し）で実行した場合は list と同じ動作になる。
func newActionsCommand() *cobra.Command {
	var (
		all        bool
		statusFlag string
	)

	cmd := &cobra.Command{
		Use:   "actions",
		Short: "改善提案を確認・整理する（list|show|drop|reopen。AI 呼び出しは行わない）",
		Long: "振り返り（daily/retro）が生成した改善提案を確認し、状態を手で変える。AI 呼び出しは一切行わない。\n" +
			"サブコマンドを指定しない場合は `list` と同じ動作になる。",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActionsList(cmd, all, statusFlag)
		},
	}

	// list と show の両方から使えるよう永続フラグにする。
	cmd.PersistentFlags().BoolVar(&all, "all", false, "全状態の改善提案を表示する（既定: open のみ）")
	cmd.PersistentFlags().StringVar(&statusFlag, "status", "", "状態で絞り込む (open|done|dropped|expired)。--all と併用不可")

	cmd.AddCommand(newActionsListCommand(&all, &statusFlag))
	cmd.AddCommand(newActionsShowCommand())
	cmd.AddCommand(newActionsDropCommand())
	cmd.AddCommand(newActionsReopenCommand())

	return cmd
}

// newActionsListCommand は `insights actions list` を組み立てる。
// --all / --status は親コマンド（actions）の永続フラグをそのまま参照する。
func newActionsListCommand(all *bool, statusFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "改善提案の一覧を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActionsList(cmd, *all, *statusFlag)
		},
	}
}

// newActionsShowCommand は `insights actions show <ID>` を組み立てる。
func newActionsShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <ID>",
		Short: "改善提案 1 件の全文を表示する",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActionsShow(cmd, args[0])
		},
	}
}

// runActionsList は list サブコマンド（および引数なし actions）の本体。
func runActionsList(cmd *cobra.Command, all bool, statusFlag string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	if all && statusFlag != "" {
		return fmt.Errorf("--all と --status は同時に指定できません")
	}
	if statusFlag != "" && !isValidActionStatus(statusFlag) {
		return fmt.Errorf("--status の値が不正です（open|done|dropped|expired のいずれか）: %q", statusFlag)
	}

	cfg, err := ConfigFromContext(cmd)
	if err != nil {
		return err
	}

	db, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	allActions, err := db.AllActions()
	if err != nil {
		return fmt.Errorf("改善提案の取得に失敗しました: %w", err)
	}

	var shown []model.Action
	switch {
	case all:
		shown = allActions
	case statusFlag != "":
		for _, a := range allActions {
			if string(a.Status) == statusFlag {
				shown = append(shown, a)
			}
		}
	default:
		for _, a := range allActions {
			if a.Status == model.ActionOpen {
				shown = append(shown, a)
			}
		}
	}

	result := actionsListResult{
		Filter:        actionsFilterLabel(all, statusFlag),
		Total:         len(allActions),
		Shown:         len(shown),
		StatusSummary: summarizeActionStatus(allActions),
		Actions:       toActionViews(shown),
	}

	return PrintResult(cmd, func(w io.Writer) error {
		return renderActionsListHuman(w, result)
	}, result)
}

// runActionsShow は show サブコマンドの本体。
func runActionsShow(cmd *cobra.Command, idArg string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	id, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil {
		return fmt.Errorf("ID は整数で指定してください: %q", idArg)
	}

	cfg, err := ConfigFromContext(cmd)
	if err != nil {
		return err
	}

	db, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	allActions, err := db.AllActions()
	if err != nil {
		return fmt.Errorf("改善提案の取得に失敗しました: %w", err)
	}

	var found *model.Action
	for i := range allActions {
		if allActions[i].ID == id {
			found = &allActions[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("ID %d の改善提案は見つかりません（`insights actions list --all` で一覧を確認してください）", id)
	}

	result := actionDetailView{
		ID: found.ID, CreatedOn: found.CreatedOn, Title: found.Title,
		Detail: found.Detail, Category: found.Category,
		Status: string(found.Status), Verdict: found.Verdict, VerifiedOn: found.VerifiedOn,
	}

	return PrintResult(cmd, func(w io.Writer) error {
		return renderActionDetailHuman(w, result)
	}, result)
}

// isValidActionStatus は s が既知の ActionStatus かどうかを判定する。
func isValidActionStatus(s string) bool {
	switch model.ActionStatus(s) {
	case model.ActionOpen, model.ActionDone, model.ActionDropped, model.ActionExpired:
		return true
	default:
		return false
	}
}

// actionsFilterLabel は --json 出力の filter フィールド値を決める。
func actionsFilterLabel(all bool, statusFlag string) string {
	switch {
	case all:
		return "all"
	case statusFlag != "":
		return "status:" + statusFlag
	default:
		return "open"
	}
}

// summarizeActionStatus は状態別の件数を固定順序（open/done/dropped/expired）で返す。
// 「提案は増え続けているが done が増えていない」状態が一目で分かるよう、
// このサマリは list の出力に必ず併記する。
func summarizeActionStatus(actions []model.Action) []actionStatusCount {
	counts := make(map[model.ActionStatus]int, len(actionStatusOrder))
	for _, a := range actions {
		counts[a.Status]++
	}
	out := make([]actionStatusCount, 0, len(actionStatusOrder))
	for _, st := range actionStatusOrder {
		out = append(out, actionStatusCount{Status: string(st), Count: counts[st]})
	}
	return out
}

// isProposalStuck は open が積み上がっていて done が追いついていない状態を検出する。
// 振り返りが実行に結びついていないことの最重要サインなので、閾値を超えたら警告を出す。
func isProposalStuck(summary []actionStatusCount) bool {
	var open, done int
	for _, c := range summary {
		switch model.ActionStatus(c.Status) {
		case model.ActionOpen:
			open = c.Count
		case model.ActionDone:
			done = c.Count
		}
	}
	return open >= 3 && open > done
}

func toActionViews(actions []model.Action) []actionView {
	out := make([]actionView, 0, len(actions))
	for _, a := range actions {
		out = append(out, toActionView(a))
	}
	return out
}

// toActionView は 1 件分を一覧表示用のビューに変換する。
func toActionView(a model.Action) actionView {
	return actionView{
		ID: a.ID, CreatedOn: a.CreatedOn, Title: a.Title,
		Status: string(a.Status), Verdict: a.Verdict, VerifiedOn: a.VerifiedOn,
	}
}

// renderActionsListHuman は actionsListResult を人間向けに整形して w に書き出す。
func renderActionsListHuman(w io.Writer, r actionsListResult) error {
	fmt.Fprintln(w, "=== insights actions ===")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "状態別件数:")
	for _, c := range r.StatusSummary {
		fmt.Fprintf(w, "  %-18s %d 件\n", actionStatusJP(c.Status), c.Count)
	}
	if isProposalStuck(r.StatusSummary) {
		fmt.Fprintln(w, "  警告: open が積み上がっており done が増えていません。振り返りが実行に結びついていない可能性があります。")
	}
	fmt.Fprintln(w)

	if r.Total == 0 {
		fmt.Fprintln(w, "まだ改善提案がありません。")
		return nil
	}
	if len(r.Actions) == 0 {
		fmt.Fprintf(w, "条件（%s）に一致する改善提案がありません（全 %d 件）。\n", r.Filter, r.Total)
		return nil
	}

	fmt.Fprintf(w, "一覧（フィルタ: %s、%d/%d 件）:\n", r.Filter, r.Shown, r.Total)
	for _, a := range r.Actions {
		fmt.Fprintf(w, "  [%d] %s  提案日:%s  状態:%s\n", a.ID, a.Title, orDash(a.CreatedOn), actionStatusJP(a.Status))
		if a.Verdict != "" {
			fmt.Fprintf(w, "      検証所見: %s\n", truncateRunes(a.Verdict, actionsListVerdictMaxRunes))
		}
	}
	return nil
}

// renderActionDetailHuman は actionDetailView を人間向けに整形して w に書き出す。
func renderActionDetailHuman(w io.Writer, r actionDetailView) error {
	fmt.Fprintf(w, "=== 改善提案 #%d ===\n\n", r.ID)
	fmt.Fprintf(w, "タイトル: %s\n", orDash(r.Title))
	fmt.Fprintf(w, "カテゴリ: %s\n", orDash(r.Category))
	fmt.Fprintf(w, "状態: %s\n", actionStatusJP(r.Status))
	fmt.Fprintf(w, "提案日: %s\n", orDash(r.CreatedOn))
	fmt.Fprintf(w, "検証日: %s\n", orDash(r.VerifiedOn))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "詳細:")
	fmt.Fprintln(w, orDash(r.Detail))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "検証所見:")
	fmt.Fprintln(w, orDash(r.Verdict))
	return nil
}

// orDash は空文字列を "-" に変換する（値が無いことを明示するため、空欄のままにしない）。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateRunes は s を max rune 以内に切り詰め、切り詰めた場合は "…" を付ける。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// --- 状態の手動変更（drop / reopen） ---

// actionsUpdateResult は drop / reopen の実行結果。
type actionsUpdateResult struct {
	Action    string       `json:"action"` // "drop" | "reopen"
	Changed   int          `json:"changed"`
	Unchanged int          `json:"unchanged"` // 既にその状態だったもの
	Actions   []actionView `json:"actions"`
}

// newActionsDropCommand は `insights actions drop <ID>...` を組み立てる。
//
// 振り返りが出した提案には、的外れなもの・重複したものが混じる。AI に検証させ続けても
// 決着しないので、要らないものは人が畳めないと未決着の一覧が膨らむ一方になる。
func newActionsDropCommand() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "drop <ID>...",
		Short: "改善提案を見送り(dropped)にする",
		Long: "改善提案を見送り(dropped)にする。複数の ID をまとめて指定できる。\n" +
			"見送りにしたものは、以降の振り返りで検証対象にならない。\n" +
			"間違えたときは `insights actions reopen <ID>` で未着手に戻せる。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActionsUpdate(cmd, args, model.ActionDropped, reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "見送りの理由（記録として残る）")

	return cmd
}

// newActionsReopenCommand は `insights actions reopen <ID>...` を組み立てる。
// drop の取り消し用。取り消せないと、ID を打ち間違えた時点で CLI からは戻せなくなる。
func newActionsReopenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <ID>...",
		Short: "改善提案を未着手(open)に戻す",
		Long: "改善提案を未着手(open)に戻す。複数の ID をまとめて指定できる。\n" +
			"検証結果（verdict / 検証日）は消える。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActionsUpdate(cmd, args, model.ActionOpen, "")
		},
	}
}

// runActionsUpdate は drop / reopen の共通本体。
//
// 1 件でも存在しない ID があれば、何も変更せずにエラーにする。まとめて指定できる以上、
// 途中まで適用された状態で失敗すると、どこまで通ったのかが分からなくなるため。
func runActionsUpdate(cmd *cobra.Command, idArgs []string, status model.ActionStatus, reason string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	ids := make([]int64, 0, len(idArgs))
	for _, arg := range idArgs {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("ID は整数で指定してください: %q", arg)
		}
		ids = append(ids, id)
	}

	cfg, err := ConfigFromContext(cmd)
	if err != nil {
		return err
	}

	db, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	allActions, err := db.AllActions()
	if err != nil {
		return fmt.Errorf("改善提案の取得に失敗しました: %w", err)
	}
	byID := make(map[int64]model.Action, len(allActions))
	for _, a := range allActions {
		byID[a.ID] = a
	}

	targets := make([]model.Action, 0, len(ids))
	for _, id := range ids {
		a, ok := byID[id]
		if !ok {
			return fmt.Errorf("ID %d の改善提案は見つかりません（`insights actions list --all` で一覧を確認してください）", id)
		}
		targets = append(targets, a)
	}

	verdict := ""
	if status == model.ActionDropped {
		verdict = manualDropVerdict(reason)
	}

	result := actionsUpdateResult{Action: actionUpdateLabel(status)}
	for _, a := range targets {
		if a.Status == status {
			result.Unchanged++
			result.Actions = append(result.Actions, toActionView(a))
			continue
		}
		// 検証日（verified_on）は空にする。ここは「振り返りがその日に検証した」ことを表す
		// 列で、同じ日の daily を回し直したときに検証対象へ戻す判定にも使われている
		// （store.ActionsForVerification）。手で畳んだものに日付を入れると、その日の
		// 振り返りが検証対象として拾い直してしまう。畳んだ事実は verdict に残す。
		if err := db.UpdateActionStatus(a.ID, status, verdict, ""); err != nil {
			return fmt.Errorf("改善提案の更新に失敗しました: %w", err)
		}
		a.Status = status
		a.Verdict = verdict
		a.VerifiedOn = ""
		result.Changed++
		result.Actions = append(result.Actions, toActionView(a))
	}

	return PrintResult(cmd, func(w io.Writer) error {
		return renderActionsUpdateHuman(w, result)
	}, result)
}

// manualDropVerdict は手動で見送りにしたことを示す verdict 文字列を作る。
// 検証日を使わない代わりに、いつ畳んだのかをここに残す。
func manualDropVerdict(reason string) string {
	base := fmt.Sprintf("手動で見送り（%s）", time.Now().Local().Format(dayLayout))
	if strings.TrimSpace(reason) == "" {
		return base
	}
	return base + ": " + strings.TrimSpace(reason)
}

// actionUpdateLabel は結果表示・JSON 出力に使う操作名を返す。
func actionUpdateLabel(status model.ActionStatus) string {
	if status == model.ActionDropped {
		return "drop"
	}
	return "reopen"
}

// renderActionsUpdateHuman は drop / reopen の結果を人間向けに整形する。
func renderActionsUpdateHuman(w io.Writer, r actionsUpdateResult) error {
	fmt.Fprintf(w, "=== insights actions %s ===\n\n", r.Action)
	for _, a := range r.Actions {
		fmt.Fprintf(w, "  #%d [%s] %s\n", a.ID, actionStatusJP(a.Status), a.Title)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "変更: %d 件\n", r.Changed)
	if r.Unchanged > 0 {
		fmt.Fprintf(w, "変更なし（既にその状態）: %d 件\n", r.Unchanged)
	}
	return nil
}
