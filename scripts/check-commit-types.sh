#!/bin/sh
# Conventional Commits の type 一覧の突き合わせ。
#
# 同じ type 一覧が次の 3 箇所に散っている。
#
#   - cliff.toml               commit_parsers（リリースノートの分類）
#   - scripts/check-commit-subject.sh  PATTERN（コミットメッセージの検証）
#   - CLAUDE.md                 表（人が読む説明）
#
# type を足したときに 1 箇所だけ直すと、通るのに分類されない（リリースノートの
# 「その他」に落ちる）、あるいは説明だけ古い、という状態になる。しかもコミットは
# 通ってしまうので気付けない。このスクリプトは 3 箇所から type 集合を抜き出して
# 突き合わせ、食い違っていれば差分を表示して失敗する。
#
# 引数は取らない。リポジトリのルートから実行する前提（scripts/check-doc-sync.sh と同じ）。
set -eu

CLIFF_TOML=cliff.toml
CHECK_SUBJECT=scripts/check-commit-subject.sh
CLAUDE_MD=CLAUDE.md

for f in "$CLIFF_TOML" "$CHECK_SUBJECT" "$CLAUDE_MD"; do
  if [ ! -f "$f" ]; then
    printf '%s が見つかりません。リポジトリのルートから実行してください。\n' "$f" >&2
    exit 1
  fi
done

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# --- cliff.toml ----------------------------------------------------------
# commit_parsers の中で「message が ^type というちょうどその形（英字のみ）」の行だけを
# type 集合とみなす。拾わない行とその理由:
#
#   - `{ message = '^chore\(release\)', skip = true }`
#     type そのものの宣言ではなく、「chore の中でも release スコープはリリースノートに
#     出さない」というスコープ限定の抑制指定なので、type 一覧としては数えない。
#     （英字のみを拾う正規表現なら `\(` を含むこの行は自然に除外される）
#
#   - `{ message = ".*", group = "...その他" }`
#     分類漏れの受け皿であって特定の type を表すものではないので対象外。
#     （こちらも「英字のみ」の条件で自然に除外される）
grep -E 'message = .\^[A-Za-z]+.' "$CLIFF_TOML" \
  | grep -Eo '\^[A-Za-z]+' \
  | sed 's/^\^//' \
  | sort -u > "$tmpdir/cliff"

# --- scripts/check-commit-subject.sh --------------------------------------
# PATTERN の 1 番目の括弧グループ（type の alternation）だけを type 集合とみなす。
# ファイル中には他にも "(scope)" のような丸括弧を含む行（コメントの説明文や
# 実例コミットのスコープ部分）があるため、`^PATTERN=` の行だけに絞ってから抜き出す。
# 2 番目の括弧グループ（scope 用、`\(` で始まり数字や記号を含む）は
# 「英字と | だけ」という条件で自然に除外される。
grep '^PATTERN=' "$CHECK_SUBJECT" \
  | grep -Eo '\([a-zA-Z|]+\)' \
  | tr -d '()' \
  | tr '|' '\n' \
  | sort -u > "$tmpdir/subject"

# --- CLAUDE.md -------------------------------------------------------------
# 「| `type` | 説明 |」という行だけを type 集合とみなす。ヘッダ行（| type | 使う場面 |）や
# 区切り行（|---|---|）はバッククォートで囲まれていないので自然に除外される。
grep -E '^\| `[a-zA-Z]+` \|' "$CLAUDE_MD" \
  | grep -Eo '`[a-zA-Z]+`' \
  | tr -d '`' \
  | sort -u > "$tmpdir/claudemd"

# 抽出結果が空なら、記法が変わって抜き出せなくなっている可能性が高い。
# 「型が 0 個で一致」という無意味な成功を返さないよう、ここで止める。
for name in cliff subject claudemd; do
  if [ ! -s "$tmpdir/$name" ]; then
    printf '%s から type を 1 つも抽出できませんでした。記法が変わっていないか確認してください。\n' "$name" >&2
    exit 1
  fi
done

status=0

# compare_pair は 2 つの type 集合ファイルを突き合わせ、食い違いがあれば表示する。
compare_pair() {
  file_a=$1
  name_a=$2
  file_b=$3
  name_b=$4

  only_a=$(comm -23 "$file_a" "$file_b")
  only_b=$(comm -13 "$file_a" "$file_b")

  if [ -n "$only_a" ] || [ -n "$only_b" ]; then
    status=1
    printf '%s と %s で type 集合が一致しません。\n' "$name_a" "$name_b" >&2
    if [ -n "$only_a" ]; then
      printf '  %s にあって %s に無い: %s\n' "$name_a" "$name_b" "$(printf '%s' "$only_a" | tr '\n' ' ')" >&2
    fi
    if [ -n "$only_b" ]; then
      printf '  %s にあって %s に無い: %s\n' "$name_b" "$name_a" "$(printf '%s' "$only_b" | tr '\n' ' ')" >&2
    fi
  fi
}

compare_pair "$tmpdir/cliff" "$CLIFF_TOML" "$tmpdir/subject" "$CHECK_SUBJECT"
compare_pair "$tmpdir/cliff" "$CLIFF_TOML" "$tmpdir/claudemd" "$CLAUDE_MD"
compare_pair "$tmpdir/subject" "$CHECK_SUBJECT" "$tmpdir/claudemd" "$CLAUDE_MD"

if [ "$status" -ne 0 ]; then
  printf '\ntype を足したときは 3 箇所すべてを直してください: %s / %s / %s\n' \
    "$CLIFF_TOML" "$CHECK_SUBJECT" "$CLAUDE_MD" >&2
fi

exit "$status"
