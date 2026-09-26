package main

import (
	"context"
	"os"
	"path/filepath"
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

func TestLintSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jev.json")
	l := newLinter(path)
	s := l.getSettings()
	if len(s.Checks) == 0 || len(s.RegexChecks) == 0 {
		t.Fatalf("既定の判定が空: %+v", s)
	}
	s.Checks[0].Threshold = 0.9
	s.Checks[0].Disabled = true
	s.RegexChecks[0].Min = 3
	if err := l.setSettings(s); err != nil {
		t.Fatal(err)
	}
	// 無効化した判定は jjl に渡す Config から外れる
	for _, c := range l.l.Config.Checks {
		if c.Key == s.Checks[0].Key {
			t.Errorf("無効化した %s が Config に残っている", c.Key)
		}
	}
	// 再起動 (読み直し) しても残る
	l2 := newLinter(path)
	got := l2.getSettings()
	if got.Checks[0].Threshold != 0.9 || !got.Checks[0].Disabled || got.RegexChecks[0].Min != 3 {
		t.Errorf("保存した設定が読み戻せない: %+v", got)
	}
	// 既定に戻すとファイルも消える
	if err := l2.resetSettings(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reset 後もファイルが残っている: %v", err)
	}
	if l2.getSettings().Checks[0].Threshold == 0.9 {
		t.Error("reset 後も閾値が既定に戻っていない")
	}
}

func TestLintSettingsValidate(t *testing.T) {
	base := defaultLintSettings()
	bad := func(name string, f func(s *lintSettings)) {
		s := defaultLintSettings()
		f(&s)
		if err := s.validate(); err == nil {
			t.Errorf("%s: エラーになるべき", name)
		}
	}
	if err := base.validate(); err != nil {
		t.Fatalf("既定が不正: %v", err)
	}
	bad("threshold > 1", func(s *lintSettings) { s.Checks[0].Threshold = 1.5 })
	bad("empty instructions", func(s *lintSettings) { s.Checks[0].Question.Instructions = "" })
	bad("bad regexp", func(s *lintSettings) { s.RegexChecks[0].Pattern = "(" })
	bad("regex min 0", func(s *lintSettings) { s.RegexChecks[0].Min = 0 })
	bad("dup key", func(s *lintSettings) { s.RegexChecks[0].Key = s.Checks[0].Key })
	bad("empty model", func(s *lintSettings) { s.Model = "" })
}
