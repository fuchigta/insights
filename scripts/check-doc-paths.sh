#!/bin/sh
# ドキュメントが名指ししているコードのパスが実在するかを調べる。
#
# docs / README / CLAUDE.md はコードの場所を名指しで参照している。ファイルを動かすと
# その記述は静かに嘘になり、読んだ人（や AI）が存在しない場所を探すことになる。
# scripts/check-doc-sync.sh が守るのは「一緒に直したか」だけで、参照先の実在は守れない。
#
# 使い方:
#   scripts/check-doc-paths.sh [ドキュメント...]   # 省略時は既定のドキュメント一式
#
# 拾うのはバッククォートで囲まれた `internal/...` のようなパスだけ。地の文の中の
# それらしい文字列まで拾うと誤検知だらけになるため。まだ無いものを例として挙げている
# 参照は scripts/doc-paths-ignore.txt に理由付きで書いて除外する。
set -eu

IGNORE=${DOC_PATHS_IGNORE:-scripts/doc-paths-ignore.txt}

if [ "$#" -gt 0 ]; then
  docs=$*
else
  docs="README.md CLAUDE.md"
  for f in docs/*.md; do
    [ -f "$f" ] || continue
    docs="$docs $f"
  done
fi

status=0

for doc in $docs; do
  [ -f "$doc" ] || continue

  # バッククォートで囲まれた中身のうち、リポジトリ内のパスに見えるものだけを拾う。
  candidates=$(grep -ohE '`[^`]+`' "$doc" | tr -d '`' |
    grep -E '^(internal|cmd|scripts|[.]githooks|[.]github)/' | sort -u || true)

  for p in $candidates; do
    # 除外リストにあるものは飛ばす（コメント行と空行は無視）。
    if [ -f "$IGNORE" ] && grep -v '^[[:space:]]*#' "$IGNORE" | grep -Fxq "$p"; then
      continue
    fi

    case "$p" in
      *"*"*)
        # glob 表記（例: internal/cli/*.go）は 1 つ以上に一致すればよい。
        # shellcheck disable=SC2086
        set -- $p
        if [ ! -e "$1" ]; then
          printf '%s: `%s` に一致するファイルがありません\n' "$doc" "$p" >&2
          status=1
        fi
        ;;
      *)
        if [ ! -e "$p" ]; then
          printf '%s: `%s` が存在しません\n' "$doc" "$p" >&2
          status=1
        fi
        ;;
    esac
  done
done

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

ドキュメントが存在しない場所を指しています。次のどれかで直してください。

  - 参照先を実際の場所に書き換える（移動・改名したなら、ドキュメントも追随させる）
  - まだ無いものを例として挙げているなら、scripts/doc-paths-ignore.txt に理由付きで足す
MSG
fi

exit "$status"
