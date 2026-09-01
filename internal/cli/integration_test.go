// このファイルは issue #1「コマンド層を通した統合テストが無い」への対応。
// これまでのテストはパッケージ単位（source/claudecode, judge, rollup, render, store...）に
// 閉じており、実際に見つかった不具合（サブエージェントのログ配置、usage の多重計上、
// パス比較の OS 依存、埋め込みアセットの改行）はいずれもパッケージの継ぎ目で起きていた。
// ここでは実際の cobra コマンドを ingest -> judge -> daily -> report -> actions list の
// 順に通しで実行し、各段の受け渡し（継ぎ目）にアサーションを置く。
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/prompts"
	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/store"
)

// integrationJudge は本テスト専用の judge.Judge フェイク実装。claude サブプロセスは
// 一切呼ばない。リクエストの中身（Prompt / Schema）で「セッション評価」「日報生成」
// 「振り返り生成」を判別して応答を出し分け、呼び出し回数とセッション評価プロンプトの
// 本文を記録する。
//
//   - sessionPrompts: 親セッションの評価プロンプト（"## セッション基本情報" を含むもの）。
//     サブエージェントの要約がここに畳み込まれているか、judge -> daily 間でキャッシュが
//     効いて再評価されていないかを確認するために使う。
//   - dailyCalls / retroCalls: 日報・振り返り生成それぞれの呼び出し回数。
//     dailySchema/retroSchema の title（"DailyNarrative" / "Retro"）で判別する
//     （呼び出し順序に依存させると、rollup.Synthesize の内部実装が変わったときに
//     静かに壊れるため）。
type integrationJudge struct {
	mu             sync.Mutex
	sessionPrompts []string
	dailyCalls     int
	retroCalls     int
}

func (f *integrationJudge) Name() string     { return "integration-fake-judge" }
func (f *integrationJudge) Available() error { return nil }

func (f *integrationJudge) Evaluate(_ context.Context, req judge.Request) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case strings.Contains(req.Prompt, "## セッション基本情報"):
		f.sessionPrompts = append(f.sessionPrompts, req.Prompt)
		return validEvalJSON("achieved"), nil
	case bytes.Contains(req.Schema, []byte("DailyNarrative")):
		f.dailyCalls++
		return validDailyJSON(), nil
	default:
		f.retroCalls++
		return integrationRetroJSON(), nil
	}
}

// integrationRetroJSON は eval_testutil_test.go の validRetroJSON をベースに、
// 改善提案を 1 件だけ加えたもの。validRetroJSON は proposals が空で、そのままでは
// actions list まで検証できない（登録される提案が無い）ため、この統合テスト専用に
// ここだけ差し替える。
func integrationRetroJSON() json.RawMessage {
	return json.RawMessage(`{
		"body": "テスト用の振り返り本文。",
		"cost_observation": "コストは概ね妥当だった。",
		"proposals": [
			{"title": "統合テスト用の改善提案", "detail": "後から実行有無を確認できる具体的な内容。", "category": "process"}
		],
		"verifications": [],
		"outliers": []
	}`)
}

// writeIntegrationSubagentSession は Claude Code のサブエージェントログ
// （<slug>/<parent-uuid>/subagents/agent-xxx.jsonl と隣接する .meta.json）を書き出す。
//
// internal/source/claudecode の parentSessionIDFromPath はファイルパスの
// "<parent-uuid>/subagents/agent-xxx.jsonl" という構造だけから親セッション ID を
// 復元する（レコード内の sessionId フィールドは使わない）ため、呼び出し側は
// parentSessionDir の basename を親セッションの jsonl と同じ ID にする必要がある。
func writeIntegrationSubagentSession(t *testing.T, parentSessionDir, agentID, cwd, description string, ts time.Time) {
	t.Helper()
	subDir := filepath.Join(parentSessionDir, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	f, err := os.Create(filepath.Join(subDir, agentID+".jsonl"))
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer f.Close()

	writeJSONLine(t, f, map[string]any{
		"type": "user", "timestamp": ts.Format(time.RFC3339Nano),
		"cwd": cwd, "gitBranch": "main", "entrypoint": "sdk-cli",
		"isSidechain": true, "agentId": agentID,
		"message": map[string]any{"role": "user", "content": "サブエージェントへの指示"},
	})
	writeJSONLine(t, f, map[string]any{
		"type": "assistant", "timestamp": ts.Add(time.Second).Format(time.RFC3339Nano),
		"cwd": cwd, "gitBranch": "main", "entrypoint": "sdk-cli", "effort": "high",
		"isSidechain": true, "agentId": agentID,
		"message": map[string]any{
			"model": "claude-sonnet-5", "id": "msg_" + agentID, "role": "assistant",
			"content": []map[string]any{{"type": "text", "text": "サブエージェントの応答"}},
			"usage": map[string]any{
				"input_tokens":            20,
				"output_tokens":           10,
				"cache_read_input_tokens": 0,
				"cache_creation":          map[string]any{"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0},
				"service_tier":            "standard",
			},
		},
	})

	metaBytes, err := json.Marshal(map[string]any{
		"agentType": "general-purpose", "description": description,
	})
	if err != nil {
		t.Fatalf("json.Marshal(meta): %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, agentID+".meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(meta): %v", err)
	}
}

// TestIntegration_IngestJudgeDailyReportActions は issue #1 が求める、コマンド層を
// 通した統合テスト。ingest -> judge -> daily -> report -> actions list を実際の cobra
// コマンドで駆動し、各段の受け渡し（継ぎ目）を確認する。
//
// 実ホーム（~/.claude・~/.insights）には一切触れず、すべて t.TempDir() 配下で完結する。
// AI 呼び出しは newJudge（internal/cli/deps.go）をフェイクに差し替えることで claude を
// 一切実行しない。
func TestIntegration_IngestJudgeDailyReportActions(t *testing.T) {
	tmp := t.TempDir()
	fakeClaudeHome := filepath.Join(tmp, "claude")
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")
	reportsDir := filepath.Join(tmp, "reports")

	// eval_runs（judge の実行記録）は created_at に実行時刻の実測値（time.Now().UTC()）を
	// 使う（internal/store.SaveEvalRun）ため、セッション自体の日付を固定値にしてしまうと
	// report 段の評価健全性セクション（EvalHealth）が「今日」とずれて出なくなる。
	// そのため対象日は実行時の「今日」（Local）を使う。
	now := time.Now()
	date := now.Format(dayLayout)
	dayStart, _, err := dayRange(date)
	if err != nil {
		t.Fatalf("dayRange(%s) error = %v", date, err)
	}
	base := dayStart.Add(9 * time.Hour)

	projPath := filepath.Join(tmp, "proj-a")
	const slug = "proj-a-slug"
	const parentSessionID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const subAgentID = "agent-cafebabe01"
	const subagentDescription = "サブエージェントによる調査タスク"

	parentPath := filepath.Join(fakeClaudeHome, "projects", slug, parentSessionID+".jsonl")
	writeValidSession(t, parentPath, parentSessionID, projPath, base)

	parentSessionDir := filepath.Join(fakeClaudeHome, "projects", slug, parentSessionID)
	writeIntegrationSubagentSession(t, parentSessionDir, subAgentID, projPath, subagentDescription, base.Add(2*time.Second))

	cfg := config.Default()
	cfg.Sources.ClaudeCode.Root = fakeClaudeHome
	isolateCodexSource(t, cfg)
	cfg.Evidence.Git = false
	cfg.Evidence.Gh = config.TristateFalse
	cfg.Evidence.Glab = config.TristateFalse
	cfg.Output.Dir = reportsDir
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	// === 1. ingest: 偽の ~/.claude ツリー -> DB ===
	{
		root := NewRootCommand("test")
		root.AddCommand(newIngestCommand())
		var outBuf, errBuf bytes.Buffer
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "ingest", "--all", "--no-evidence"})

		if err := root.Execute(); err != nil {
			t.Fatalf("ingest error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
		}

		var ingestRes ingestResult
		if err := json.Unmarshal(outBuf.Bytes(), &ingestRes); err != nil {
			t.Fatalf("ingest stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
		}
		// 継ぎ目: source/claudecode の Discover/Parse が親セッションとサブエージェントの
		// 両方を見つけ、DB へ取り込めていること（issue が挙げる「サブエージェントのログ
		// 配置」の不具合はここで起きた）。
		if ingestRes.Discovered != 2 || ingestRes.Ingested != 2 {
			t.Fatalf("ingest 結果 = %+v, want Discovered=2, Ingested=2", ingestRes)
		}
	}

	// ingest 直後の DB を直接見て、サブエージェントの親子関係が正しく保存されている
	// ことを確認する。
	{
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatalf("SessionsInRange: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close: %v", err)
		}

		if len(rows) != 2 {
			t.Fatalf("ingest 後のセッション数 = %d, want 2: %+v", len(rows), rows)
		}
		var parentRow, childRow *store.SessionRow
		for i := range rows {
			switch rows[i].SessionID {
			case parentSessionID:
				parentRow = &rows[i]
			case subAgentID:
				childRow = &rows[i]
			}
		}
		if parentRow == nil || childRow == nil {
			t.Fatalf("親/子セッションが見つからない: %+v", rows)
		}
		if parentRow.IsSidechain {
			t.Error("親セッションの IsSidechain = true, want false")
		}
		if !childRow.IsSidechain {
			t.Error("サブエージェントの IsSidechain = false, want true")
		}
		if childRow.ParentSessionID != parentSessionID {
			t.Errorf("サブエージェントの ParentSessionID = %q, want %q（ディレクトリ構成からの復元が壊れている）",
				childRow.ParentSessionID, parentSessionID)
		}
		if childRow.Title != subagentDescription {
			t.Errorf("サブエージェントの Title = %q, want %q (.meta.json の description)", childRow.Title, subagentDescription)
		}
	}

	// === 2. judge（フェイク Judge）: 未評価セッションを評価し DB にキャッシュする ===
	fj := &integrationJudge{}
	origNewJudge := newJudge
	newJudge = func(*config.Config) (judge.Judge, error) { return fj, nil }
	t.Cleanup(func() { newJudge = origNewJudge })

	{
		root := NewRootCommand("test")
		root.AddCommand(newJudgeCommand())
		var outBuf, errBuf bytes.Buffer
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "judge", "--date", date, "--yes"})

		if err := root.Execute(); err != nil {
			t.Fatalf("judge error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
		}

		var judgeRes judgeResult
		if err := json.Unmarshal(outBuf.Bytes(), &judgeRes); err != nil {
			t.Fatalf("judge stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
		}
		// 継ぎ目: サブエージェントは個別評価の対象から除外され、親セッションだけが
		// 評価されること（prepareEvalTargets によるサイドチェーン除外）。
		if judgeRes.SidechainExcluded != 1 {
			t.Errorf("SidechainExcluded = %d, want 1", judgeRes.SidechainExcluded)
		}
		if judgeRes.Evaluated != 1 || judgeRes.Failed != 0 {
			t.Errorf("Evaluated/Failed = %d/%d, want 1/0: %+v", judgeRes.Evaluated, judgeRes.Failed, judgeRes)
		}
	}

	// 継ぎ目: サブエージェント（sidechain）を個別評価せず、親セッションの評価プロンプト
	// に委譲の要約として畳み込むこと（buildChildSummaries -> judge.BuildSessionPrompt の
	// 受け渡し）。.meta.json の description がプロンプト本文に現れているかで確認する。
	fj.mu.Lock()
	if len(fj.sessionPrompts) != 1 {
		t.Fatalf("sessionPrompts の件数 = %d, want 1（親セッションのみ評価されるはず）", len(fj.sessionPrompts))
	}
	if !strings.Contains(fj.sessionPrompts[0], subagentDescription) {
		t.Errorf("親セッションの評価プロンプトにサブエージェントの要約（%q）が含まれていない。"+
			"サブエージェントのログが親の評価に畳み込まれていない可能性がある", subagentDescription)
	}
	fj.mu.Unlock()

	// judge 段階での評価結果が DB にキャッシュされていることを直接確認する
	// （プロンプトバージョン + content_hash がキー）。
	{
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		rows, err := db.SessionsInRange(base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatalf("SessionsInRange: %v", err)
		}
		var parentRow *store.SessionRow
		for i := range rows {
			if rows[i].SessionID == parentSessionID {
				parentRow = &rows[i]
			}
		}
		if parentRow == nil {
			t.Fatal("親セッションが見つからない")
		}
		_, ok, err := db.EvalFor(parentSessionID, prompts.PromptVersion, parentRow.ContentHash)
		if closeErr := db.Close(); closeErr != nil {
			t.Fatalf("db.Close: %v", closeErr)
		}
		if err != nil || !ok {
			t.Errorf("EvalFor(親セッション) = ok=%v err=%v, want ok=true（judge の結果がキャッシュされているはず）", ok, err)
		}
	}

	// === 3. daily（フェイク Judge）: 日報・振り返りを生成する ===
	var dailyRes dailyResult
	{
		root := NewRootCommand("test")
		root.AddCommand(newDailyCommand())
		var outBuf, errBuf bytes.Buffer
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "daily", "--date", date, "--yes"})

		if err := root.Execute(); err != nil {
			t.Fatalf("daily error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
		}
		if err := json.Unmarshal(outBuf.Bytes(), &dailyRes); err != nil {
			t.Fatalf("daily stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
		}
	}

	// 継ぎ目: judge 段階で評価済みのセッションを daily 段階が再評価しないこと
	// （評価キャッシュが judge -> daily のコマンドをまたいで効くこと）。
	if dailyRes.JudgeEvaluated != 0 {
		t.Errorf("daily.JudgeEvaluated = %d, want 0（judge 段階のキャッシュが効くはず）", dailyRes.JudgeEvaluated)
	}
	fj.mu.Lock()
	if len(fj.sessionPrompts) != 1 {
		t.Errorf("daily 実行後の sessionPrompts の件数 = %d, want 1（再評価されていないはず）", len(fj.sessionPrompts))
	}
	if fj.dailyCalls != 1 || fj.retroCalls != 1 {
		t.Errorf("dailyCalls/retroCalls = %d/%d, want 1/1", fj.dailyCalls, fj.retroCalls)
	}
	fj.mu.Unlock()

	if dailyRes.NoSessions {
		t.Fatal("daily.NoSessions = true, want false")
	}
	if dailyRes.ProposalCount != 1 {
		t.Errorf("daily.ProposalCount = %d, want 1", dailyRes.ProposalCount)
	}
	if dailyRes.DailyPath == "" || dailyRes.RetroPath == "" {
		t.Fatal("daily/retro のパスが空")
	}

	// 継ぎ目: 丸め（rollup.BuildDaily、親+子の合計セッション数・コスト）の結果が、
	// 日報 Markdown 先頭の最小フロントマターに正しく反映されていること。
	dailyMD, err := os.ReadFile(dailyRes.DailyPath)
	if err != nil {
		t.Fatalf("日報ファイルの読み取りに失敗: %v", err)
	}
	miniFM, err := render.ParseFrontMatter(dailyMD)
	if err != nil {
		t.Fatalf("render.ParseFrontMatter: %v", err)
	}
	if miniFM.Sessions != 2 {
		t.Errorf("フロントマターの Sessions = %d, want 2（親+サブエージェント）", miniFM.Sessions)
	}
	if miniFM.CostUSD <= 0 {
		t.Errorf("フロントマターの CostUSD = %v, want > 0", miniFM.CostUSD)
	}

	// 継ぎ目: サイドカー YAML（<outDir>/meta/<date>.yaml）だけから丸めの詳細
	// （サブエージェント件数を含む）を再集計できること（render.ParseSidecar の契約）。
	metaPath := filepath.Join(reportsDir, "meta", date+".yaml")
	sidecarBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("サイドカー YAML の読み取りに失敗: %v", err)
	}
	fm, err := render.ParseSidecar(sidecarBytes)
	if err != nil {
		t.Fatalf("render.ParseSidecar: %v", err)
	}
	if fm.Sessions != 2 || fm.SidechainSessions != 1 {
		t.Errorf("サイドカー YAML の Sessions/SidechainSessions = %d/%d, want 2/1", fm.Sessions, fm.SidechainSessions)
	}

	// === 4. report: 生成済みの日次ロールアップから HTML を出す ===
	// --from/--to は date のみだと EvalHealth（下記）を取りこぼしうるため、前後 1 日ずつ
	// 広げる。eval_runs.created_at は UTC の実測時刻、report の --from/--to は
	// time.Parse（＝UTC 起点）でパースされるのに対し、date はローカル日付なので、
	// タイムゾーンによっては date 単体の範囲に実測 created_at が収まらないことがある
	// （最大で日付が front/back に 1 日ずれうる）。日次ロールアップ自体は date の 1 日分
	// しか無いので、DaysWithData の期待値は変わらない。
	var reportRes reportResult
	outHTMLPath := filepath.Join(tmp, "report.html")
	fromDate := dayStart.AddDate(0, 0, -1).Format(dayLayout)
	toDate := dayStart.AddDate(0, 0, 1).Format(dayLayout)
	{
		root := NewRootCommand("test")
		root.AddCommand(newReportCommand())
		var outBuf, errBuf bytes.Buffer
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "report",
			"--from", fromDate, "--to", toDate, "--out", outHTMLPath})

		if err := root.Execute(); err != nil {
			t.Fatalf("report error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
		}
		if err := json.Unmarshal(outBuf.Bytes(), &reportRes); err != nil {
			t.Fatalf("report stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
		}
	}

	// 継ぎ目: daily が保存した daily_rollups の JSON を report が正しくデコードして
	// HTML を生成できること。
	if !reportRes.Generated || reportRes.DaysWithData != 1 {
		t.Fatalf("report 結果 = %+v, want Generated=true DaysWithData=1", reportRes)
	}
	htmlBytes, err := os.ReadFile(outHTMLPath)
	if err != nil {
		t.Fatalf("HTML レポートの読み取りに失敗: %v", err)
	}
	if !bytes.Contains(htmlBytes, []byte("<html")) {
		t.Error("HTML レポートに <html> が含まれていない")
	}
	// 継ぎ目: judge 段階の評価実行記録（eval_runs）は日次ロールアップ本体には
	// 載らず、report が db.EvalRunStatsInRange 経由で別途取り出して埋める
	// （evalHealthFor）。この経路が壊れると、評価が実行されたこと自体・その健全性が
	// 日次ロールアップからもレポートからも見えなくなる。
	if !bytes.Contains(htmlBytes, []byte("評価実行回数")) {
		t.Error("HTML レポートに評価実行の健全性セクションが含まれていない（EvalHealth の受け渡しが壊れている可能性）")
	}

	// === 5. actions list: 振り返りが登録した改善提案が見えること ===
	{
		root := NewRootCommand("test")
		root.AddCommand(newActionsCommand())
		var outBuf, errBuf bytes.Buffer
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetArgs([]string{"--config", configPath, "--db", dbPath, "--json", "actions", "list", "--all"})

		if err := root.Execute(); err != nil {
			t.Fatalf("actions list error = %v\nstdout=%s\nstderr=%s", err, outBuf.String(), errBuf.String())
		}

		var actionsRes actionsListResult
		if err := json.Unmarshal(outBuf.Bytes(), &actionsRes); err != nil {
			t.Fatalf("actions list stdout の JSON デコードに失敗しました: %v\nstdout=%s", err, outBuf.String())
		}

		// 継ぎ目: rollup.PersistRetro が振り返りの proposals を actions テーブルへ登録し、
		// 別コマンド実行（別 DB オープン）から actions list で見えること。
		if actionsRes.Total != 1 {
			t.Fatalf("actions.Total = %d, want 1: %+v", actionsRes.Total, actionsRes)
		}
		if len(actionsRes.Actions) != 1 || actionsRes.Actions[0].Title != "統合テスト用の改善提案" {
			t.Errorf("actions.Actions = %+v, want タイトル一致の1件", actionsRes.Actions)
		}
		if actionsRes.Actions[0].Status != "open" {
			t.Errorf("actions.Actions[0].Status = %q, want open", actionsRes.Actions[0].Status)
		}
	}
}
