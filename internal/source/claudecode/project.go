package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// lastPathElement は path の末尾要素を返す。cwd はレコード採取時のマシンの区切り文字
// （Windows なら \）で入っており、実行環境の filepath とは限らないため、
// / と \ の両方を区切りとして扱う。
func lastPathElement(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if trimmed == "" {
		return ""
	}
	idx := strings.LastIndexAny(trimmed, `/\`)
	return trimmed[idx+1:]
}

// reconstructProjectPath はエンコードされたプロジェクトディレクトリ名（cwd の / \ : を
// - に置換したもの）から元の cwd を推定する。ハイフンがプロジェクト名自体に含まれる
// ケースがあり一般には可逆でないため、実際に存在するディレクトリになった場合だけ採用し、
// 駄目なら空文字を返す。
func reconstructProjectPath(dirName string) string {
	if dirName == "" {
		return ""
	}
	if candidate := windowsPathCandidate(dirName); candidate != "" && isExistingDir(candidate) {
		return candidate
	}
	if candidate := unixPathCandidate(dirName); candidate != "" && isExistingDir(candidate) {
		return candidate
	}
	return ""
}

// windowsPathCandidate は "C--Users-foo-bar" のような名前から "C:\Users\foo\bar" を組み立てる。
func windowsPathCandidate(dirName string) string {
	if len(dirName) < 3 {
		return ""
	}
	drive := dirName[0]
	isLetter := (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')
	if !isLetter || dirName[1] != '-' || dirName[2] != '-' {
		return ""
	}
	rest := strings.ReplaceAll(dirName[3:], "-", `\`)
	return string(drive) + `:\` + rest
}

// unixPathCandidate は "-Users-foo-bar" のような名前から "/Users/foo/bar" を組み立てる。
func unixPathCandidate(dirName string) string {
	if !strings.HasPrefix(dirName, "-") {
		return ""
	}
	rest := strings.ReplaceAll(strings.TrimPrefix(dirName, "-"), "-", "/")
	return "/" + rest
}

// isExistingDir は path が実在するディレクトリかどうかを返す。
func isExistingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// parentSessionIDFromPath は "<slug>/<parent-uuid>/subagents/agent-xxx.jsonl" という
// 実ファイルパスから親セッション ID (<parent-uuid>) を復元する。
// subagents/ 配下でなければ空文字を返す（メインセッションには親が無い）。
func parentSessionIDFromPath(jsonlPath string) string {
	dir := filepath.Dir(jsonlPath) // .../<parent-uuid>/subagents
	if !strings.EqualFold(filepath.Base(dir), subagentsDirName) {
		return ""
	}
	return filepath.Base(filepath.Dir(dir))
}

// agentMeta は agent-<id>.meta.json の内容のうち、Title の復元に使う部分だけ。
type agentMeta struct {
	Description string `json:"description"`
}

// loadAgentMetaDescription は jsonlPath と同じディレクトリにある
// "<拡張子を除いた名前>.meta.json" の description を読む。
// ファイルが無い・壊れている場合はエラーにせず空文字を返す
// （サブエージェント以外のセッションには存在しないのが普通）。
func loadAgentMetaDescription(jsonlPath string) string {
	metaPath := strings.TrimSuffix(jsonlPath, filepath.Ext(jsonlPath)) + ".meta.json"

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}

	var meta agentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Description)
}
