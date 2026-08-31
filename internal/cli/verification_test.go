package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/pricing"
)

// actionIDRe は振り返りプロンプトに載る open_actions の action_id を拾う。
var actionIDRe = regexp.MustCompile(`"action_id":\s*(\d+)`)

// fakeVerifyingJudge は「毎回同じ提案を 1 件出し、渡された未決着提案はすべて done にする」
// 振り返りを返すフェイク。同じ日を回し直したときの積み上がりを再現するために使う。
type fakeVerifyingJudge struct {
	seenActionIDs [][]string // 振り返り呼び出しごとに、渡された action_id
	nonSession    int
}

func (f *fakeVerifyingJudge) Name() string     { return "fake-verifying-judge" }
func (f *fakeVerifyingJudge) Available() error { return nil }

func (f *fakeVerifyingJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	if strings.Contains(req.Prompt, "## セッション基本情報") {
		return validEvalJSON("achieved"), nil
	}

	f.nonSession++
	if f.nonSession%2 == 1 {
		return validDailyJSON(), nil
	}

	// 振り返り。渡された未決着提案をすべて done にし、毎回同じタイトルの提案を出す。
	var ids []string
	var verifications []string
	for _, m := range actionIDRe.FindAllStringSubmatch(req.Prompt, -1) {
		ids = append(ids, m[1])
		verifications = append(verifications, fmt.Sprintf(
			`{"action_id":%s,"title":"検証","status":"done","verdict":"実行された"}`, m[1]))
	}
	f.seenActionIDs = append(f.seenActionIDs, ids)

	return json.RawMessage(fmt.Sprintf(`{
		"headline": "テスト",
		"verdict": "mixed",
		"body": "テスト用の振り返り本文。",
		"cost_observation": "コストは概ね妥当だった。",
		"proposals": [{"title":"同じ提案","detail":"毎回同じことを提案する","category":"process"}],
		"verifications": [%s],
		"outliers": [],
		"project_reviews": {}
	}`, strings.Join(verifications, ","))), nil
}

// 同じ日の daily を回し直しても、その日に出した提案が検証対象にならないこと。
// 当日の提案を当日に検証させると、閉じられたぶんタイトル重複の判定から外れ、
// 同じ提案が作り直されて日ごとに積み上がっていく。
func TestDaily_SameDayRerunDoesNotVerifyTodaysProposals(t *testing.T) {
	db := newTempDB(t)
	cfg := config.Default()
	cfg.Output.Dir = t.TempDir()
	date := "2026-08-21"
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

	saveTestSession(t, db, testSessionSpec{
		SessionID: "sess-1", ProjectPath: "/proj-a", FirstPrompt: "fix bug",
		StartedAt: base, EndedAt: base.Add(5 * time.Minute), CostUSD: 0.03, CostKnown: true,
	})

	// 前日までに出ていた未決着の提案。これは検証対象に入るのが正しい。
	pastID, err := db.CreateAction(&model.Action{
		CreatedOn: "2026-08-20", Title: "過去の提案", Detail: "前日に出したもの", Category: "process",
	})
	if err != nil {
		t.Fatalf("CreateAction() error = %v", err)
	}

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange() error = %v", err)
	}
	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("SessionsInRange() error = %v", err)
	}
	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load() error = %v", err)
	}

	fj := &fakeVerifyingJudge{}
	cmd := newDailyTestCmd(t)

	for i := 1; i <= 2; i++ {
		if _, err := runDaily(context.Background(), cmd, cfg, db, prices, fj, rows, 0, date, false, true); err != nil {
			t.Fatalf("runDaily() %d回目 error = %v", i, err)
		}
	}

	if len(fj.seenActionIDs) != 2 {
		t.Fatalf("振り返りの呼び出し回数 = %d, want 2", len(fj.seenActionIDs))
	}

	// 1 回目・2 回目とも、渡るのは前日までの提案だけ。
	for i, ids := range fj.seenActionIDs {
		want := fmt.Sprintf("%d", pastID)
		if len(ids) != 1 || ids[0] != want {
			t.Errorf("%d回目に渡された action_id = %v, want [%s]（当日の提案は渡さない）", i+1, ids, want)
		}
	}

	// 当日の提案は 1 件だけ。閉じられて作り直される積み上がりが起きていないこと。
	all, err := db.AllActions()
	if err != nil {
		t.Fatalf("AllActions() error = %v", err)
	}
	todays := 0
	for _, a := range all {
		if a.CreatedOn == date {
			todays++
			if a.Status != model.ActionOpen {
				t.Errorf("当日の提案が %v になっている（当日に検証されている）", a.Status)
			}
		}
	}
	if todays != 1 {
		t.Errorf("当日に作られた提案 = %d 件, want 1（回し直しで積み上がっている）", todays)
	}
}
