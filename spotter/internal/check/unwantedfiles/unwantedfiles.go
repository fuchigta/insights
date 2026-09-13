// Package unwantedfiles はコミットしてはいけないものの混入を防ぐ検査
// （scripts/check-unwanted-files.sh 相当）を実装する。
package unwantedfiles

import (
	"fmt"
	"regexp"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/config"
	"github.com/fuchigta/spotter/internal/globmatch"
)

type rule struct {
	paths  *regexp.Regexp
	reason string
}

// Check は unwanted-files 検査の 1 インスタンス。
type Check struct {
	rules    []rule
	maxBytes int64
}

// New は config.CheckConfig から Check を組み立てる。
func New(cc config.CheckConfig) (*Check, error) {
	c := &Check{maxBytes: cc.MaxBytes}
	for _, d := range cc.Deny {
		if d.Paths == "" || d.Reason == "" {
			return nil, fmt.Errorf("unwantedfiles: deny には paths と reason の両方が必要です")
		}
		re, err := globmatch.Compile(d.Paths)
		if err != nil {
			return nil, fmt.Errorf("unwantedfiles: deny: %w", err)
		}
		c.rules = append(c.rules, rule{paths: re, reason: d.Reason})
	}
	return c, nil
}

// Granularity はコミットごとに 1 回ずつ見る（後から消しても履歴に残るため、検査もその
// 単位で行う）。checks 側からは上書きできない。
func (c *Check) Granularity() check.Granularity {
	return check.GranularityPerCommit
}

// Run は ctx.Source の変更ファイルを拒否ルール・サイズ上限と突き合わせる。
func (c *Check) Run(ctx check.Context) ([]check.Violation, error) {
	src := ctx.Source
	changed, err := src.ChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("unwantedfiles: 変更ファイルの取得に失敗しました: %w", err)
	}

	var files []string
	reasons := map[string]string{}

	for _, f := range changed {
		reason := ""
		for _, r := range c.rules {
			if r.paths.MatchString(f) {
				reason = r.reason
				break
			}
		}

		if reason == "" && c.maxBytes > 0 {
			size, err := src.BlobSize(f)
			if err != nil {
				return nil, fmt.Errorf("unwantedfiles: %s のサイズ取得に失敗しました: %w", f, err)
			}
			if size > c.maxBytes {
				reason = fmt.Sprintf("%d バイト（上限 %d バイト）", size, c.maxBytes)
			}
		}

		if reason == "" {
			continue
		}
		files = append(files, f)
		reasons[f] = reason
	}

	if len(files) == 0 {
		return nil, nil
	}

	violations := make([]check.Violation, 0, len(files))
	for _, f := range files {
		violations = append(violations, check.Violation{
			Summary: fmt.Sprintf("%s: %s", f, reasons[f]),
		})
	}
	return violations, nil
}
