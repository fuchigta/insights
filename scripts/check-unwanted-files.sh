#!/bin/sh
# コミットしてはいけないものが混ざっていないかを調べる。
#
# このリポジトリが扱う題材はセッションログなので、動作確認の過程で他人との会話を含む
# JSONL や、利用者のデータベース・レポートが作業ツリーへ入り込みやすい。公開リポジトリに
# 入れてしまうと取り返しがつかないため、.gitignore（追加させない）に加えて、
# ここでも止める（.gitignore は `git add -f` や、すでに追跡中のファイルには効かない）。
#
# 使い方:
#   scripts/check-unwanted-files.sh --message <ファイル>  # ステージ済み（commit-msg フック）
#   scripts/check-unwanted-files.sh <git の範囲>          # コミット単位（CI）
#
# 逃げ道はコミットメッセージ本文に書く（doc-sync と同じ考え方。ローカルで通した判断が
# CI でもそのまま通る必要があるため）。
#
#   Unwanted-Files: skip 理由
set -eu

# 大きすぎるファイルの閾値。現在の追跡ファイルの最大は 44KB 程度なので、1MB を超えるものは
# まず生成物か取り込んだデータであり、意図せず入ったと考えてよい。
MAX_BYTES=${MAX_FILE_BYTES:-1048576}

SKIP_PATTERN='^[Uu]nwanted-[Ff]iles:[[:space:]]*skip'

mode=staged
range=
msgfile=
case ${1:-} in
  --message) msgfile=${2:-} ;;
  "") ;;
  *) mode=range; range=$1 ;;
esac

SHA=

list_changed() {
  if [ -n "$SHA" ]; then
    git show --name-only --format= --diff-filter=ACMR "$SHA"
  else
    git diff --cached --name-only --diff-filter=ACMR
  fi
}

# size_of はそのファイルの（インデックス／コミット上の）バイト数を返す。
# 作業ツリーではなく git が持っている中身を見るため、あとから消しても検知できる。
size_of() {
  if [ -n "$SHA" ]; then
    git cat-file -s "$SHA:$1" 2>/dev/null || echo 0
  else
    git cat-file -s ":$1" 2>/dev/null || echo 0
  fi
}

check_commit() {
  msg=$1
  label=$2

  if printf '%s\n' "$msg" | grep -Eq "$SKIP_PATTERN"; then
    return 0
  fi

  local_status=0

  for f in $(list_changed); do
    [ -n "$f" ] || continue

    reason=
    case "$f" in
      *.db | *.db-wal | *.db-shm) reason="利用者のデータベース" ;;
      *.jsonl) reason="取り込み元のセッションログ（他人との会話が入りうる）" ;;
      .insights/* | */.insights/*) reason="insights の実データ置き場" ;;
    esac

    if [ -z "$reason" ]; then
      size=$(size_of "$f")
      if [ "$size" -gt "$MAX_BYTES" ]; then
        reason="$size バイト（上限 $MAX_BYTES バイト）"
      fi
    fi

    [ -n "$reason" ] || continue

    if [ "$local_status" -eq 0 ]; then
      printf 'コミットしてはいけないものが含まれています%s。\n\n' "$label" >&2
      local_status=1
    fi
    printf '  %s: %s\n' "$f" "$reason" >&2
  done

  return "$local_status"
}

status=0

if [ "$mode" = "range" ]; then
  # shellcheck disable=SC2086
  for sha in $(git rev-list --no-merges $range); do
    SHA=$sha
    check_commit "$(git log -1 --format=%B "$sha")" "（$(git log -1 --format="%h %s" "$sha")）" || status=1
  done
else
  SHA=
  msg=""
  if [ -n "$msgfile" ] && [ -f "$msgfile" ]; then
    msg=$(cat "$msgfile")
  fi
  check_commit "$msg" "" || status=1
fi

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

  - 誤って入ったものなら `git rm --cached <ファイル>` で外してください。
  - 意図して入れる必要があるなら、コミットメッセージの本文に理由を書いてください。
      Unwanted-Files: skip 理由をここに書く
MSG
fi

exit "$status"
