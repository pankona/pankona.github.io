package main

import (
	"strings"
	"testing"
)

func TestSplitSentences(t *testing.T) {
	doc := strings.Join([]string{
		"---",
		"title: テスト",
		"---",
		"",
		"# 見出し",
		"",
		"最初の文である。二つ目の文だ！三つ目はどうか？",
		"- 箇条書きの文である。",
		"```",
		"code。code。",
		"```",
		"![alt](img.png)",
		"[リンク](https://example.com)を含む文である。",
		"短い。",
		"これはですます調の文です。",
	}, "\n")
	got := splitSentences(doc)
	want := []string{
		"最初の文である。", "二つ目の文だ！", "三つ目はどうか？",
		"箇条書きの文である。",
		"リンクを含む文である。",
		"これはですます調の文です。",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Text != w {
			t.Errorf("[%d] text = %q, want %q", i, got[i].Text, w)
		}
	}
	// 位置は UTF-16 オフセットで、原稿の該当箇所を指す
	runes := utf16Runes(doc)
	if s := string(runes[got[1].From:got[1].To]); s != "二つ目の文だ！" {
		t.Errorf("offset of [1] points to %q", s)
	}
	if s := string(runes[got[3].From:got[3].To]); s != "箇条書きの文である。" {
		t.Errorf("offset of [3] points to %q", s)
	}
	if got[1].Prev != "最初の文である。" || got[1].Next != "三つ目はどうか？" {
		t.Errorf("context of [1] = %q / %q", got[1].Prev, got[1].Next)
	}
	re := lintRegexChecks[0].Re // desumasu
	if !re.MatchString(got[5].Text) || re.MatchString(got[0].Text) {
		t.Errorf("desumasu detection wrong")
	}
	ga := lintRegexChecks[1] // double_ga
	if n := len(ga.Re.FindAllStringIndex("詳しくは分からないが、どうやら LLM ではあるが、テキストは出せない。", -1)); n < ga.Min {
		t.Errorf("double_ga: got %d matches", n)
	}
	if n := len(ga.Re.FindAllStringIndex("分からないが、試してみた。", -1)); n >= ga.Min {
		t.Errorf("double_ga false positive: %d", n)
	}
}

// utf16Runes は UTF-16 単位で添字が引けるよう、サロゲートペアを考慮せずに
// テスト用に BMP 前提で rune 列を返す
func utf16Runes(s string) []rune { return []rune(s) }
