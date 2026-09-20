// jev (TypeSafe の System One モデル) による軽い校正。
//
// claude -p の赤入れ (数分) とは別に、保存のたびに数秒で「引っかかりそうな文」に
// 波線を引くためのもの。jev は文章を生成せず確率だけ返すので、指摘文は作らない。
// 原稿本文が api.typesafe.ai に送られる点に注意 (TYPESAFE_API_KEY が無ければ無効)。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const jevEndpoint = "https://api.typesafe.ai/v1/systemone"

// jev に聞く判定 (すべて Noul = yes の確率) と、波線を引く閾値。
// 閾値は 2026-09-20 の既存記事 3 本での実験値 (絶対値は文によってぶれるので、
// 使いながら調整する前提)。viewpoints.md の 1 章 (文の成立) と 3 章 (読みやすさ) から、
// jev が実際に拾えたものだけを採用している。「係り受けの曖昧さ」「指示語の指し先」は
// 試したが信号が弱かったので入れていない。
var lintChecks = []struct {
	Key       string
	Label     string // フロントの表示名
	Threshold float64
	MinRunes  int // 空白を除いた字数がこれ未満の文には出さない (0 = 制限なし)
}{
	{"typo", "誤字?", 0.5, 0},
	{"twisted", "主述のねじれ?", 0.5, 0},
	// jev の「長い」は字数でなく節の詰め込みを見るので、括弧の挿入や語の反復がある
	// 短い文にも反応する。字数は規則で決められるので下限を置く (本物は 70 字以上だった)
	{"long", "一文が長い", 0.6, 70},
	{"repeat", "同じ語句の繰り返し", 0.55, 0},
}

// 正規表現で拾うもの (jev が苦手、または規則で十分なもの)。
// ですます調は jev がである文にも高い値を返すので使わない。
var lintRegexChecks = []struct {
	Key   string
	Label string
	Re    *regexp.Regexp
	Min   int // マッチ数がこれ以上で波線
}{
	{"desumasu", "ですます調", regexp.MustCompile(`(です|ます|ました|でした|ましょう|ません|ですね|ますね)[。！？!?」)]*\s*$`), 1},
	{"double_ga", "逆接が二重 (〜が、〜が、)", regexp.MustCompile(`(が|けど|けれど|けれども|ものの)、`), 2},
}

var lintQuestions = map[string]any{
	"typo": map[string]any{
		"type":         "noul",
		"instructions": "`sentence` に誤字・脱字・漢字の変換ミス・文字の重複や抜けがあるか? くだけた言い回しや口語 (「やめらんねぇ」等) は誤字ではない。",
		"criteria": map[string]any{
			"true":  "明らかな誤字・脱字・変換ミスがある",
			"false": "表記は正しい (口語表現・ひらがな表記は誤りとしない)",
		},
	},
	"twisted": map[string]any{
		"type":         "noul",
		"instructions": "`sentence` は日本語の一文である。主語と述語が対応せず、文としてねじれている、または述語が着地していない (「〜というのは、〜がある」のような重なり、文末で主語がすり替わる、譲歩や否定の二重化) か?",
		"criteria": map[string]any{
			"true":  "主述がねじれている、述語が未完・重複している、否定や譲歩が二重になっている",
			"false": "口語的・くだけた表現でも、主語と述語は素直に対応しており文として成立している",
		},
	},
	"long": map[string]any{
		"type":         "noul",
		"instructions": "`sentence` は、読点や「〜し」「〜が」「〜ので」で節をつなぎ続けて一文に話題や動作が三つ以上詰め込まれており、読者が途中で息切れするか? (読者は `prev`・`next` も見ている)",
		"criteria": map[string]any{
			"true":  "一文に節が多く、区切って二文以上にした方が読みやすい",
			"false": "節の数は適度で、一文のまま読める",
		},
	},
	"repeat": map[string]any{
		"type":         "noul",
		"instructions": "`sentence` の中で、同じ語句 (名詞・動詞・言い回し) が二度以上出てきて冗長に感じるか? 強調のための意図的な繰り返しや口癖は除く。",
		"criteria": map[string]any{
			"true":  "同じ語句が一文の中で繰り返されていて、片方を削るか言い換えた方がよい",
			"false": "繰り返しはない、または意図的な強調",
		},
	},
}

type lintSentence struct {
	From, To int // UTF-16 オフセット (エディタの位置と一致させる)
	Text     string
	Prev     string
	Next     string
}

// lintScores は判定キー → yes の確率
type lintScores map[string]float64

type lintHit struct {
	From   int        `json:"from"`
	To     int        `json:"to"`
	Text   string     `json:"text"`
	Scores lintScores `json:"scores"`
	Flags  []string   `json:"flags"` // 閾値を超えた判定キー。空なら波線なし
}

type lintLabel struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type lintResult struct {
	Enabled   bool        `json:"enabled"`
	Model     string      `json:"model,omitempty"`
	Tokens    int         `json:"tokens"`
	Sentences int         `json:"sentences"`
	Cached    int         `json:"cached"`
	Hits      []lintHit   `json:"hits"`
	Errors    int         `json:"errors"`
	Labels    []lintLabel `json:"labels"` // 表示順つきの判定名 (フロントはこれで描く)
}

// linter は jev への問い合わせと、文ごとの結果キャッシュを持つ。
// 同じ文は保存のたびに聞き直さない (書き換えた文だけ課金・待ち時間が発生する)。
type linter struct {
	apiKey string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]lintScores // key: prev+"\x00"+sentence+"\x00"+next
}

func newLinter() *linter {
	return &linter{
		apiKey: os.Getenv("TYPESAFE_API_KEY"),
		client: &http.Client{Timeout: 30 * time.Second},
		cache:  map[string]lintScores{},
	}
}

func (l *linter) enabled() bool { return l.apiKey != "" }

var (
	reLintFence   = regexp.MustCompile("^\\s*(```|~~~)")
	reLintSkip    = regexp.MustCompile(`^\s*(#|!\[|<!--|\[[^\]]*\]:\s|\||{{<|{{%|---\s*$|https?://\S+\s*$)`)
	reLintPrefix  = regexp.MustCompile(`^(\s*([-*+]|\d+\.)\s+|\s*>\s*)+`)
	reLintInline  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)|\[([^\]]+)\]\([^)]*\)|` + "`[^`]*`")
	reLintEndMark = regexp.MustCompile(`[。！？!?]`)
)

// splitSentences は Markdown 原稿を文に割る。front matter、コードブロック、見出し、
// 画像行などは飛ばす。返す位置は UTF-16 単位 (CodeMirror の doc 位置)。
func splitSentences(doc string) []lintSentence {
	var out []lintSentence
	off := 0 // 現在行の先頭の UTF-16 オフセット
	inFence, inFront := false, false
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		lineStart := off
		off += utf16Len(line) + 1
		if i == 0 && strings.TrimSpace(line) == "---" {
			inFront = true
			continue
		}
		if inFront {
			if strings.TrimSpace(line) == "---" {
				inFront = false
			}
			continue
		}
		if reLintFence.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence || reLintSkip.MatchString(line) {
			continue
		}
		body := line
		start := 0
		if m := reLintPrefix.FindStringIndex(line); m != nil {
			body, start = line[m[1]:], m[1]
		}
		base := lineStart + utf16Len(line[:start])
		// 文末記号で区切る。記号の直後で切り、引用符閉じ等は次の文の先頭に残る
		// 程度の雑さで良しとする (波線の位置が数文字ずれるだけ)
		segStart := 0
		flush := func(end int) {
			seg := body[segStart:end]
			trimmed := strings.TrimSpace(seg)
			if trimmed != "" {
				lead := strings.Index(seg, trimmed)
				plain := strings.TrimSpace(reLintInline.ReplaceAllString(trimmed, "$1"))
				// 読点やコロンで終わる行は箇条書きなどへ続く導入で、それ単体では
				// 述語が無いのが正常。断片として jev に渡すと「ねじれ」に誤判定される
				if len([]rune(plain)) >= 6 && !strings.HasSuffix(plain, "、") &&
					!strings.HasSuffix(plain, "：") && !strings.HasSuffix(plain, ":") {
					out = append(out, lintSentence{
						From: base + utf16Len(body[:segStart+lead]),
						To:   base + utf16Len(body[:segStart+lead]) + utf16Len(trimmed),
						Text: plain,
					})
				}
			}
			segStart = end
		}
		for _, m := range reLintEndMark.FindAllStringIndex(body, -1) {
			// 「」や () の中の ？！ は文末ではない (「〜は？」って質問して、など)
			if bracketDepth(body[segStart:m[0]]) == 0 {
				flush(m[1])
			}
		}
		flush(len(body))
	}
	for i := range out {
		if i > 0 {
			out[i].Prev = out[i-1].Text
		}
		if i+1 < len(out) {
			out[i].Next = out[i+1].Text
		}
	}
	return out
}

// bracketDepth は s の中で開いたまま閉じていない括弧の数を返す
func bracketDepth(s string) int {
	d := 0
	for _, r := range s {
		switch r {
		case '「', '『', '（', '(', '"', '“':
			d++
		case '」', '』', '）', ')', '”':
			if d > 0 {
				d--
			}
		}
	}
	return d
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func (l *linter) ask(ctx context.Context, s lintSentence) (lintScores, string, int, error) {
	body, _ := json.Marshal(map[string]any{
		"model":     "jev-latest",
		"state":     map[string]string{"prev": s.Prev, "sentence": s.Text, "next": s.Next},
		"questions": lintQuestions,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", jevEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+l.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := l.client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, "", 0, fmt.Errorf("jev: %d: %s", res.StatusCode, rb)
	}
	var r struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Noul float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &r); err != nil {
		return nil, "", 0, err
	}
	sc := lintScores{}
	for _, c := range lintChecks {
		sc[c.Key] = r.Answers[c.Key].Noul
	}
	return sc, r.Model, r.Usage.InputTokens, nil
}

// lint は原稿全文を文に割って jev に並列で問い合わせ、文ごとの判定を返す。
func (l *linter) lint(ctx context.Context, doc string) lintResult {
	res := lintResult{Enabled: l.enabled(), Hits: []lintHit{}}
	for _, c := range lintChecks {
		res.Labels = append(res.Labels, lintLabel{c.Key, c.Label})
	}
	for _, c := range lintRegexChecks {
		res.Labels = append(res.Labels, lintLabel{c.Key, c.Label})
	}
	if !l.enabled() {
		return res
	}
	sents := splitSentences(doc)
	res.Sentences = len(sents)
	scores := make([]lintScores, len(sents))
	errs := make([]error, len(sents))
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		sem = make(chan struct{}, 16)
	)
	for i, s := range sents {
		key := s.Prev + "\x00" + s.Text + "\x00" + s.Next
		l.mu.Lock()
		sc, ok := l.cache[key]
		l.mu.Unlock()
		if ok {
			scores[i] = sc
			res.Cached++
			continue
		}
		wg.Add(1)
		go func(i int, s lintSentence, key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sc, model, tokens, err := l.ask(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[i] = err
				return
			}
			scores[i] = sc
			res.Model, res.Tokens = model, res.Tokens+tokens
			l.mu.Lock()
			l.cache[key] = sc
			l.mu.Unlock()
		}(i, s, key)
	}
	wg.Wait()
	for i, s := range sents {
		if errs[i] != nil {
			res.Errors++
			continue
		}
		h := lintHit{From: s.From, To: s.To, Text: s.Text, Scores: scores[i], Flags: []string{}}
		runes := len([]rune(strings.Join(strings.Fields(s.Text), "")))
		for _, c := range lintChecks {
			if h.Scores[c.Key] >= c.Threshold && runes >= c.MinRunes {
				h.Flags = append(h.Flags, c.Key)
			}
		}
		for _, c := range lintRegexChecks {
			if len(c.Re.FindAllStringIndex(s.Text, -1)) >= c.Min {
				h.Flags = append(h.Flags, c.Key)
			}
		}
		res.Hits = append(res.Hits, h)
	}
	return res
}
