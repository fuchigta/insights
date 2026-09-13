// Package rangespec は範囲モードでの検査起動を、検査ごとの granularity に応じて計画する。
package rangespec

import (
	"fmt"

	"github.com/fuchigta/spotter/internal/check"
	"github.com/fuchigta/spotter/internal/gitutil"
)

// Invocation は 1 回の検査起動に必要な情報。
type Invocation struct {
	// Source はこの起動で検査が参照する変更内容。
	Source check.Source
	// Message は免除判定（トレーラ検出）に使うコミットメッセージ。
	Message string
	// Label は失敗時の表示に使う「（<短い sha> <件名> までの範囲）」のようなラベル。
	Label string
}

// Plan は rangeExpr（"a..b" や "-1 HEAD" のような git の範囲式）を granularity に応じた
// Invocation 列に展開する。対象コミットが無ければ空スライスを返す。
func Plan(repo *gitutil.Repo, rangeExpr string, granularity check.Granularity) ([]Invocation, error) {
	commits, err := repo.RevListNoMerges(rangeExpr)
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, nil
	}

	switch granularity {
	case check.GranularitySquashed:
		newest := commits[0]
		oldest := commits[len(commits)-1]

		from, err := repo.ParentOrEmptyTree(oldest)
		if err != nil {
			return nil, err
		}
		message, err := repo.RangeMessagesBody(rangeExpr)
		if err != nil {
			return nil, err
		}
		label, err := repo.CommitLabel(newest)
		if err != nil {
			return nil, err
		}

		return []Invocation{{
			Source:  repo.RangeSource(from, newest),
			Message: message,
			Label:   fmt.Sprintf("（%s までの範囲）", label),
		}}, nil

	case check.GranularityPerCommit:
		invocations := make([]Invocation, 0, len(commits))
		for _, sha := range commits {
			from, err := repo.ParentOrEmptyTree(sha)
			if err != nil {
				return nil, err
			}
			message, err := repo.CommitMessageBody(sha)
			if err != nil {
				return nil, err
			}
			label, err := repo.CommitLabel(sha)
			if err != nil {
				return nil, err
			}
			invocations = append(invocations, Invocation{
				Source:  repo.RangeSource(from, sha),
				Message: message,
				Label:   fmt.Sprintf("（%s）", label),
			})
		}
		return invocations, nil

	default:
		return nil, fmt.Errorf("rangespec: 未対応の granularity %q です", granularity)
	}
}
