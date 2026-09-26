package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	c := newClaudeConfig(path)
	s := c.get()
	if len(s.Prompts) != len(claudeModes) {
		t.Fatalf("既定の雛形が %d 件 (want %d)", len(s.Prompts), len(claudeModes))
	}
	s.Model = "opus"
	s.Effort = "high"
	s.Prompts["diff"] = "対象は %[1]s、指摘は %[2]s へ。100%% で頼む"
	if err := c.set(s); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.get().cliArgs(), " "); got != "--model opus --effort high" {
		t.Errorf("cliArgs = %q", got)
	}
	c2 := newClaudeConfig(path)
	got := c2.get()
	p, _ := got.prompt("diff")
	if got.Model != "opus" || got.Effort != "high" || !strings.Contains(p, "100%%") {
		t.Errorf("保存した設定が読み戻せない: %+v", got)
	}
	// 差し替えていないモードは既定のまま
	if p, _ := got.prompt("full"); p != fullReviewPromptFmt {
		t.Error("full の雛形が既定でない")
	}
	if err := c2.reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reset 後もファイルが残っている: %v", err)
	}
	if c2.get().Model != "" || len(c2.get().cliArgs()) != 0 {
		t.Error("reset 後もモデルが残っている")
	}
}

func TestClaudeSettingsValidate(t *testing.T) {
	if err := defaultClaudeSettings().validate(); err != nil {
		t.Fatalf("既定が不正: %v", err)
	}
	bad := func(name string, f func(s *claudeSettings)) {
		s := defaultClaudeSettings()
		f(&s)
		if err := s.validate(); err == nil {
			t.Errorf("%s: エラーになるべき", name)
		}
	}
	bad("unknown effort", func(s *claudeSettings) { s.Effort = "ultra" })
	bad("model with space", func(s *claudeSettings) { s.Model = "opus 4" })
	bad("unknown mode", func(s *claudeSettings) { s.Prompts["foo"] = "%[1]s" })
	bad("raw percent", func(s *claudeSettings) { s.Prompts["diff"] = "%[1]s を 100% 読む" })
	bad("too many args", func(s *claudeSettings) { s.Prompts["diff"] = "%[1]s %[2]s %[3]s" })
	bad("missing doc path", func(s *claudeSettings) { s.Prompts["diff"] = "指摘は %[2]s へ" })
}
