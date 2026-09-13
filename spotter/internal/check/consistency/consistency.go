// Package consistency は「複数ファイルから抽出した集合が一致するか」を検証する汎用検査
// （scripts/check-commit-types.sh の一般化）を実装する。
//
// 元のスクリプトは cliff.toml / scripts/check-commit-subject.sh / CLAUDE.md の 3 箇所から
// それぞれ違う抽出規則で Conventional Commits の type 集合を取り出し、突き合わせていた。
// 「ファイル＋抽出規則の集合を突き合わせる」という仕組み自体はコミット type に限らず
// 汎用なので、抽出規則を設定（sources）に外出しした型として実装する
// （docs/hooks-extraction.md §6 の「一般化できるかを見てから決める」を受けたもの）。
//
// 現在の worktree の中身を見るだけで、git の差分にも commit-msg にも関わらないため
// check.GranularityWorktree を使う（doc-paths と同じ理由）。
package consistency

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/config"
)

type source struct {
	file    string
	line    *regexp.Regexp // nil なら全行を対象にする
	extract *regexp.Regexp
	split   string
}

// Check は consistency 検査の 1 インスタンス。
type Check struct {
	sources []source
}

// New は config.CheckConfig から Check を組み立てる。
func New(cc config.CheckConfig) (*Check, error) {
	if len(cc.Sources) < 2 {
		return nil, fmt.Errorf("consistency: sources は 2 つ以上必要です")
	}

	sources := make([]source, 0, len(cc.Sources))
	for _, s := range cc.Sources {
		if s.File == "" {
			return nil, fmt.Errorf("consistency: sources には file が必要です")
		}
		if s.Extract == "" {
			return nil, fmt.Errorf("consistency: %s: extract が必要です", s.File)
		}

		var lineRe *regexp.Regexp
		if s.Line != "" {
			re, err := regexp.Compile(s.Line)
			if err != nil {
				return nil, fmt.Errorf("consistency: %s: line のコンパイルに失敗しました: %w", s.File, err)
			}
			lineRe = re
		}

		extractRe, err := regexp.Compile(s.Extract)
		if err != nil {
			return nil, fmt.Errorf("consistency: %s: extract のコンパイルに失敗しました: %w", s.File, err)
		}
		if extractRe.NumSubexp() < 1 {
			return nil, fmt.Errorf("consistency: %s: extract にはキャプチャグループが 1 つ必要です", s.File)
		}

		sources = append(sources, source{file: s.File, line: lineRe, extract: extractRe, split: s.Split})
	}

	return &Check{sources: sources}, nil
}

// Granularity は現在の作業ツリーを 1 回だけ見る。checks 側からは上書きできない。
func (c *Check) Granularity() check.Granularity {
	return check.GranularityWorktree
}

// Run は各 source から集合を抜き出し、全ての組み合わせで一致するかを確認する。
func (c *Check) Run(ctx check.Context) ([]check.Violation, error) {
	sets := make([]map[string]bool, len(c.sources))
	for i, s := range c.sources {
		set, err := extractSet(ctx.Root, s)
		if err != nil {
			return nil, err
		}
		if len(set) == 0 {
			return nil, fmt.Errorf("consistency: %s から 1 つも抽出できませんでした。記法が変わっていないか確認してください", s.file)
		}
		sets[i] = set
	}

	var violations []check.Violation
	for i := 0; i < len(c.sources); i++ {
		for j := i + 1; j < len(c.sources); j++ {
			onlyA := diff(sets[i], sets[j])
			onlyB := diff(sets[j], sets[i])
			if len(onlyA) == 0 && len(onlyB) == 0 {
				continue
			}

			fileA, fileB := c.sources[i].file, c.sources[j].file
			var detail []string
			if len(onlyA) > 0 {
				detail = append(detail, fmt.Sprintf("%s にあって %s に無い: %s", fileA, fileB, strings.Join(onlyA, " ")))
			}
			if len(onlyB) > 0 {
				detail = append(detail, fmt.Sprintf("%s にあって %s に無い: %s", fileB, fileA, strings.Join(onlyB, " ")))
			}

			violations = append(violations, check.Violation{
				Summary: fmt.Sprintf("%s と %s で抽出結果が一致しません:", fileA, fileB),
				Files:   detail,
			})
		}
	}

	return violations, nil
}

func extractSet(root string, s source) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, s.file))
	if err != nil {
		return nil, fmt.Errorf("consistency: %s の読み込みに失敗しました: %w", s.file, err)
	}

	set := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if s.line != nil && !s.line.MatchString(line) {
			continue
		}
		for _, m := range s.extract.FindAllStringSubmatch(line, -1) {
			val := m[1]
			if s.split == "" {
				set[val] = true
				continue
			}
			for _, tok := range strings.Split(val, s.split) {
				tok = strings.TrimSpace(tok)
				if tok != "" {
					set[tok] = true
				}
			}
		}
	}
	return set, nil
}

// diff は a にあって b に無い要素を昇順で返す。
func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
