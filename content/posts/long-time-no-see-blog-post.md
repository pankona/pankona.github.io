---
title: "ブログを何日書いてないかをリマインドする仕組みを作った"
date: 2021-03-04T00:38:25+09:00
draft: false
categories: ["その他"]
---

今年こそはいっぱいブログ書くんだって宣言するのはいいんだけど、結局なかなかコンスタントに書き続けることができず、気付いたら半年過ぎているみたいな人生を送っている。ので、それを打破すべく「最後にブログを書いてから n 日過ぎました」というリマインダーを仕込むことにしたんだ。

<!--more-->

こんなようなリマインダーがくる。

![image](https://user-images.githubusercontent.com/6533008/109830663-0b2ca500-7c82-11eb-8c91-65134940b32c.png?s=10)

仕組みは以下。

- 最後にコミットした日時を git log を使って確認する
- 所定の日数過ぎていたら (7日間とか) Slack に通知する
  - ここの最終投稿からの日数計算は Go で CLI を作った
- GitHub Actions で cron job を仕込み、毎朝 7:00 に確認するようにする

できたものはこれ。ちゃんと動いている。
https://github.com/pankona/pankona.github.io/blob/hugo/.github/workflows/notify_long_time_no_see.yaml

## そもそも人間 (自分を含む) を信じるな

去年の自分は「ブログをいっぱい書くぞ」って気合を入れたっきりそれで終わりだった。記事は生えてこなかった。
そもそも、ブログをいっぱい書くぞって気合を入れただけでどうしてブログ記事が生えてくると思った？っていうことである。なんとブログ記事は書かないと生えてこない。書かねばならない。そして人間は書くのを忘れるし、書くことを思い出したとしてもネタがないだの時間がないだの言っていったん頭の片隅に追いやってしまい、そして一晩寝たら忘れている。気合だけで達成しようというのがそもそもあまりにも脆弱であった。

ということで、やはりマシンに頼らねばならない。こういうリマインドの類などで圧をかけるような仕事はマシンにやらせるに限る。そして GitHub Actions が cron job の役目を果たしてくれるのは本当に便利。これで今年こそブログをいっぱい書くんだ！
