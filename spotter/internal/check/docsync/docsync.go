// Package docsync はドキュメントの陳腐化を防ぐ検査（scripts/check-doc-sync.sh 相当）を実装する。
package docsync

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/globmatch"
)

type pair struct {
	pathsLabel string
	paths      *regexp.Regexp
	doc        string
	when       *regexp.Regexp
}

// Check は doc-sync 検査の 1 インスタンス。
type Check struct {
	pairs   []pair
	exclude []*regexp.Regexp
}

// New は config.CheckConfig から Check を組み立てる。パターンはここで事前コンパイルし、
// 不正な設定は起動時に検出する。
func New(cc config.CheckConfig) (*Check, error) {
	c := &Check{}

	for _, pat := range cc.Exclude {
		re, err := globmatch.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("docsync: exclude: %w", err)
		}
		c.exclude = append(c.exclude, re)
	}

	for _, p := range cc.Pairs {
		if p.Paths == "" || p.Doc == "" {
			return nil, fmt.Errorf("docsync: pairs には paths と doc の両方が必要です")
		}
		paths, err := globmatch.Compile(p.Paths)
		if err != nil {
			return nil, fmt.Errorf("docsync: pairs: %w", err)
		}
		var when *regexp.Regexp
		if p.When != "" {
			re, err := regexp.Compile(p.When)
			if err != nil {
				return nil, fmt.Errorf("docsync: when %q のコンパイルに失敗しました: %w", p.When, err)
			}
			when = re
		}
		c.pairs = append(c.pairs, pair{pathsLabel: p.Paths, paths: paths, doc: p.Doc, when: when})
	}
	return c, nil
}

// Granularity は範囲全体をまとめて 1 回で見る（後からドキュメントを直すコミットを
// 足せば通るようにするため）。checks 側からは上書きできない。
func (c *Check) Granularity() check.Granularity {
	return check.GranularitySquashed
}

// Run は ctx.Source の変更内容を対応表と突き合わせ、コードだけが変更されドキュメントが
// 一緒に変更されていない組を違反として返す。
func (c *Check) Run(ctx check.Context) ([]check.Violation, error) {
	src := ctx.Source
	changed, err := src.ChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("docsync: 変更ファイルの取得に失敗しました: %w", err)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	changedSet := make(map[string]bool, len(changed))
	for _, f := range changed {
		changedSet[f] = true
	}

	var violations []check.Violation
	for _, p := range c.pairs {
		if changedSet[p.doc] {
			// ドキュメント側も一緒に入っているなら、この行は満たされている。
			continue
		}

		var hits []string
		for _, f := range changed {
			if isExcluded(f, c.exclude) {
				continue
			}
			if !p.paths.MatchString(f) {
				continue
			}
			if p.when != nil {
				diff, err := src.DiffLines(f)
				if err != nil {
					return nil, fmt.Errorf("docsync: %s の差分取得に失敗しました: %w", f, err)
				}
				if !p.when.MatchString(diff) {
					continue
				}
			}
			hits = append(hits, f)
		}

		if len(hits) > 0 {
			violations = append(violations, check.Violation{
				Summary: fmt.Sprintf("%s を変更していますが、%s が一緒に入っていません:", p.pathsLabel, p.doc),
				Files:   hits,
			})
		}
	}

	return violations, nil
}

// isExcluded は *_test.go（テストは利用者に見える面を定義しないため常に対象外）と、
// checks 側で追加指定された exclude パターンに一致するかを判定する。
func isExcluded(f string, extra []*regexp.Regexp) bool {
	if strings.HasSuffix(f, "_test.go") {
		return true
	}
	for _, re := range extra {
		if re.MatchString(f) {
			return true
		}
	}
	return false
}
