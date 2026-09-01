// Package rollup の改善提案永続化ヘルパ。Retro が生成した新規提案・検証結果を
// store の actions テーブルへ反映する。
package rollup

import (
	"fmt"
	"strings"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/store"
)

// PersistRetro は Retro の提案を actions テーブルに登録し、検証結果を反映する。
//
// 同じ日を回し直したときは、前回の実行が作った提案のうち今回出てこなかったものを消す。
// 同じ日を何度回しても、その日の提案として残るのは最後の実行の分だけになる。
//
// r が nil なら何もしない。
func PersistRetro(db *store.DB, date string, r *Retro) error {
	if db == nil {
		return fmt.Errorf("PersistRetro: db が nil")
	}
	if r == nil {
		return nil
	}

	if err := persistProposals(db, date, r.Proposals); err != nil {
		return err
	}
	if err := persistVerifications(db, date, r.Verifications); err != nil {
		return err
	}
	return nil
}

// persistProposals は新しい提案を登録する。既存の open な提案とタイトルが実質同じものは
// 新規作成しない（同じ提案が毎日重複登録されるのを防ぐ）。
//
// 重複判定は normalizeTitle で正規化した文字列の完全一致で行う。意味的な重複検出（言い換えの
// 検出）まではしないが、AI が同じ提案を毎日ほぼ同じ言い回しで出してくる典型ケースには十分。
//
// 登録の前に、同じ日の前回実行が作った提案のうち今回出てこなかったものを消す（pruneSameDayProposals）。
// 提案は毎回 AI が出し直すもので、回し直すたびに積み上がると「その日の提案」が
// 実行のたびの和集合になってしまうため。
func persistProposals(db *store.DB, date string, proposals []Proposal) error {
	keep := make(map[string]struct{}, len(proposals))
	for _, p := range proposals {
		if key := normalizeTitle(p.Title); key != "" {
			keep[key] = struct{}{}
		}
	}
	if err := pruneSameDayProposals(db, date, keep); err != nil {
		return err
	}
	if len(proposals) == 0 {
		return nil
	}

	openActions, err := db.ActionsByStatus(model.ActionOpen)
	if err != nil {
		return fmt.Errorf("既存の open な提案取得に失敗: %w", err)
	}

	existing := make(map[string]struct{}, len(openActions))
	for _, a := range openActions {
		existing[normalizeTitle(a.Title)] = struct{}{}
	}

	for _, p := range proposals {
		key := normalizeTitle(p.Title)
		if key == "" {
			// タイトルが空の提案は識別できないので登録しない。
			continue
		}
		if _, dup := existing[key]; dup {
			continue
		}

		a := &model.Action{
			CreatedOn: date,
			Title:     p.Title,
			Detail:    p.Detail,
			Category:  p.Category,
			Status:    model.ActionOpen,
		}
		if _, err := db.CreateAction(a); err != nil {
			return fmt.Errorf("提案 %q の登録に失敗: %w", p.Title, err)
		}
		// 同じ振り返り内で類似タイトルの提案が複数あっても二重登録しない。
		existing[key] = struct{}{}
	}
	return nil
}

// pruneSameDayProposals は date に作られた提案のうち、今回の振り返りに出てこなかったものを削除する。
//
// 同じ日を何度回しても最後の実行の提案だけが残るようにするための掃除。keep には今回の提案の
// 正規化タイトルを渡し、そこに無いものだけを消す（同じ提案が出続けた場合は ID を保つ）。
//
// 消すのは status が open かつ未検証（verified_on が空）のものに限る。手で畳んだもの
// （drop / reopen）や、後の日の振り返りが検証したものは、その日の実行結果ではなくその後の
// 判断なので、回し直しで消してはいけない。
func pruneSameDayProposals(db *store.DB, date string, keep map[string]struct{}) error {
	sameDay, err := db.ActionsCreatedOn(date)
	if err != nil {
		return fmt.Errorf("同日に作られた提案の取得に失敗: %w", err)
	}
	for _, a := range sameDay {
		if a.Status != model.ActionOpen || a.VerifiedOn != "" {
			continue
		}
		if _, ok := keep[normalizeTitle(a.Title)]; ok {
			continue
		}
		if err := db.DeleteAction(a.ID); err != nil {
			return fmt.Errorf("前回実行の提案 %q の削除に失敗: %w", a.Title, err)
		}
	}
	return nil
}

// persistVerifications は過去提案の検証結果を actions テーブルへ反映する。
// status が model.ActionStatus の既知の値でない場合、その 1 件だけを無視して処理を続ける
// （AI の出力揺れで振り返り全体の保存が失敗しないようにするため）。
func persistVerifications(db *store.DB, date string, verifications []ActionVerdict) error {
	for _, v := range verifications {
		status := model.ActionStatus(v.Status)
		switch status {
		case model.ActionOpen, model.ActionDone, model.ActionDropped, model.ActionExpired:
		default:
			continue
		}
		if err := db.UpdateActionStatus(v.ActionID, status, v.Verdict, date); err != nil {
			return fmt.Errorf("action(id=%d) の検証結果反映に失敗: %w", v.ActionID, err)
		}
	}
	return nil
}

// normalizeTitle は提案タイトルの重複判定用に正規化する。
// 前後の空白を除去し、連続する空白を 1 個に圧縮したうえで小文字化する。
// 大文字小文字や空白の数といった表記ゆれだけを吸収する単純な正規化であり、
// 言い換えによる意味的重複までは検出しない。
func normalizeTitle(s string) string {
	fields := strings.Fields(strings.ToLower(s))
	return strings.Join(fields, " ")
}
