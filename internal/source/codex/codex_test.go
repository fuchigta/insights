package codex

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// writeRollout は root 配下の日付ディレクトリにロールアウトを 1 本作る。
// compress が true なら zstd で圧縮した .jsonl.zst として書く。
func writeRollout(t *testing.T, root, subdir, day, name, body string, compress bool) string {
	t.Helper()

	dir := filepath.Join(root, subdir, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("ディレクトリの作成に失敗: %v", err)
	}

	path := filepath.Join(dir, name)
	if !compress {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("ロールアウトの作成に失敗: %v", err)
		}
		return path
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("ロールアウトの作成に失敗: %v", err)
	}
	defer f.Close()
	w, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatalf("zstd writer の作成に失敗: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("zstd への書き込みに失敗: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zstd のクローズに失敗: %v", err)
	}
	return path
}

func TestName(t *testing.T) {
	if got := New(t.TempDir()).Name(); got != sourceName {
		t.Errorf("Name() = %q, want %q", got, sourceName)
	}
}

// TestNew_ResolvesRoot は root 未指定時の解決順（CODEX_HOME → ~/.codex）を確かめる。
// Codex 自身が CODEX_HOME を優先するため、ここがずれるとホームを移している利用者の
// ログを丸ごと取りこぼす。
func TestNew_ResolvesRoot(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join("X:", "somewhere", "codex"))
	if got := New("").Root; got != filepath.Join("X:", "somewhere", "codex") {
		t.Errorf("Root = %q, want CODEX_HOME の値", got)
	}

	t.Setenv("CODEX_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("ホームディレクトリを取得できない環境のためスキップ")
	}
	if got, want := New("").Root, filepath.Join(home, ".codex"); got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}

	// 明示した root は常に優先される。
	if got := New("/explicit").Root; got != "/explicit" {
		t.Errorf("Root = %q, want /explicit", got)
	}
}

func TestAvailable(t *testing.T) {
	root := t.TempDir()

	s := New(root)
	if err := s.Available(); err == nil {
		t.Error("sessions/ が無いのに Available() = nil")
	}

	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o755); err != nil {
		t.Fatalf("ディレクトリの作成に失敗: %v", err)
	}
	if err := s.Available(); err != nil {
		t.Errorf("Available() = %v, want nil", err)
	}
}

func TestParseRolloutFileName(t *testing.T) {
	tests := []struct {
		name           string
		file           string
		wantID         string
		wantCompressed bool
		wantOK         bool
	}{
		{
			name:   "通常",
			file:   "rollout-2026-08-30T10-11-12-11111111-2222-3333-4444-555555555555.jsonl",
			wantID: "11111111-2222-3333-4444-555555555555",
			wantOK: true,
		},
		{
			name:           "圧縮済み",
			file:           "rollout-2026-08-30T10-11-12-11111111-2222-3333-4444-555555555555.jsonl.zst",
			wantID:         "11111111-2222-3333-4444-555555555555",
			wantCompressed: true,
			wantOK:         true,
		},
		{
			// 履歴を巻き戻したスレッドは <thread-id>_<rollout-id> になる。
			// ID を分解してしまうと DB 上で同じセッションとして潰し合うため、
			// ファイル名の ID 部分をそのまま使う。
			name:   "巻き戻し後（ID が 2 つ）",
			file:   "rollout-2026-08-30T10-11-12-thread-1_rollout-9.jsonl",
			wantID: "thread-1_rollout-9",
			wantOK: true,
		},
		{name: "接頭辞が違う", file: "session-2026-08-30T10-11-12-abc.jsonl"},
		{name: "拡張子が違う", file: "rollout-2026-08-30T10-11-12-abc.json"},
		{name: "ID が無い", file: "rollout-2026-08-30T10-11-12-.jsonl"},
		{name: "短すぎる", file: "rollout-abc.jsonl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, compressed, ok := parseRolloutFileName(tt.file)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if compressed != tt.wantCompressed {
				t.Errorf("compressed = %v, want %v", compressed, tt.wantCompressed)
			}
		})
	}
}

// TestDiscover は sessions/ と archived_sessions/ の両方を拾い、ロールアウトでない
// ファイルを無視することを確かめる。アーカイブは利用者が片付けただけで、実際に
// 行われた作業であることは変わらないため対象に含める。
func TestDiscover(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "sessions", "2026/08/30", "rollout-2026-08-30T10-00-00-aaa.jsonl", "{}\n", false)
	writeRollout(t, root, "sessions", "2026/08/31", "rollout-2026-08-31T10-00-00-bbb.jsonl.zst", "{}\n", true)
	writeRollout(t, root, "archived_sessions", "2026/07/01", "rollout-2026-07-01T10-00-00-ccc.jsonl", "{}\n", false)
	writeRollout(t, root, "sessions", "2026/08/30", "notes.txt", "not a rollout", false)

	refs, err := New(root).Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	got := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Source != sourceName {
			t.Errorf("Source = %q, want %q", r.Source, sourceName)
		}
		if r.Size <= 0 {
			t.Errorf("%s: Size = %d, want > 0", r.SessionID, r.Size)
		}
		got = append(got, r.SessionID)
	}
	sort.Strings(got)

	want := []string{"aaa", "bbb", "ccc"}
	if len(got) != len(want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Discover() = %v, want %v", got, want)
		}
	}
}

// TestDiscover_PrefersPlainOverCompressed は、同じ ID の素の .jsonl と .jsonl.zst が
// 並存したときに素のほうを採ることを確かめる（Codex 自身の優先順と揃える）。
// 両方を返すと、同じセッションを 2 回取り込もうとして無駄が出る。
func TestDiscover_PrefersPlainOverCompressed(t *testing.T) {
	root := t.TempDir()
	writeRollout(t, root, "sessions", "2026/08/30", "rollout-2026-08-30T10-00-00-dup.jsonl.zst", "{}\n", true)
	plain := writeRollout(t, root, "sessions", "2026/08/30", "rollout-2026-08-30T10-00-00-dup.jsonl", "{}\n", false)

	refs, err := New(root).Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("Discover() = %d 件, want 1 件", len(refs))
	}
	if refs[0].Path != plain {
		t.Errorf("Path = %q, want %q（素の .jsonl を優先する）", refs[0].Path, plain)
	}
}

// TestDiscover_Since は since より古いファイルを返さないことを確かめる。
func TestDiscover_Since(t *testing.T) {
	root := t.TempDir()
	old := writeRollout(t, root, "sessions", "2026/07/01", "rollout-2026-07-01T10-00-00-old.jsonl", "{}\n", false)

	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("mtime の変更に失敗: %v", err)
	}
	writeRollout(t, root, "sessions", "2026/08/30", "rollout-2026-08-30T10-00-00-new.jsonl", "{}\n", false)

	refs, err := New(root).Discover(time.Now().Add(-1 * time.Hour))
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(refs) != 1 || refs[0].SessionID != "new" {
		t.Fatalf("Discover() = %+v, want new のみ", refs)
	}
}

// TestDiscover_MissingRootIsNotAnError は、sessions/ が無い環境でも
// Discover がエラーにならないことを確かめる。Codex を使っていない利用者でも
// ingest 全体は動き続ける必要がある。
func TestDiscover_MissingRootIsNotAnError(t *testing.T) {
	refs, err := New(t.TempDir()).Discover(time.Time{})
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	if len(refs) != 0 {
		t.Errorf("Discover() = %d 件, want 0 件", len(refs))
	}
}
