// Package cirange は CI 環境（GitHub Actions / GitLab CI）から、比較対象の
// git の範囲式を自動検出する。
//
// docs/hooks-extraction.md の「CI 側の範囲算出」節にある通り、この判定は
// どのプロジェクトでもそのままコピペされる部分で、`spotter range` として持たせると
// CI 側の記述が `spotter check --range "$(spotter range)"` まで縮む。
//
// 両プロバイダとも本質的には同じ 3 パターンに落ちる。
//
//  1. MR/PR イベント → base..head
//  2. push イベント → before..after
//  3. 判定できない・新規ブランチ等 → フォールバック "-1 HEAD"
//
// 組み込みで自動検出するのはこの 2 つに限定する。他の CI は --provider による明示も
// 用意しない（環境変数の組み合わせを増やすほど実機でしか検証できない分岐が増えるため）。
// それ以外の CI では利用者が自分で組み立てた範囲を `spotter check --range` に渡す。
package cirange

import (
	"encoding/json"
	"fmt"
	"os"
)

// Provider は自動検出（または --provider での明示）対象の CI。
type Provider string

const (
	ProviderGitHubActions Provider = "github-actions"
	ProviderGitLabCI      Provider = "gitlab-ci"
)

// fallbackRange は比較対象を判定できないときに使う範囲式。新しいブランチの最初の push や
// force push 直後は before 相当の SHA が全部ゼロ（0000...）になることがあり、
// その場合もここに落ちる（CommitExistsFunc が false を返すため）。
const fallbackRange = "-1 HEAD"

// Env は環境変数の参照を抽象化する（テストで os.Getenv を差し替えるため）。
type Env func(key string) string

// CommitExistsFunc は sha がリポジトリ内に実在するコミットかどうかを返す。
// gitutil.Repo.CommitExists がこれを満たす。
type CommitExistsFunc func(sha string) (bool, error)

// Detect は環境変数から CI プロバイダを自動判別する。判別できなければ ok=false。
func Detect(env Env) (Provider, bool) {
	if env("GITHUB_ACTIONS") == "true" {
		return ProviderGitHubActions, true
	}
	if env("GITLAB_CI") == "true" {
		return ProviderGitLabCI, true
	}
	return "", false
}

// Resolve は provider に応じた git の範囲式を計算する。
func Resolve(provider Provider, env Env, commitExists CommitExistsFunc) (string, error) {
	switch provider {
	case ProviderGitHubActions:
		return resolveGitHubActions(env, commitExists)
	case ProviderGitLabCI:
		return resolveGitLabCI(env, commitExists)
	default:
		return "", fmt.Errorf("cirange: 未対応の provider %q です（github-actions | gitlab-ci）", provider)
	}
}

func resolveGitHubActions(env Env, commitExists CommitExistsFunc) (string, error) {
	eventPath := env("GITHUB_EVENT_PATH")

	switch env("GITHUB_EVENT_NAME") {
	case "pull_request", "pull_request_target":
		base, err := readEventField(eventPath, "pull_request", "base", "sha")
		if err != nil {
			return "", err
		}
		if base == "" {
			return fallbackRange, nil
		}
		return base + "..HEAD", nil

	default:
		// push イベントの前段（比較元）は github.event.before、すなわちイベント JSON の
		// トップレベル "before" フィールド。GITHUB_EVENT_NAME が push 以外（workflow_dispatch
		// など）のときも同じフィールドを試し、無ければフォールバックに委ねる。
		before, err := readEventField(eventPath, "before")
		if err != nil {
			return "", err
		}
		return resolveBeforeAfter(before, env("GITHUB_SHA"), commitExists)
	}
}

func resolveGitLabCI(env Env, commitExists CommitExistsFunc) (string, error) {
	if env("CI_PIPELINE_SOURCE") == "merge_request_event" {
		base := env("CI_MERGE_REQUEST_DIFF_BASE_SHA")
		if base == "" {
			return fallbackRange, nil
		}
		return base + "..HEAD", nil
	}

	before := env("CI_COMMIT_BEFORE_SHA")
	return resolveBeforeAfter(before, env("CI_COMMIT_SHA"), commitExists)
}

// resolveBeforeAfter は before..after 形式の範囲を組み立てる。before が空、または
// リポジトリに実在しない（新規ブランチの初回 push・force push 直後で全ゼロになる場合を
// 含む）ならフォールバックする。
func resolveBeforeAfter(before, after string, commitExists CommitExistsFunc) (string, error) {
	if before == "" {
		return fallbackRange, nil
	}
	ok, err := commitExists(before)
	if err != nil {
		return "", fmt.Errorf("cirange: %s の実在確認に失敗しました: %w", before, err)
	}
	if !ok {
		return fallbackRange, nil
	}
	if after == "" {
		after = "HEAD"
	}
	return before + ".." + after, nil
}

// readEventField は CI が用意するイベント JSON ファイルから、keys をたどって文字列
// フィールドを読む。path が空、ファイルが無い、途中のフィールドが無い場合は空文字を返す
// （イベントの種類によって形が変わる情報なので、無ければ「無かった」として扱い、
// 呼び出し側のフォールバックに任せる）。JSON 自体の読み込み・解析に失敗した場合はエラー。
func readEventField(path string, keys ...string) (string, error) {
	if path == "" {
		return "", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("cirange: %s の読み込みに失敗しました: %w", path, err)
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("cirange: %s の解析に失敗しました: %w", path, err)
	}

	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return "", nil
		}
		v, ok = m[k]
		if !ok {
			return "", nil
		}
	}

	s, _ := v.(string)
	return s, nil
}
