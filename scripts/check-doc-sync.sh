#!/bin/sh
# ドキュメントの陳腐化を防ぐ検査。
#
# 対応表（scripts/doc-sync.tsv）に書いた「コード ↔ ドキュメント」の組のうち、
# コード側だけがコミット対象になっているものがあれば失敗する。片方だけ直して
# もう片方を忘れる、という取りこぼしを機械で止めるためのもの。
#
# 使い方:
#   scripts/check-doc-sync.sh              # ステージ済みの変更を見る（pre-commit フック）
#   scripts/check-doc-sync.sh <git の範囲> # コミット範囲を見る（CI から使う場合）
#
# 回避: ドキュメントに影響しない変更なら SKIP_DOC_SYNC=1 を付ける。
set -eu

TABLE=${DOC_SYNC_TABLE:-scripts/doc-sync.tsv}

if [ "${SKIP_DOC_SYNC:-}" = "1" ]; then
  exit 0
fi

# 対応表が無い状態（表を導入する前のコミットを checkout している等）では何も見ない。
if [ ! -f "$TABLE" ]; then
  exit 0
fi

range=${1:-}
if [ -n "$range" ]; then
  changed=$(git diff --name-only "$range")
else
  changed=$(git diff --cached --name-only --diff-filter=ACMR)
fi

[ -n "$changed" ] || exit 0

# diff_of はそのファイルの差分行だけを出す（3 列目の正規表現を当てるため）。
diff_of() {
  if [ -n "$range" ]; then
    git diff -U0 "$range" -- "$1"
  else
    git diff --cached -U0 -- "$1"
  fi
}

# match_files は pattern に一致し、かつ（regex があれば）その差分が regex に
# 一致するファイルを出す。標準出力で返すので、呼び出し側はコマンド置換で受ける。
match_files() {
  pat=$1
  re=$2
  printf '%s\n' "$changed" | while IFS= read -r f; do
    [ -n "$f" ] || continue
    # テストは利用者に見える面を定義しないので、対応表の対象から外す。
    case "$f" in *_test.go) continue ;; esac
    case "$f" in $pat) ;; *) continue ;; esac
    if [ -n "$re" ]; then
      diff_of "$f" | grep -Eq "$re" || continue
    fi
    printf '%s\n' "$f"
  done
}

status=0
tab=$(printf '\t')

# 対応表はリダイレクトで読む（パイプにするとサブシェルになり status が戻らない）。
while IFS="$tab" read -r pattern doc regex || [ -n "${pattern:-}" ]; do
  case "${pattern:-}" in "" | "#"*) continue ;; esac
  [ -n "${doc:-}" ] || continue

  # ドキュメント側も一緒にコミットされているなら、この行は満たされている。
  if printf '%s\n' "$changed" | grep -Fxq "$doc"; then
    continue
  fi

  hits=$(match_files "$pattern" "${regex:-}")
  [ -n "$hits" ] || continue

  if [ "$status" -eq 0 ]; then
    echo "ドキュメントが置き去りになっている可能性があります。" >&2
    echo >&2
  fi
  status=1
  printf '  %s を変更していますが、%s がコミット対象に入っていません:\n' "$pattern" "$doc" >&2
  printf '    - %s\n' $hits >&2
done < "$TABLE"

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

対応表: scripts/doc-sync.tsv

  - ドキュメントも直す場合は、直して git add してからコミットし直してください。
  - ドキュメントに影響しない変更なら、次のように回避できます。
      SKIP_DOC_SYNC=1 git commit ...
  - 対応そのものが誤っている（過剰・不足している）なら scripts/doc-sync.tsv を直してください。
MSG
fi

exit "$status"
