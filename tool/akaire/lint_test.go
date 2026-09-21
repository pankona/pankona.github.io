package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf16"

	jjl "github.com/pankona/japanese-jev-lint"
)

// 文の分割と判定は jjl 側でテストしている。ここでは UTF-16 への位置変換だけ見る。
func TestToHitsUTF16(t *testing.T) {
	doc := strings.Join([]string{
		"---", "title: テスト", "---", "",
		"最初の文である。二つ目の文だ！",
		"- 箇条書きの文である。𠮷野家の話である。", // サロゲートペアを含む
		"これはですます調の文です。",
	}, "\n")
	src := []byte(doc)
	// jev には聞かず正規表現判定だけ (Client なし)
	r := (&jjl.Linter{Config: jjl.DefaultConfig()}).Lint(context.Background(), "", src)
	hits := toHits(src, r.Items)
	want := []string{"最初の文である。", "二つ目の文だ！", "箇条書きの文である。", "𠮷野家の話である。", "これはですます調の文です。"}
	if len(hits) != len(want) {
		t.Fatalf("hits = %d, want %d", len(hits), len(want))
	}
	u16 := utf16.Encode([]rune(doc))
	for i, h := range hits {
		if got := string(utf16.Decode(u16[h.From:h.To])); got != want[i] {
			t.Errorf("[%d] utf16 range points to %q, want %q", i, got, want[i])
		}
	}
	if len(hits[4].Flags) != 1 || hits[4].Flags[0] != "desumasu" {
		t.Errorf("flags of [4] = %v", hits[4].Flags)
	}
}
