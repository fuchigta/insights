#!/bin/sh
# ドキュメントの陳腐化を防ぐ検査。
#
# 対応表（scripts/doc-sync.tsv）に書いた「コード ↔ ドキュメント」の組のうち、
# コード側だけがコミットされているものがあれば失敗する。片方だけ直して
# もう片方を忘れる、という取りこぼしを機械で止めるためのもの。
#
# 使い方:
#   scripts/check-doc-sync.sh --message <ファイル>  # ステージ済みの変更（commit-msg フック）
#   scripts/check-doc-sync.sh <git の範囲>          # コミット単位（CI）
#
# 逃げ道はコミットメッセージに書く。本文に次の行があるコミットは対象外になる。
#
#   Doc-Sync: skip 理由
#
# 環境変数ではなくコミットメッセージにしているのは、ローカルで通した判断が CI でも
# そのまま通る必要があるため。環境変数の逃げ道は CI に届かず、あとで理由の分からない
# 失敗になる。トレーラなら履歴に理由ごと残り、両方が同じものを見て判断できる。
set -eu

TABLE=${DOC_SYNC_TABLE:-scripts/doc-sync.tsv}

# 逃げ道のトレーラ。行頭の Doc-Sync: skip（大文字小文字は問わない）。
SKIP_PATTERN='^[Dd]oc-[Ss]ync:[[:space:]]*skip'

# 対応表が無い状態（表を導入する前のコミットを含む範囲を見ている等）では何も見ない。
if [ ! -f "$TABLE" ]; then
  exit 0
fi

mode=staged
range=
msgfile=
case ${1:-} in
  --message)
    msgfile=${2:-}
    ;;
  "")
    ;;
  *)
    mode=range
    range=$1
    ;;
esac

# SHA は check_commit が見ている対象。空ならステージ済みの変更を見る。
SHA=

# list_changed は対象に含まれる変更ファイルを出す。
list_changed() {
  if [ -n "$SHA" ]; then
    git show --name-only --format= --diff-filter=ACMR "$SHA"
  else
    git diff --cached --name-only --diff-filter=ACMR
  fi
}

# diff_of はそのファイルの差分行だけを出す（対応表 3 列目の正規表現を当てるため）。
diff_of() {
  if [ -n "$SHA" ]; then
    git show -U0 --format= "$SHA" -- "$1"
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

# check_commit は 1 コミット（またはステージ済みの変更）を検査する。
# 第 1 引数はコミットメッセージ本文、第 2 引数は失敗時の表示に使うラベル。
check_commit() {
  msg=$1
  label=$2

  if printf '%s\n' "$msg" | grep -Eq "$SKIP_PATTERN"; then
    return 0
  fi

  changed=$(list_changed)
  [ -n "$changed" ] || return 0

  local_status=0
  tab=$(printf '\t')

  # 対応表はリダイレクトで読む（パイプにするとサブシェルになり結果が戻らない）。
  while IFS="$tab" read -r pattern doc regex || [ -n "${pattern:-}" ]; do
    case "${pattern:-}" in "" | "#"*) continue ;; esac
    [ -n "${doc:-}" ] || continue

    # ドキュメント側も一緒に入っているなら、この行は満たされている。
    if printf '%s\n' "$changed" | grep -Fxq "$doc"; then
      continue
    fi

    hits=$(match_files "$pattern" "${regex:-}")
    [ -n "$hits" ] || continue

    if [ "$local_status" -eq 0 ]; then
      printf 'ドキュメントが置き去りになっている可能性があります%s。\n\n' "$label" >&2
      local_status=1
    fi
    printf '  %s を変更していますが、%s が一緒に入っていません:\n' "$pattern" "$doc" >&2
    printf '    - %s\n' $hits >&2
  done < "$TABLE"

  return "$local_status"
}

status=0

if [ "$mode" = "range" ]; then
  # マージコミットは自分では何も変更しないので対象外。
  # $range は分割させたいので引用しない（CI から "-1 HEAD" のような形も渡せる）。
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

対応表: scripts/doc-sync.tsv

  - ドキュメントも直す場合は、直して一緒にコミットしてください。
  - ドキュメントに影響しない変更なら、コミットメッセージの本文に次の行を入れてください。
      Doc-Sync: skip 理由をここに書く
    （ローカルのフックと CI は同じ判定をします。環境変数での回避はありません）
  - 対応そのものが誤っている（過剰・不足している）なら scripts/doc-sync.tsv を直してください。
MSG
fi

exit "$status"
