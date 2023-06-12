---
title: "ブログ生成を Octopress から Hugo に移行"
date: 2018-01-02T13:32:34+09:00
categories: [その他, Octopress, Hugo]
---

いままで本ブログは Octopress を使って生成していたのであるが、
久々に動かしたり別のPCでやったりするときに上手く動かないことがままあり、まあまあストレスであった。

ということで、爆速サイトジェネレータとして高名な Hugo を使うように変えてみた。
なるほど、噂に違わぬ爆速サイトジェネレーティングである。しばらく使ってみよう。

Octopress から Hugo への移行は概ねすんなり済んだ。以下のような作業を行った。
テーマ選びに大半の時間を費やしたものの、概ね丸一日の作業となった (7.5h くらい)

* Hugo の使い方確認 (ドキュメントを見る等して)
* Hugo でサイトを生成し、Octopress 時代に生成したポスト一式をコピー
  * ファイル名、各ポストのヘッダー等は若干適合しない部分があったので修正を加えた
* 各記事のパーマリンクを従来サイトと同じになるように設定
* Hugo の設定 (config.toml) に各種サイトの設定を記載 (ブログ名、Twitter、GitHub へのリンク等)
* BlackFriday をちょっとだけ設定し、hardLineBreak が有効になるように
* テーマ選び。ここがめちゃめちゃ時間が掛かった。結局シンプルなの選んである程度カスタマイズする方針に

以下はもう少しやっておきたいところ。

* フォントがダサい。というか見にくい。のでもうちょっと目に優しいやつを選ぶ
* サイトマップというか記事一覧というか、みたいのが欲しい
* favicon 設定
* about.html 的なやつを置く
* (あまり利用の実績がないが) SNS へのシェアボタンを追加。ツイッターだけ置いておいた。はてぶも欲しい。
* (まったく利用の実績がないが) disqus を置く。利用実績がまったくないところからして置かなくていいかも。優先度低。


昨年は勉強会レポートと ZenPad レビューと、みたいなどちらかというと受動的な強いられポストが多かったため、
今年はもっと能動的に日常的に息を吐くように記事を更新していきたい所存。今年もやっていくぞー。