package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/source"
)

// maxScanBufferBytes は bufio.Scanner の最大行バッファ。
// ロールアウトは 1 行が数 MB になることがあるため大きめに確保する。
const maxScanBufferBytes = 16 * 1024 * 1024

// firstPromptMaxLen は Session.FirstPrompt に残す文字数。claudecode と揃えている。
const firstPromptMaxLen = 200

// rolloutLine はロールアウト JSONL の 1 行。
//
// Codex 側は RolloutLine { timestamp, ordinal, #[serde(flatten)] item } という形で
// 書いており、item は type タグ + payload を持つ内部タグ付き列挙になる。
// つまり実際の 1 行は {"timestamp":..,"ordinal":..,"type":"..","payload":{..}}。
type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// sessionMeta は type="session_meta" の payload のうち、本実装が使う項目だけ。
// SessionMetaLine（SessionMeta を flatten し、git を並べたもの）に対応する。
type sessionMeta struct {
	ID             string          `json:"id"`         // スレッド ID
	SessionID      string          `json:"session_id"` // ルートスレッドの ID
	ParentThreadID string          `json:"parent_thread_id"`
	Timestamp      string          `json:"timestamp"`
	Cwd            string          `json:"cwd"`
	Source         json.RawMessage `json:"source"` // 文字列にもオブジェクトにもなる
	ThreadSource   string          `json:"thread_source"`
	Git            *gitInfo        `json:"git"`
}

// gitInfo は session_meta の git フィールド。
type gitInfo struct {
	Branch string `json:"branch"`
}

// turnContext は type="turn_context" の payload。ターンごとのモデルと推論強度が入る。
// ロールアウトにはアシスタント発話そのものにモデル名が乗らないため、この行を見て
// 「いまどのモデルで喋っているか」を追いかける必要がある。
type turnContext struct {
	Cwd    string `json:"cwd"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// tokenUsageRecord は type="token_usage_record" の payload。
// 1 レスポンス（API 呼び出し 1 回）ごとに 1 行書かれる。
type tokenUsageRecord struct {
	Usage codexUsage `json:"usage"`
}

// codexUsage は Codex の TokenUsage。
//
// input_tokens は cached_input_tokens を含む合計であることに注意
// （Codex 側の TokenUsage::non_cached_input が引き算している）。insights の
// model.Usage は Anthropic 系の意味論、つまり InputTokens がキャッシュ読み取りを
// 含まない値なので、変換時に差し引く。
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// compactedItem は type="compacted" の payload。文脈圧縮が起きた印。
type compactedItem struct {
	Message string `json:"message"`
}

// eventMsgEnvelope は type="event_msg" の payload の外側だけを見る。
// EventMsg は内部タグ付き列挙（Rust 側 #[serde(tag = "type")]）で、type の値ごとに
// 中身の形が変わる。ここでは token_count（使用量）だけを使うので、他の変種は
// Type だけ見て捨てる（Info は type=="token_count" のときしか埋まらない）。
type eventMsgEnvelope struct {
	Type string          `json:"type"`
	Info *tokenCountInfo `json:"info"`
}

// tokenCountInfo は event_msg(type="token_count").info（TokenUsageInfo）のうち使う項目。
type tokenCountInfo struct {
	LastTokenUsage codexUsage `json:"last_token_usage"`
}

// responseItem は type="response_item" の payload。Codex の ResponseItem に対応し、
// 会話本文・ツール呼び出し・ツール結果がすべてここに来る。
// 種類によって使うフィールドが変わるため、必要なものを 1 つの構造体に集めている。
type responseItem struct {
	Type string `json:"type"`

	// message / agent_message
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`

	// reasoning
	Summary []contentPart `json:"summary"`

	// function_call / custom_tool_call / function_call_output
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`

	// local_shell_call / web_search_call
	Action json.RawMessage `json:"action"`
}

// contentPart は content / summary の配列要素。text を持つ型だけを見る
// （input_image のような本文の無い要素は型名だけ残す）。
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Parse は 1 ロールアウトを読み、正規化された *model.Session を返す。
// 壊れた行はスキップして読み進め、ファイル全体が壊れているときだけエラーを返す。
func (s *Source) Parse(ref source.Ref) (*model.Session, error) {
	maxLen := s.MaxTextLen
	if maxLen <= 0 {
		maxLen = DefaultMaxTextLen
	}

	rc, err := openRollout(ref.Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("ロールアウトの読み取りに失敗しました (%s): %w", ref.Path, err)
	}

	// event_msg(type=token_count) からの使用量取り込み（下記 "event_msg" ケース）は、
	// token_usage_record が無いロールアウト専用のフォールバックにする。両方が書かれている
	// ロールアウト（新しめの Codex）で両方を拾うと、同じレスポンスの使用量を二重に数える。
	hasDedicatedUsageRecords := bytes.Contains(data, []byte(`"token_usage_record"`))

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanBufferBytes)

	sess := &model.Session{
		Source:         sourceName,
		SessionID:      ref.SessionID,
		TranscriptPath: ref.Path,
	}

	var (
		seq            int
		totalLines     int
		okLines        int
		haveTimestamp  bool
		firstPromptSet bool
		curModel       string
		curEffort      string
		toolNames      = map[string]string{}
	)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		totalLines++

		var rec rolloutLine
		if err := json.Unmarshal(line, &rec); err != nil {
			slog.Warn("codex: ロールアウト行の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
			continue
		}
		okLines++

		ts, tsOK := parseTimestamp(rec.Timestamp)
		if tsOK {
			if !haveTimestamp || ts.Before(sess.StartedAt) {
				sess.StartedAt = ts
			}
			if ts.After(sess.EndedAt) {
				sess.EndedAt = ts
			}
			haveTimestamp = true
		}

		appendMessages := func(msgs []model.Message) {
			for _, m := range msgs {
				m.Seq = seq
				seq++
				sess.Messages = append(sess.Messages, m)
				if !firstPromptSet && m.Role == model.RoleUser && !m.IsMeta {
					sess.FirstPrompt = truncateText(m.Text, firstPromptMaxLen)
					firstPromptSet = true
				}
			}
		}

		switch rec.Type {
		case "session_meta":
			var meta sessionMeta
			if err := json.Unmarshal(rec.Payload, &meta); err != nil {
				slog.Warn("codex: session_meta の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			applySessionMeta(sess, meta)
			// session_meta の timestamp は「セッションの開始時刻」そのもの。
			// 行の timestamp が欠けている古いロールアウトでも開始時刻を拾えるようにする。
			if t, ok := parseTimestamp(meta.Timestamp); ok {
				if !haveTimestamp || t.Before(sess.StartedAt) {
					sess.StartedAt = t
				}
				if t.After(sess.EndedAt) {
					sess.EndedAt = t
				}
				haveTimestamp = true
			}

		case "turn_context":
			var tc turnContext
			if err := json.Unmarshal(rec.Payload, &tc); err != nil {
				slog.Warn("codex: turn_context の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			if tc.Model != "" {
				curModel = tc.Model
			}
			if tc.Effort != "" {
				curEffort = tc.Effort
			}
			if sess.ProjectPath == "" && tc.Cwd != "" {
				sess.ProjectPath = tc.Cwd
			}

		case "token_usage_record":
			var rec2 tokenUsageRecord
			if err := json.Unmarshal(rec.Payload, &rec2); err != nil {
				slog.Warn("codex: token_usage_record の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			if extra := attachUsage(sess, convertUsage(rec2.Usage), curModel, curEffort, ts); extra != nil {
				extra.Seq = seq
				seq++
				sess.Messages = append(sess.Messages, *extra)
			}

		case "response_item":
			var item responseItem
			if err := json.Unmarshal(rec.Payload, &item); err != nil {
				slog.Warn("codex: response_item の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			appendMessages(convertResponseItem(item, ts, curModel, curEffort, maxLen, toolNames))

		case "compacted":
			var c compactedItem
			if err := json.Unmarshal(rec.Payload, &c); err != nil {
				slog.Warn("codex: compacted の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			// 文脈圧縮はセッションの読み方（それ以前のやり取りがモデルから見えなく
			// なる）に効くので、人間の発話ではない印として残す。
			appendMessages([]model.Message{{
				Timestamp: ts,
				Role:      model.RoleSystem,
				Text:      truncateText(c.Message, maxLen),
				Truncated: runeLen(c.Message) > maxLen,
				IsMeta:    true,
			}})

		case "event_msg":
			// event_msg は本来、legacy 履歴モードのロールアウトで response_item と同じ発話を
			// もう一度書く経路（取り込むと会話が二重になる）だが、type=token_count のものだけは
			// 例外で、token_usage_record が無い版の Codex ではここにしか使用量が来ない
			// （codex-rs の rollout/policy.rs は token_count を常に永続化対象にしている）。
			if hasDedicatedUsageRecords {
				// 両方あるロールアウトでは token_usage_record 側だけを正とし、二重計上を避ける。
				continue
			}
			var em eventMsgEnvelope
			if err := json.Unmarshal(rec.Payload, &em); err != nil {
				slog.Warn("codex: event_msg の解析に失敗しました", "path", ref.Path, "line", lineNo, "error", err)
				continue
			}
			if em.Type != "token_count" || em.Info == nil {
				continue
			}
			// last_token_usage は「直近 1 レスポンス分の使用量」（total_token_usage は
			// セッション開始からの累積）。TokenUsageRecord.usage と同じ粒度なので、
			// 同じ attachUsage にそのまま渡せる。
			if extra := attachUsage(sess, convertUsage(em.Info.LastTokenUsage), curModel, curEffort, ts); extra != nil {
				extra.Seq = seq
				seq++
				sess.Messages = append(sess.Messages, *extra)
			}

		default:
			// world_state / security_risk_score / realtime_item などは正規化対象外。
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ロールアウトの読み取りに失敗しました (%s): %w", ref.Path, err)
	}
	if totalLines > 0 && okLines == 0 {
		return nil, fmt.Errorf("ロールアウト全体が壊れています (%s): 有効な行がありません", ref.Path)
	}

	// ワークツリー配下での作業は元のプロジェクトに寄せる（claudecode と同じ扱い）。
	if base, worktree := model.SplitWorktreePath(sess.ProjectPath); worktree != "" {
		sess.ProjectPath = base
		sess.Worktree = worktree
	}
	sess.ProjectLabel = lastPathElement(sess.ProjectPath)
	sess.ContentHash = model.ContentHash(sess.Messages)

	return sess, nil
}

// applySessionMeta は session_meta の内容をセッションに反映する。
func applySessionMeta(sess *model.Session, meta sessionMeta) {
	if meta.Cwd != "" {
		sess.ProjectPath = meta.Cwd
	}
	if meta.Git != nil && meta.Git.Branch != "" {
		sess.GitBranch = meta.Git.Branch
	}

	entrypoint, nonRoot, parentFromSource := decodeSessionSource(meta.Source)
	if entrypoint != "" {
		sess.Entrypoint = entrypoint
	}

	// サブエージェント（Codex の言い方では非ルートのスレッド）は、親セッションの
	// 評価に畳み込むため個別評価の対象から外す。判定材料は 3 つあり、どれか 1 つでも
	// 立っていればサブエージェントとみなす（Codex 側もバージョンによって、どの
	// フィールドに現れるかが違うため）。
	if nonRoot || strings.EqualFold(meta.ThreadSource, "subagent") || meta.ParentThreadID != "" {
		sess.IsSidechain = true
	}

	switch {
	case meta.ParentThreadID != "":
		sess.ParentSessionID = meta.ParentThreadID
	case parentFromSource != "":
		sess.ParentSessionID = parentFromSource
	case sess.IsSidechain && meta.SessionID != "" && meta.SessionID != meta.ID:
		// session_id はルートスレッドの ID。直接の親が分からない場合の最後の頼り。
		sess.ParentSessionID = meta.SessionID
	}
}

// decodeSessionSource は session_meta の source を読む。
//
// Codex の SessionSource は「文字列」にも「1 キーのオブジェクト」にもなる
// （Cli/VSCode/Exec/Mcp/Unknown は "cli" のような文字列、Custom/Internal/SubAgent は
// {"custom":"atlas"} や {"subagent":{"thread_spawn":{...}}} のようなオブジェクト）。
// 戻り値の entrypoint は Codex 側の Display 実装と同じ表記に揃える。
func decodeSessionSource(raw json.RawMessage) (entrypoint string, nonRootAgent bool, parentThreadID string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false, ""
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", false, ""
		}
		return s, false, ""
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return "", false, ""
	}
	for key, val := range obj {
		switch key {
		case "custom":
			var s string
			if err := json.Unmarshal(val, &s); err == nil {
				return s, false, ""
			}
			return "", false, ""
		case "internal":
			// メモリ整理・ガーディアンなど、Codex が内部的に回すスレッド。
			// 利用者が始めた作業ではないので非ルート扱いにする。
			return "internal_" + variantName(val), true, ""
		case "subagent":
			name, parent := decodeSubAgentSource(val)
			return "subagent_" + name, true, parent
		}
	}
	return "", false, ""
}

// decodeSubAgentSource は SubAgentSource（"review" のような文字列、または
// {"thread_spawn":{...}} のようなオブジェクト）から種別名と親スレッド ID を取り出す。
func decodeSubAgentSource(raw json.RawMessage) (name, parentThreadID string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s, ""
		}
		return "", ""
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return "", ""
	}
	for key, val := range obj {
		if key == "thread_spawn" {
			var spawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			}
			if err := json.Unmarshal(val, &spawn); err == nil {
				return key, spawn.ParentThreadID
			}
		}
		return key, ""
	}
	return "", ""
}

// variantName は外部タグ付き列挙の中身（文字列 or 1 キーのオブジェクト）から
// 種別名を取り出す。読めなければ "unknown" を返す。
func variantName(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "unknown"
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil && s != "" {
			return s
		}
		return "unknown"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		for key := range obj {
			return key
		}
	}
	return "unknown"
}

// convertResponseItem は response_item 1 件を 0 件以上の model.Message に変換する。
func convertResponseItem(item responseItem, ts time.Time, curModel, curEffort string, maxLen int, toolNames map[string]string) []model.Message {
	switch item.Type {
	case "message":
		return convertMessageItem(item, ts, curModel, curEffort)

	case "agent_message":
		// マルチエージェント時のエージェント間メッセージ。人間の発話ではないが、
		// 何が委譲されたかは評価に効くので本文として残す。
		text := joinTextParts(item.Content)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleAssistant,
			Model:     curModel,
			Effort:    curEffort,
			Text:      truncateText(text, maxLen),
			Truncated: runeLen(text) > maxLen,
		}}

	case "reasoning":
		// 推論の要約。claudecode の thinking と同じく「人間の発話ではない」印を付ける。
		parts := append(append([]contentPart{}, item.Summary...), item.Content...)
		text := joinTextParts(parts)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleAssistant,
			Model:     curModel,
			Effort:    curEffort,
			Text:      truncateText(text, maxLen),
			Truncated: runeLen(text) > maxLen,
			IsMeta:    true,
		}}

	case "function_call", "custom_tool_call":
		name := item.Name
		if item.CallID != "" && name != "" {
			toolNames[item.CallID] = name
		}
		body := item.Arguments
		if body == "" {
			body = item.Input
		}
		return []model.Message{toolCallMessage(ts, curModel, curEffort, name, body, maxLen)}

	case "local_shell_call":
		return []model.Message{toolCallMessage(ts, curModel, curEffort, "local_shell", compactJSON(item.Action), maxLen)}

	case "web_search_call":
		return []model.Message{toolCallMessage(ts, curModel, curEffort, "web_search", compactJSON(item.Action), maxLen)}

	case "function_call_output", "custom_tool_call_output":
		name := item.Name
		if name == "" {
			name = toolNames[item.CallID]
		}
		text := extractOutputText(item.Output)
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleTool,
			Text:      truncateText(text, maxLen),
			Truncated: runeLen(text) > maxLen,
			ToolName:  name,
			// ツール結果が成功だったかはロールアウトに残らない
			// （Codex の FunctionCallOutputPayload.success はワイヤに出ない）。
			// 推測で埋めると「ツールエラー数」が実態と無関係な数字になるため、
			// 分からないものは分からないままにしておく。
		}}

	default:
		// tool_search_call / image_generation_call / compaction / ghost_snapshot など。
		// 会話の意味を運ばないか、暗号化されていて読めないものはスキップする。
		return nil
	}
}

// convertMessageItem は type="message" の response_item を変換する。
func convertMessageItem(item responseItem, ts time.Time, curModel, curEffort string) []model.Message {
	text := joinTextParts(item.Content)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	switch strings.ToLower(item.Role) {
	case "assistant":
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleAssistant,
			Model:     curModel,
			Effort:    curEffort,
			Text:      text,
		}}
	case "user":
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleUser,
			Text:      text,
			IsMeta:    isMetaText(text),
		}}
	default:
		// developer / system。Codex が組み立てて差し込む指示（AGENTS.md の内容、
		// 環境情報、権限の説明など）で、人間が書いた発話ではない。
		return []model.Message{{
			Timestamp: ts,
			Role:      model.RoleSystem,
			Text:      text,
			IsMeta:    true,
		}}
	}
}

// toolCallMessage はツール呼び出し 1 件分の Message を作る。
func toolCallMessage(ts time.Time, curModel, curEffort, name, body string, maxLen int) model.Message {
	return model.Message{
		Timestamp: ts,
		Role:      model.RoleAssistant,
		Model:     curModel,
		Effort:    curEffort,
		ToolName:  name,
		Text:      truncateText(body, maxLen),
		Truncated: runeLen(body) > maxLen,
	}
}

// attachUsage は 1 レスポンス分のトークン使用量を、直近のアシスタント発話に載せる。
//
// Codex は使用量を発話とは別の行（token_usage_record）に書くため、どの発話の分か
// を対応付ける必要がある。1 レスポンス＝直前に並んでいるアシスタント発話群なので、
// 「まだ使用量が付いていない直近のアシスタント発話」に付ける。
// 該当が無い場合（発話を 1 つも伴わないレスポンス）は、集計から漏らさないために
// 使用量だけを持つメッセージを返す（呼び出し側が末尾に足す）。
func attachUsage(sess *model.Session, usage *model.Usage, curModel, curEffort string, ts time.Time) *model.Message {
	if usage == nil {
		return nil
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		m := &sess.Messages[i]
		if m.Role != model.RoleAssistant {
			continue
		}
		if m.Usage != nil {
			// ここより前は既に別のレスポンスとして対応付け済み。
			break
		}
		m.Usage = usage
		if m.Model == "" {
			m.Model = curModel
		}
		return nil
	}
	return &model.Message{
		Timestamp: ts,
		Role:      model.RoleAssistant,
		Model:     curModel,
		Effort:    curEffort,
		IsMeta:    true,
		Usage:     usage,
	}
}

// convertUsage は Codex の使用量を model.Usage に変換する。
//
// Codex の input_tokens はキャッシュ読み取り分を含む合計なので、差し引いて
// insights の意味論（InputTokens はキャッシュ読み取りを含まない）に合わせる。
// これをしないと、キャッシュヒットしたトークンを入力単価で二重に数えてしまう。
func convertUsage(u codexUsage) *model.Usage {
	if u == (codexUsage{}) {
		return nil
	}
	input := u.InputTokens - u.CachedInputTokens
	if input < 0 {
		input = 0
	}
	return &model.Usage{
		InputTokens:    input,
		OutputTokens:   u.OutputTokens,
		ThinkingTokens: u.ReasoningOutputTokens,
		CacheRead:      u.CachedInputTokens,
		// Codex（OpenAI）のキャッシュには claudecode のような 5分/1時間の区別が無い。
		// 単価表の項目に合わせて 5m 側に寄せる。
		CacheCreation5m: u.CacheWriteInputTokens,
	}
}

// metaTextMarkers は「人間の発話ではない user ロールのメッセージ」を見分ける印。
//
// Codex は AGENTS.md の内容・シェル実行の報告・スキルの説明などを user ロールの
// メッセージとして会話に差し込む。これらを人間の発話として数えると、対話の往復数も
// 最初のプロンプトも実態からずれる。印は Codex 側の「文脈フラグメント」実装
// （codex-rs/core/src/context/*）が使っているマーカーに対応する。
var metaTextMarkers = []string{
	"# AGENTS.md instructions",
	"<user_shell_command>",
	"<turn_aborted>",
	"<subagent_notification>",
	"<codex_internal_context",
	"<goal_context>",
	"<skill>",
	// 以下は古いロールアウト向け。現在の Codex は developer ロールで差し込む。
	"<environment_context>",
	"<user_instructions>",
}

// isMetaText は user ロールの本文が Codex の差し込みかどうかを判定する。
func isMetaText(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, marker := range metaTextMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

// joinTextParts は content / summary の配列からテキストを取り出して連結する。
// 本文を持たない要素（画像・暗号化された推論内容など）は型名だけを残し、
// 「何かがあったが読めない」ことが分かるようにする。
func joinTextParts(parts []contentPart) string {
	var out []string
	for _, p := range parts {
		switch {
		case p.Text != "":
			out = append(out, p.Text)
		case p.Type == "input_image", p.Type == "input_audio":
			out = append(out, "["+p.Type+"]")
		}
	}
	return strings.Join(out, "\n")
}

// extractOutputText は function_call_output の output を読む。
// ワイヤ上は「文字列」か「構造化された content items の配列」のどちらかになる。
func extractOutputText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return string(trimmed)
	}
	if trimmed[0] == '[' {
		var parts []contentPart
		if err := json.Unmarshal(trimmed, &parts); err == nil {
			return joinTextParts(parts)
		}
	}
	return string(trimmed)
}

// compactJSON は JSON を 1 行に圧縮する。ツール呼び出しの引数を本文に載せるときに使う。
func compactJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// openRollout は素の .jsonl と圧縮済みの .jsonl.zst のどちらでも読めるように開く。
// 書き終えて 7 日以上経ったロールアウトは Codex 自身が zstd で圧縮するため、
// 圧縮版を読めないと過去ぶんの取り込みがそこで途切れる。
func openRollout(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ロールアウトを開けませんでした (%s): %w", path, err)
	}
	if !strings.HasSuffix(path, compressedSuffix) {
		return f, nil
	}

	dec, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("圧縮ロールアウトを開けませんでした (%s): %w", path, err)
	}
	return &zstdReadCloser{decoder: dec, file: f}, nil
}

// zstdReadCloser は zstd デコーダと元ファイルをまとめて閉じるためのラッパ。
// zstd.Decoder の Close は error を返さないため、Close の戻り値は元ファイルのもの。
type zstdReadCloser struct {
	decoder *zstd.Decoder
	file    *os.File
}

func (z *zstdReadCloser) Read(p []byte) (int, error) { return z.decoder.Read(p) }

func (z *zstdReadCloser) Close() error {
	z.decoder.Close()
	return z.file.Close()
}

// parseTimestamp は ISO8601 タイムスタンプ（小数秒あり/なし）をパースする。
func parseTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// runeLen は文字列のルーン数（マルチバイト文字を 1 文字として数える）。
func runeLen(s string) int { return len([]rune(s)) }

// truncateText は s をルーン単位で maxLen 文字に切り詰める。
func truncateText(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

// lastPathElement は path の末尾要素を返す。cwd は記録した側のマシンの区切り文字で
// 入っており、実行環境の filepath とは限らないため、/ と \ の両方を区切りとして扱う。
func lastPathElement(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if trimmed == "" {
		return ""
	}
	idx := strings.LastIndexAny(trimmed, `/\`)
	return trimmed[idx+1:]
}
