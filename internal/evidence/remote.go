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

// isGitHubHost は host が github.com（GitHub.com）かを判定する。
func isGitHubHost(host string) bool {
	return strings.EqualFold(host, "github.com")
}

// isGitLabHost は host が gitlab.com（GitLab.com）かを判定する。
func isGitLabHost(host string) bool {
	return strings.EqualFold(host, "gitlab.com")
}
