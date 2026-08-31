package evidence

import (
	"fmt"
	"net/url"
	"strings"
)

// parseRemoteURL は git remote の URL から host と "owner/repo" 形式のスラグを
// 取り出す。対応する形式:
//
//	https://github.com/owner/repo(.git)
//	ssh://git@github.com/owner/repo(.git)
//	git@github.com:owner/repo(.git)          … scp 風構文
//
// セルフホストの GitLab はサブディレクトリ配下（https://example.com/gitlab/group/repo）
// に置かれることもあるが、その場合のパス接頭辞までは判別できない。
func parseRemoteURL(raw string) (host, slug string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("remote URL が空です")
	}

	if !strings.Contains(raw, "://") {
		// scp 風構文 (user@host:path) は net/url では解釈できないため個別に処理する。
		if at := strings.Index(raw, "@"); at >= 0 {
			rest := raw[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				host = rest[:colon]
				slug = strings.TrimPrefix(rest[colon+1:], "/")
				slug = strings.TrimSuffix(slug, ".git")
				if host != "" && slug != "" {
					return host, slug, nil
				}
			}
		}
		return "", "", fmt.Errorf("remote URL を解釈できません: %s", raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("remote URL の解析に失敗しました: %w", err)
	}
	host = u.Hostname()
	slug = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if host == "" || slug == "" {
		return "", "", fmt.Errorf("remote URL から host/slug を取り出せません: %s", raw)
	}
	return host, slug, nil
}

// forgeKind は origin のホストがどのフォージのものかを表す。
type forgeKind int

const (
	forgeUnknown forgeKind = iota
	forgeGitHub
	forgeGitLab
)

func (k forgeKind) String() string {
	switch k {
	case forgeGitHub:
		return "github"
	case forgeGitLab:
		return "gitlab"
	default:
		return "unknown"
	}
}

// githubSaaSHost / gitlabSaaSHost はそれぞれの SaaS 版のホスト名。
// セルフホストと区別する必要がある（CLI に渡すホスト指定の要否が変わる）ため
// 定数として持つ。
const (
	githubSaaSHost = "github.com"
	gitlabSaaSHost = "gitlab.com"
)

// detectForge は host がどのフォージかを判定する。判定は次の順で行う。
//
//  1. 設定の github_hosts / gitlab_hosts との完全一致。利用者が明示した以上、
//     ホスト名がどんな形でもそれに従う（GitHub Enterprise Server や
//     セルフホスト GitLab が git.example.com のような名前のことがある）。
//  2. SaaS のホスト名との完全一致。
//  3. ホスト名のラベルに "github" / "gitlab" を含むかどうかの推測。
//     gitlab.example.com / github.corp.example.jp のような素直な命名を
//     設定なしで拾うためのフォールバック。
//
// どれにも当てはまらなければ forgeUnknown を返す。この場合は設定で明示して
// もらうしかない（誤って別のフォージの CLI を叩くより、収集しない方が安全）。
func detectForge(host string, githubHosts, gitlabHosts []string) forgeKind {
	host = strings.TrimSpace(host)
	if host == "" {
		return forgeUnknown
	}

	if matchesHost(host, githubHosts) {
		return forgeGitHub
	}
	if matchesHost(host, gitlabHosts) {
		return forgeGitLab
	}

	if strings.EqualFold(host, githubSaaSHost) {
		return forgeGitHub
	}
	if strings.EqualFold(host, gitlabSaaSHost) {
		return forgeGitLab
	}

	if hostHasLabelContaining(host, "github") {
		return forgeGitHub
	}
	if hostHasLabelContaining(host, "gitlab") {
		return forgeGitLab
	}
	return forgeUnknown
}

// matchesHost は host が候補一覧のいずれかと（大文字小文字を無視して）一致するかを返す。
// 設定値には URL やポート付きの値が書かれることがあるため、正規化してから比較する。
func matchesHost(host string, candidates []string) bool {
	for _, c := range candidates {
		if normalizeHost(c) == "" {
			continue
		}
		if strings.EqualFold(normalizeHost(c), normalizeHost(host)) {
			return true
		}
	}
	return false
}

// normalizeHost は設定に書かれたホスト指定を比較用に整える。
// "https://ghe.example.com/" のような URL 形式・末尾スラッシュ・ポート番号・
// IPv6 リテラルの角括弧を落とし、parseRemoteURL が返す host と同じ形に揃える。
func normalizeHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	if strings.HasPrefix(s, "[") {
		// IPv6 リテラル "[::1]:8080"。角括弧の中だけを取り出せばポートも一緒に落ちる。
		if end := strings.Index(s, "]"); end > 0 {
			return strings.ToLower(s[1:end])
		}
	} else if i := strings.LastIndex(s, ":"); i >= 0 && strings.Count(s, ":") == 1 && isPortDigits(s[i+1:]) {
		// コロンが1つで後ろが数字のときだけポートとみなす。角括弧なしの IPv6
		// （"::1"）をポート付きと誤認しないため。
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// isPortDigits は s が 1 文字以上の数字だけから成るかを返す。
func isPortDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// hostHasLabelContaining は host のドット区切りラベルのどれかが needle を含むかを返す。
// "gitlab.example.com" は拾い、"example.com" は拾わない。
func hostHasLabelContaining(host, needle string) bool {
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if strings.Contains(label, needle) {
			return true
		}
	}
	return false
}
