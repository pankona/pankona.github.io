#!/usr/bin/env bash
# GitHub PR のインラインレビューコメントを akaire の指摘ファイル
# (原稿ごとの .<原稿名>.akaire.json) に取り込み、対象の原稿ファイルの
# うち手元にないものを PR のブランチから取得する。
#
# マージ方式: 既存の指摘ファイルのエントリ (dismissed 状態やローカルの指摘を含む)
# はそのまま残し、まだ無い id のコメントだけを追加する。既存の原稿ファイルも上書きしない。
#
# 使い方: ./fetch-pr.sh <owner/repo> <pr番号> <出力ディレクトリ>
# 例:     ./fetch-pr.sh pankona/pankona.github.io 447 ./demo-data
set -euo pipefail

repo=$1
pr=$2
out=$3
mkdir -p "$out"

branch=$(gh pr view "$pr" --repo "$repo" --json headRefName --jq .headRefName)
comments_file=$(mktemp)
trap 'rm -f "$comments_file"' EXIT
gh api "repos/$repo/pulls/$pr/comments" --paginate > "$comments_file"

# コメント対象のファイルのうち、手元にないものだけ PR ブランチから取得
# (既存ファイルは編集中の作業コピーかもしれないので上書きしない)
python3 -c 'import json,sys; print("\n".join(sorted({c["path"] for c in json.load(open(sys.argv[1]))})))' "$comments_file" |
  while read -r path; do
    [ -n "$path" ] || continue
    dest="$out/$path"
    if [ -e "$dest" ]; then
      echo "skipped (already exists): $path"
      continue
    fi
    mkdir -p "$(dirname "$dest")"
    gh api "repos/$repo/contents/$path?ref=$branch" --jq .content | base64 -d > "$dest"
    echo "fetched: $path"
  done

# コメント → 原稿ごとの指摘ファイル (マージ: 既存エントリは温存し、新しい id だけ追加)
python3 - "$out" "$comments_file" <<'EOF'
import collections, json, os, re, sys

out = sys.argv[1]
comments = json.load(open(sys.argv[2]))

by_path = collections.defaultdict(list)
for c in comments:
    by_path[c["path"]].append(c)

for path, cs in sorted(by_path.items()):
    d, b = os.path.split(path)
    ann = os.path.join(out, d, "." + re.sub(r"\.md$", "", b) + ".akaire.json")
    existing = []
    if os.path.exists(ann):
        with open(ann) as f:
            existing = json.load(f).get("annotations", [])
    known = {a["id"] for a in existing}

    added = 0
    for c in cs:
        if str(c["id"]) in known:
            continue
        hunk = c.get("diff_hunk") or ""
        lines = hunk.split("\n")
        # コメント対象行 = hunk の最終行。先頭の diff 記号 (+/-/空白) を落とす
        quote = lines[-1][1:] if lines else ""
        body = c["body"]
        kind = "pencil" if "medium-priority" in body or "low-priority" in body else "red"
        body = re.sub(r'!\[[^\]]*\]\([^)]*\)\s*', '', body).strip()
        body = body.split("<details>")[0].strip()
        existing.append({
            "id": str(c["id"]),
            "file": c["path"],
            "quote": quote,
            "body": body,
            "author": c["user"]["login"],
            "kind": kind,
            "url": c["html_url"],
        })
        added += 1

    os.makedirs(os.path.dirname(ann), exist_ok=True)
    with open(ann, "w") as f:
        json.dump({"annotations": existing}, f, ensure_ascii=False, indent=2)
    print(f"{added} new / {len(existing)} total annotations -> {ann}")
EOF
