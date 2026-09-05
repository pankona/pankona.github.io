---
name: akaire-review
description: ブログ記事の PR を「赤入れ・鉛筆入れ」としてレビューし、GitHub にインラインコメントと講評を投稿する。GitHub Actions 上の @claude で記事 PR のレビュー・赤入れ・校閲を頼まれたとき、または手元で「この記事 PR を赤入れして」と言われたときに使う。
---

# akaire-review: 記事 PR の赤入れ

観点は **references/viewpoints.md** にすべて書いてある。このスキルは「PR 上でどう出力するか」だけを定める。
観点を変えたいときは viewpoints.md を直す (akaire ツールも同じファイルを読んでいる)。

## 手順

1. **viewpoints.md を読む**。`.claude/skills/akaire-review/references/viewpoints.md` を Read する
2. **PR の対象を把握する**。`gh pr view` / `gh pr diff` で変更された記事 (content/ 配下の .md) と画像を特定する。
   記事ファイルは全文を読む (差分だけでは文の成立や重複は判定できない)。画像は Read で開いて中身を見る
3. **viewpoints に従って指摘を洗い出す**。1〜5 章 (文の成立、文体、構成、通し、事実確認) を全部通す。
   textlint (8 章) は手元で動かすときだけ実行する。GitHub Actions 上の @claude では使えない
   (.github/workflows/claude.yml が npm ci を走らせておらず、Bash(npx textlint:*) も許可していない) ので、
   Actions 上では試さずにスキップする
4. **行に紐づく指摘はインラインコメントで投稿する**。
   `mcp__github_inline_comment__create_inline_comment` を **`confirmed: true`** で呼ぶ
   (confirmed なしだとバッファされて投稿されない)。
   - 1 指摘 1 コメント。本文の冒頭に 🔴 (red: 文の成立・写り込み) か ✏️ (pencil: 構成・言い回し) を置いて優先度を示す
   - 該当行が diff に含まれていない場合 (既存記事の未変更行など) だけ、講評側に行番号つきで書く
   - Actions 上では `gh api` は許可されていない。インライン投稿には必ず上記ツールを使う
   - **手元の対話セッション** (上記 MCP ツールが無い環境) では、代わりに
     `gh api repos/{owner}/{repo}/pulls/{number}/comments --method POST -f body=... -f commit_id=<head SHA> -f path=... -F line=... -f side=RIGHT`
     で投稿する。講評 (手順 5) は `gh pr comment` で新規コメントとして投稿する
5. **講評をまとめる**。進捗コメント (`mcp__github_comment__update_claude_comment`) を最終的に次の内容に更新する:
   - 読者としての感想・良かった点 (viewpoints 9 章)
   - 指摘の件数と red / pencil の内訳 (1 行)
   - 事実確認の結果 (数字・日付の整合、写り込みの有無、front matter) を 1〜3 行
   - インラインで書けなかった指摘 (行が diff 外のもの) があればそれだけ列挙
   - **インラインコメントの内容は再掲しない**。インラインを読めば分かることを講評で繰り返すと二重になる

## やらないこと

- 記事本文を書き換える、書き直した文章を丸ごと提示する (viewpoints 0 章)
- PR を approve / request changes する (インラインコメントツールにその機能はない。使わない)
- 指摘を水増しする。指摘がなければ「文の成立・事実確認ともに問題なし」と講評に書いて終わる
