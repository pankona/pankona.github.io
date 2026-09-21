// jev (TypeSafe の System One モデル) による軽い校正。
//
// 判定の中身は github.com/pankona/japanese-jev-lint (jjl) に切り出してあり、
// ここでは jjl の結果をエディタ (CodeMirror) 向けの形に変換するだけ。
// 判定を足す・閾値を変えるときは jjl 側の DefaultConfig を触る。
//
// claude -p の赤入れ (数分) とは別に、保存のたびに数秒で「引っかかりそうな文」に
// 波線を引くためのもの。jev は文章を生成せず確率だけ返すので、指摘文は作らない。
// 原稿本文が api.typesafe.ai に送られる点に注意 (TYPESAFE_API_KEY が無ければ無効)。
package main

import (
	"context"

	jjl "github.com/pankona/japanese-jev-lint"
)

type lintHit struct {
	From   int                `json:"from"` // UTF-16 オフセット (エディタの位置と一致させる)
	To     int                `json:"to"`
	Text   string             `json:"text"`
	Scores map[string]float64 `json:"scores"`
	Flags  []string           `json:"flags"` // 閾値を超えた判定キー。空なら波線なし
}

type lintResult struct {
	Enabled   bool        `json:"enabled"`
	Model     string      `json:"model,omitempty"`
	Tokens    int         `json:"tokens"`
	Sentences int         `json:"sentences"`
	Cached    int         `json:"cached"`
	Hits      []lintHit   `json:"hits"`
	Errors    int         `json:"errors"`
	Labels    []jjl.Label `json:"labels"` // 表示順つきの判定名 (フロントはこれで描く)
}

// linter は jjl.Linter の薄い包み。文ごとの結果キャッシュはプロセス内に持ち、
// 同じ文は保存のたびに聞き直さない (書き換えた文だけ課金・待ち時間が発生する)。
type linter struct {
	l *jjl.Linter
}

func newLinter() *linter {
	return &linter{l: jjl.New()}
}

func (l *linter) enabled() bool { return l.l.Client != nil }

// describe はプロンプト用に、波線の理由を「誤字? 0.65 / ですます調」の形で返す
func (l *linter) describe(flags []string, scores map[string]float64) string {
	return l.l.Config.Describe(flags, scores)
}

// lint は原稿全文を jjl に渡し、位置を UTF-16 に変換して返す。
func (l *linter) lint(ctx context.Context, doc string) lintResult {
	res := lintResult{Enabled: l.enabled(), Hits: []lintHit{}, Labels: l.l.Config.Labels()}
	if !l.enabled() {
		return res
	}
	src := []byte(doc)
	r := l.l.Lint(ctx, "", src)
	res.Model, res.Tokens, res.Sentences, res.Cached, res.Errors = r.Model, r.Tokens, len(r.Items), r.Cached, r.Errors
	res.Hits = toHits(src, r.Items)
	return res
}

// toHits は jjl の文ごとの結果を、UTF-16 オフセット付きの hit に変換する。
// 文は原稿順なので、オフセットは前回位置からの差分で求める (毎回先頭から数えない)。
func toHits(src []byte, items []jjl.Item) []lintHit {
	hits := []lintHit{}
	byteOff, u16 := 0, 0
	advance := func(to int) int {
		u16 += jjl.UTF16Offset(src[byteOff:], to-byteOff)
		byteOff = to
		return u16
	}
	for _, it := range items {
		if it.Err != nil {
			continue
		}
		from := advance(it.Sentence.Start.Offset)
		to := advance(it.Sentence.End.Offset)
		hits = append(hits, lintHit{From: from, To: to, Text: it.Sentence.Text, Scores: it.Scores, Flags: it.Flags})
	}
	return hits
}
