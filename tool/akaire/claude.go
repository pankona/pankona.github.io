// claude -p (赤入れ・通し・構成・相談・jev→赤入れ) の設定。
//
// 既定のプロンプト雛形は main.go の定数。エディタの「⚙ Claude」からモデル・
// エフォート・各モードの雛形を差し替えられ、-claude-config のファイルに保存する。
// 雛形は fmt.Sprintf の書式 (%[1]s など) で、モードごとに埋まる値が違う。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// claudeMode はモードごとの雛形と、そこに埋まる引数の説明。
type claudeMode struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Args   []string `json:"args"` // %[1]s, %[2]s, … の意味 (設定画面の凡例)
	prompt string   // 既定の雛形
}

var claudeModes = []claudeMode{
	{Key: "diff", Label: "赤入れ (差分)", Args: []string{"原稿のパス", "指摘ファイル (.akaire.json) のパス"}, prompt: reviewPromptFmt},
	{Key: "full", Label: "通し", Args: []string{"原稿のパス", "指摘ファイルのパス"}, prompt: fullReviewPromptFmt},
	{Key: "structure", Label: "構成", Args: []string{"原稿のパス", "指摘ファイルのパス"}, prompt: structurePromptFmt},
	{Key: "consult", Label: "相談", Args: []string{"原稿のパス", "筆者が選択した引用", "筆者のメモ", "指摘ファイルのパス"}, prompt: consultPromptFmt},
	{Key: "lint", Label: "jev→赤入れ", Args: []string{"原稿のパス", "jev の判定つき引用の一覧", "指摘ファイルのパス"}, prompt: lintPromptFmt},
}

func claudeModeByKey(key string) (claudeMode, bool) {
	for _, m := range claudeModes {
		if m.Key == key {
			return m, true
		}
	}
	return claudeMode{}, false
}

// claudeSettings は設定画面で編集する一式。Model / Effort が空なら claude の既定に任せる
// (フラグを渡さない)。Prompts はモードキー → 雛形。無いモードは既定の雛形を使う。
type claudeSettings struct {
	Model   string            `json:"model"`
	Effort  string            `json:"effort"`
	Prompts map[string]string `json:"prompts"`
}

var claudeEfforts = []string{"", "low", "medium", "high", "xhigh", "max"}

func defaultClaudeSettings() claudeSettings {
	s := claudeSettings{Prompts: map[string]string{}}
	for _, m := range claudeModes {
		s.Prompts[m.Key] = m.prompt
	}
	return s
}

// prompt はモードの雛形 (設定で差し替えたもの、無ければ既定) を返す。
func (s claudeSettings) prompt(mode string) (string, bool) {
	m, ok := claudeModeByKey(mode)
	if !ok {
		return "", false
	}
	if p, ok := s.Prompts[mode]; ok && p != "" {
		return p, true
	}
	return m.prompt, true
}

// validate は effort の値と、各雛形が fmt.Sprintf で壊れないことを確かめる。
// 雛形に生の % があると %!(BADPREC) 等が混じるので、ダミー引数で展開して検出する。
func (s claudeSettings) validate() error {
	ok := false
	for _, e := range claudeEfforts {
		if s.Effort == e {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("effort は %s のいずれか (got %q)", strings.Join(claudeEfforts[1:], " / "), s.Effort)
	}
	if strings.ContainsAny(s.Model, " \t\n") {
		return errors.New("model に空白が含まれている")
	}
	for key, p := range s.Prompts {
		m, ok := claudeModeByKey(key)
		if !ok {
			return fmt.Errorf("未知のモード %q", key)
		}
		if p == "" {
			continue
		}
		args := make([]any, len(m.Args))
		for i := range args {
			args[i] = fmt.Sprintf("<arg%d>", i+1)
		}
		if out := fmt.Sprintf(p, args...); strings.Contains(out, "%!") {
			return fmt.Errorf("%s の雛形が書式として不正 (%%[1]s〜%%[%d]s 以外の %% は %%%% と書く)", m.Label, len(m.Args))
		}
		if !strings.Contains(p, "%[1]s") {
			return fmt.Errorf("%s の雛形に原稿のパス %%[1]s が無い", m.Label)
		}
	}
	return nil
}

// cliArgs は claude -p に足すフラグを返す。
func (s claudeSettings) cliArgs() []string {
	var a []string
	if s.Model != "" {
		a = append(a, "--model", s.Model)
	}
	if s.Effort != "" {
		a = append(a, "--effort", s.Effort)
	}
	return a
}

// claudeConfig は設定の置き場。読み書きは mu で守る。
type claudeConfig struct {
	mu       sync.RWMutex
	settings claudeSettings
	path     string // 保存先。空なら保存しない
}

func newClaudeConfig(path string) *claudeConfig {
	c := &claudeConfig{settings: defaultClaudeSettings(), path: path}
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		fmt.Fprintf(os.Stderr, "claude 設定 %s を読めないので既定を使う: %v\n", path, err)
		return c
	}
	if s.Prompts == nil {
		s.Prompts = map[string]string{}
	}
	if err := s.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "claude 設定 %s が不正なので既定を使う: %v\n", path, err)
		return c
	}
	// 保存ファイルに無いモードは既定で埋める (後からモードが増えても壊れない)
	for _, m := range claudeModes {
		if _, ok := s.Prompts[m.Key]; !ok {
			s.Prompts[m.Key] = m.prompt
		}
	}
	c.settings = s
	return c
}

func (c *claudeConfig) get() claudeSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := c.settings
	out.Prompts = map[string]string{}
	for k, v := range c.settings.Prompts {
		out.Prompts[k] = v
	}
	return out
}

func (c *claudeConfig) set(s claudeSettings) error {
	if s.Prompts == nil {
		s.Prompts = map[string]string{}
	}
	if err := s.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = s
	c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.path, append(b, '\n'), 0o644)
}

func (c *claudeConfig) reset() error {
	c.mu.Lock()
	c.settings = defaultClaudeSettings()
	c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// defaultClaudeConfigPath は claude 設定の既定の保存先 (~/.config/akaire/claude.json)。
func defaultClaudeConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "akaire", "claude.json")
}
