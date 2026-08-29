package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/rollup"
	"github.com/fuchigta/insights/internal/store"
)

// --- テスト用ヘルパ ---

// runReportCLI は NewRootCommand + newReportCommand を組み合わせて実行する。
// root.go 自体は変更していないため（他の担当が同時に internal/cli にファイルを
// 追加しており、配線は後でコーディネータがまとめて行う）、この組み立ては
// テスト側で毎回行う。--config には存在しないパスを渡してもよい
// （config.Load はファイルが無ければ既定値を使うため、実ホームには一切触れない）。
func runReportCLI(t *testing.T, configPath, dbPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand("test")
	root.AddCommand(newReportCommand())

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	fullArgs := append([]string{"--config", configPath, "--db", dbPath, "report"}, args...)
	root.SetArgs(fullArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// minimalDaily は BuildSeries が読める最小限の rollup.Daily を作る。
func minimalDaily(date string, sessions int, cost float64) *rollup.Daily {
	return &rollup.Daily{
		Date:        date,
		GeneratedAt: time.Now().UTC(),
		Totals: rollup.Totals{
			Sessions:            sessions,
			InteractiveSessions: sessions,
			DurationMinutes:     float64(sessions) * 10,
			CostUSD:             cost,
		},
		Facets: rollup.Facets{
			Outcome: map[string]int{"achieved": sessions},
		},
	}
}

// saveDailyRollup は minimalDaily を JSON 化して db.SaveRollup で保存する。
func saveDailyRollup(t *testing.T, db *store.DB, date string, sessions int, cost float64) {
	t.Helper()
	raw, err := json.Marshal(minimalDaily(date, sessions, cost))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := db.SaveRollup(date, raw, "", ""); err != nil {
		t.Fatalf("SaveRollup(%s): %v", date, err)
	}
}

// --- テスト本体 ---

func TestReport_GeneratesHTMLForRange(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml") // 存在しないパス（既定値で動作する）
	dbPath := filepath.Join(tmp, "insights.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	saveDailyRollup(t, db, "2026-03-01", 2, 1.5)
	saveDailyRollup(t, db, "2026-03-02", 1, 0.5)
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	outPath := filepath.Join(tmp, "out.html")
	stdout, _, err := runReportCLI(t, configPath, dbPath,
		"--from", "2026-03-01", "--to", "2026-03-02", "--out", outPath)
	if err != nil {
		t.Fatalf("report 実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	info, statErr := os.Stat(outPath)
	if statErr != nil {
		t.Fatalf("出力ファイルが見つかりません: %v", statErr)
	}
	if info.Size() == 0 {
		t.Error("出力ファイルが空です")
	}
	if !strings.Contains(stdout, outPath) {
		t.Errorf("stdout に出力パスが含まれていません: %s", stdout)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(data, []byte("<html")) && !bytes.Contains(data, []byte("<!DOCTYPE")) {
		t.Errorf("出力ファイルが HTML に見えません（先頭 200 バイト: %q）", string(data[:min(200, len(data))]))
	}
}

func TestReport_NoDataInRangeShowsGuidanceWithoutError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	stdout, _, err := runReportCLI(t, configPath, dbPath,
		"--from", "2099-01-01", "--to", "2099-01-02", "--json")
	if err != nil {
		t.Fatalf("データなし期間はエラー終了しないはず: %v (stdout=%s)", err, stdout)
	}

	var payload reportResult
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v (stdout=%s)", jsonErr, stdout)
	}
	if payload.Generated {
		t.Errorf("Generated = true, want false")
	}
	if !strings.Contains(payload.Message, "daily") {
		t.Errorf("案内メッセージに daily コマンドの案内が含まれていません: %q", payload.Message)
	}
	if payload.OutputPath != "" {
		t.Errorf("OutputPath = %q, データが無い場合は空のはず", payload.OutputPath)
	}
}

func TestReport_MissingDaysAreWarnedButStillGenerated(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	saveDailyRollup(t, db, "2026-04-01", 1, 1.0)
	saveDailyRollup(t, db, "2026-04-03", 1, 1.0) // 04-02 は欠測
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	outPath := filepath.Join(tmp, "out.html")
	stdout, _, err := runReportCLI(t, configPath, dbPath,
		"--from", "2026-04-01", "--to", "2026-04-03", "--out", outPath, "--json")
	if err != nil {
		t.Fatalf("report 実行に失敗しました: %v (stdout=%s)", err, stdout)
	}

	var payload reportResult
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil {
		t.Fatalf("JSON デコードに失敗: %v (stdout=%s)", jsonErr, stdout)
	}
	if !payload.Generated {
		t.Errorf("Generated = false, 欠測日があっても生成されるはず")
	}
	if payload.MissingDays != 1 {
		t.Errorf("MissingDays = %d, want 1", payload.MissingDays)
	}
	if payload.DaysWithData != 2 {
		t.Errorf("DaysWithData = %d, want 2", payload.DaysWithData)
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Fatalf("欠測日があっても HTML は生成されるはず: %v", statErr)
	}

	// 人間向け出力でも欠測が警告として見えることを確認する。
	stdoutHuman, _, err := runReportCLI(t, configPath, dbPath,
		"--from", "2026-04-01", "--to", "2026-04-03", "--out", outPath)
	if err != nil {
		t.Fatalf("report 実行に失敗しました: %v", err)
	}
	if !strings.Contains(stdoutHuman, "欠測") {
		t.Errorf("人間向け出力に欠測の警告が含まれていません: %s", stdoutHuman)
	}
}

func TestReport_FromAfterToIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	_, _, err := runReportCLI(t, configPath, dbPath, "--from", "2026-05-10", "--to", "2026-05-01")
	if err == nil {
		t.Fatal("from > to はエラーになるはず")
	}
}

func TestReport_MissingFromOrToIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	if _, _, err := runReportCLI(t, configPath, dbPath, "--to", "2026-05-01"); err == nil {
		t.Error("--from 無しはエラーになるはず")
	}
	if _, _, err := runReportCLI(t, configPath, dbPath, "--from", "2026-05-01"); err == nil {
		t.Error("--to 無しはエラーになるはず")
	}
}
