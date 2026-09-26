// jev (TypeSafe の System One モデル) による軽い校正。
//
// 判定の中身は github.com/pankona/japanese-jev-lint (jjl) に切り出してあり、
// ここでは jjl の結果をエディタ (CodeMirror) 向けの形に変換するだけ。
// 判定の既定値は jjl 側の DefaultConfig。閾値や質問文はエディタの「⚙ jev」から
// 差し替えられ、-lint-config のファイル (既定: ユーザー設定ディレクトリ) に保存する。
//
// claude -p の赤入れ (数分) とは別に、保存のたびに数秒で「引っかかりそうな文」に
// 波線を引くためのもの。jev は文章を生成せず確率だけ返すので、指摘文は作らない。
// 原稿本文が api.typesafe.ai に送られる点に注意 (TYPESAFE_API_KEY が無ければ無効)。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

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

// lintCheck / lintRegexCheck は jjl の判定に「無効化」フラグを足したもの。
// jjl の Config には on/off が無く、外すと定義ごと消えるので、設定画面で
// 一時的に切っても質問文や閾値を失わないようにこちらで持つ。
type lintCheck struct {
	jjl.Check
	Disabled bool `json:"disabled,omitempty"`
}

type lintRegexCheck struct {
	jjl.RegexCheck
	Disabled bool `json:"disabled,omitempty"`
}

// lintSettings は設定画面で編集する jev の設定一式。ファイルにもこの形で保存する。
type lintSettings struct {
	Model       string           `json:"model"`
	Checks      []lintCheck      `json:"checks"`
	RegexChecks []lintRegexCheck `json:"regex_checks"`
}

func defaultLintSettings() lintSettings {
	return settingsFromConfig(jjl.DefaultConfig())
}

func settingsFromConfig(c jjl.Config) lintSettings {
	s := lintSettings{Model: c.Model}
	for _, ch := range c.Checks {
		s.Checks = append(s.Checks, lintCheck{Check: ch})
	}
	for _, rc := range c.RegexChecks {
		s.RegexChecks = append(s.RegexChecks, lintRegexCheck{RegexCheck: rc})
	}
	return s
}

// config は無効化した判定を除いた、jjl に渡す形の Config を返す。
func (s lintSettings) config() jjl.Config {
	c := jjl.Config{Model: s.Model}
	for _, ch := range s.Checks {
		if !ch.Disabled {
			c.Checks = append(c.Checks, ch.Check)
		}
	}
	for _, rc := range s.RegexChecks {
		if !rc.Disabled {
			c.RegexChecks = append(c.RegexChecks, jjl.RegexCheck{
				Key: rc.Key, Label: rc.Label, Pattern: rc.Pattern, Min: rc.Min,
			})
		}
	}
	return c
}

// validate は設定画面から来た値の体裁を確かめる (閾値の範囲・キーの重複・正規表現)。
func (s lintSettings) validate() error {
	if s.Model == "" {
		return errors.New("model が空")
	}
	seen := map[string]bool{}
	for _, ch := range s.Checks {
		if ch.Key == "" || seen[ch.Key] {
			return fmt.Errorf("判定キーが空か重複: %q", ch.Key)
		}
		seen[ch.Key] = true
		if ch.Threshold < 0 || ch.Threshold > 1 {
			return fmt.Errorf("%s: threshold は 0〜1 (got %v)", ch.Key, ch.Threshold)
		}
		if ch.MinRunes < 0 {
			return fmt.Errorf("%s: min_runes は 0 以上", ch.Key)
		}
		if ch.Question.Instructions == "" || ch.Question.True == "" || ch.Question.False == "" {
			return fmt.Errorf("%s: 質問文 (instructions / true / false) が空", ch.Key)
		}
	}
	for _, rc := range s.RegexChecks {
		if rc.Key == "" || seen[rc.Key] {
			return fmt.Errorf("判定キーが空か重複: %q", rc.Key)
		}
		seen[rc.Key] = true
		if _, err := regexp.Compile(rc.Pattern); err != nil {
			return fmt.Errorf("%s: 正規表現が不正: %v", rc.Key, err)
		}
		if rc.Min < 1 {
			return fmt.Errorf("%s: min は 1 以上", rc.Key)
		}
	}
	return nil
}

// linter は jjl.Linter の薄い包み。文ごとの結果キャッシュはプロセス内に持ち、
// 同じ文は保存のたびに聞き直さない (書き換えた文だけ課金・待ち時間が発生する)。
// 設定は設定画面から差し替えられるので mu で守る。閾値や min_runes は jev の
// 応答をキャッシュしたまま判定し直せる (問い合わせのキャッシュキーは質問文とモデルだけ)。
type linter struct {
	mu       sync.RWMutex
	l        *jjl.Linter
	settings lintSettings
	path     string // 設定の保存先。空なら保存しない
}

func newLinter(path string) *linter {
	l := &linter{l: jjl.New(), settings: defaultLintSettings(), path: path}
	if path == "" {
		return l
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return l // 無ければ既定
	}
	var s lintSettings
	if err := json.Unmarshal(b, &s); err != nil {
		fmt.Fprintf(os.Stderr, "jev 設定 %s を読めないので既定を使う: %v\n", path, err)
		return l
	}
	if err := s.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "jev 設定 %s が不正なので既定を使う: %v\n", path, err)
		return l
	}
	l.settings = s
	l.l.Config = s.config()
	return l
}

func (l *linter) enabled() bool { return l.l.Client != nil }

// getSettings は現在の設定のコピーを返す。
func (l *linter) getSettings() lintSettings {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.settings
}

// setSettings は設定を検証して差し替え、ファイルにも書く。
func (l *linter) setSettings(s lintSettings) error {
	if err := s.validate(); err != nil {
		return err
	}
	l.mu.Lock()
	l.settings = s
	l.l.Config = s.config()
	l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(l.path, append(b, '\n'), 0o644)
}

// resetSettings は既定に戻し、保存ファイルを消す。
func (l *linter) resetSettings() error {
	l.mu.Lock()
	l.settings = defaultLintSettings()
	l.l.Config = l.settings.config()
	l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// describe はプロンプト用に、波線の理由を「誤字? 0.65 / ですます調」の形で返す
func (l *linter) describe(flags []string, scores map[string]float64) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.l.Config.Describe(flags, scores)
}

// lint は原稿全文を jjl に渡し、位置を UTF-16 に変換して返す。
func (l *linter) lint(ctx context.Context, doc string) lintResult {
	l.mu.RLock()
	defer l.mu.RUnlock()
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
