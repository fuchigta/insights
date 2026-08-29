// Package evidence はセッションが「何に結実したか」を、セッションの外側にある
// 成果物テキスト（git コミット・PR/Issue/MR の本文）から集める層。AI 評価器に
// 渡す一次資料を作るのが目的で、コミット数のような「数える」ことではなく、
// コミット本文・PR 本文といった「中身」を取ってくることに主眼がある。
//
// すべて best-effort。git が無い・gh の認証が切れている・そもそもリポジトリで
// ない・ネットワークが死んでいる、といったどの失敗もエラーとして呼び出し側に
// 返さない。個別の失敗は slog.Warn に記録した上で、集められた分だけを返す。
// 呼び出し側（judge など）はこのパッケージの戻り値だけを見て評価を続行できる。
package evidence

import (
	"context"
	"log/slog"
	"os/exec"
	"time"
	"unicode/utf8"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/model"
)

// Query は「どのリポジトリの、いつからいつまでを見るか」を表す。
type Query struct {
	SessionID   string
	ProjectPath string    // セッションの cwd。ここが git リポジトリかを判定する
	GitBranch   string    // 空なら現在のブランチ
	From, To    time.Time // セッションの開始・終了時刻
}

// Collector は 1 セッション分の成果物を集める。New で作ること（ゼロ値では
// 外部コマンドの有無が解決されていないため使わない）。
type Collector struct {
	cfg config.EvidenceConfig

	gitPath  string // 空文字なら git 未検出
	ghPath   string // 空文字なら gh 未検出
	glabPath string // 空文字なら glab 未検出

	ghEnabled   bool // cfg.Gh（auto を含む）を検出結果で解決した後の値
	glabEnabled bool // cfg.Glab（auto を含む）を検出結果で解決した後の値

	// Timeout は外部コマンド1回あたりのタイムアウト。0 以下なら DefaultTimeout を使う。
	// ネットワーク越しの gh/glab が固まって全体を止めるのを防ぐためのガード。
	Timeout time.Duration

	// MaxItems は gh/glab の PR/Issue/MR 取得件数の上限。0 以下なら DefaultMaxItems を使う。
	MaxItems int
}

// New は設定から Collector を作る。ここで git/gh/glab の有無を一度だけ
// exec.LookPath で調べ、Tristate の auto を解決しておく。Collect のたびに
// LookPath し直すことはしない（毎回のプロセス起動コストとブレを避けるため）。
func New(cfg config.EvidenceConfig) *Collector {
	gitPath, _ := exec.LookPath("git")
	ghPath, ghErr := exec.LookPath("gh")
	glabPath, glabErr := exec.LookPath("glab")

	c := &Collector{
		cfg:      cfg,
		gitPath:  gitPath,
		ghPath:   ghPath,
		glabPath: glabPath,
		Timeout:  DefaultTimeout,
		MaxItems: DefaultMaxItems,
	}
	c.ghEnabled = cfg.Gh.Enabled(ghErr == nil)
	c.glabEnabled = cfg.Glab.Enabled(glabErr == nil)
	return c
}

// Available は実際に使える収集手段の一覧を返す（doctor 表示用）。
// 設定で有効になっていても対応コマンドが見つからなければ含めない。
// 例: []string{"git", "gh"}
func (c *Collector) Available() []string {
	var methods []string
	if c.cfg.Git && c.gitPath != "" {
		methods = append(methods, "git")
	}
	if c.ghEnabled && c.ghPath != "" {
		methods = append(methods, "gh")
	}
	if c.glabEnabled && c.glabPath != "" {
		methods = append(methods, "glab")
	}
	return methods
}

// Collect は 1 セッションに紐づく成果物を集める。
// エラーは返さない。個別の失敗は slog.Warn に記録し、集められた分だけ返す。
func (c *Collector) Collect(ctx context.Context, q Query) []model.Evidence {
	wantGit := c.cfg.Git
	wantForge := c.ghEnabled || c.glabEnabled
	if !wantGit && !wantForge {
		return nil
	}

	if c.gitPath == "" {
		// リポジトリ判定にも remote 判定にも git 自体が要るため、git が無ければ
		// commit 収集・gh/glab 収集のいずれも成立しない。
		slog.Warn("evidence: git コマンドが見つからないため成果物収集をスキップします", "session", q.SessionID)
		return nil
	}

	inside, err := c.isInsideWorkTree(ctx, q.ProjectPath)
	if err != nil {
		slog.Warn("evidence: git リポジトリかどうかの判定に失敗しました", "session", q.SessionID, "path", q.ProjectPath, "error", err)
		return nil
	}
	if !inside {
		slog.Warn("evidence: git ワークツリーではないため成果物収集をスキップします", "session", q.SessionID, "path", q.ProjectPath)
		return nil
	}

	var out []model.Evidence
	if wantGit {
		out = append(out, c.collectGitCommits(ctx, q)...)
	}
	if wantForge {
		out = append(out, c.collectForge(ctx, q)...)
	}

	for i := range out {
		out[i].Body = c.truncateBody(out[i].Body)
	}
	return out
}

// collectForge は origin リモートの host から gh / glab のどちらを使うかを決めて呼び分ける。
// gh と glab はサブコマンド体系が違うため、それぞれ独立した実装（collectGh / collectGlab）
// を呼ぶだけで、コマンドライン自体を共有はしない。
func (c *Collector) collectForge(ctx context.Context, q Query) []model.Evidence {
	remote, err := c.gitRemoteURL(ctx, q.ProjectPath)
	if err != nil {
		slog.Warn("evidence: origin リモートURLの取得に失敗しました", "session", q.SessionID, "path", q.ProjectPath, "error", err)
		return nil
	}

	host, slug, err := parseRemoteURL(remote)
	if err != nil {
		slog.Warn("evidence: origin リモートURLを解釈できませんでした", "session", q.SessionID, "remote", remote, "error", err)
		return nil
	}

	var out []model.Evidence
	if c.ghEnabled {
		if isGitHubHost(host) {
			out = append(out, c.collectGh(ctx, q, slug)...)
		} else {
			slog.Warn("evidence: origin が GitHub ではないため gh をスキップします", "session", q.SessionID, "host", host)
		}
	}
	if c.glabEnabled {
		if isGitLabHost(host) {
			out = append(out, c.collectGlab(ctx, q, slug)...)
		} else {
			slog.Warn("evidence: origin が GitLab ではないため glab をスキップします", "session", q.SessionID, "host", host)
		}
	}
	return out
}

// truncateBody は s を cfg.MaxBodyChars（rune 数）で切り詰める。
// MaxBodyChars が 0 以下の場合は「無制限」として扱い、切り詰めない。
// 切り詰めた場合は末尾にその旨が分かる印を付ける。
func (c *Collector) truncateBody(s string) string {
	max := c.cfg.MaxBodyChars
	if max <= 0 {
		return s
	}

	total := utf8.RuneCountInString(s)
	if total <= max {
		return s
	}

	runes := []rune(s)
	kept := string(runes[:max])
	return kept + truncatedMark(total-max)
}
