package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot はこのパッケージから見たリポジトリのルート (tool/akaire → ../../)。
// viewpointsPath は -data (= リポジトリのチェックアウト) 相対なので、ここを基点に解決する。
const repoRoot = "../.."

// viewpointPrompts は viewpoints の章番号を参照しうるプロンプト定数の一覧。
// 定数を足したらここにも足す。
var viewpointPrompts = map[string]string{
	"viewpointsSuffixFmt": viewpointsSuffixFmt,
	"annotationFormat":    annotationFormat,
	"reviewPromptFmt":     reviewPromptFmt,
	"fullReviewPromptFmt": fullReviewPromptFmt,
	"structurePromptFmt":  structurePromptFmt,
	"consultPromptFmt":    consultPromptFmt,
}

// readViewpoints は viewpointsPath を読む。無ければテストを失敗させる
// (skill ディレクトリのリネーム等で実行時にしか壊れない事故をここで拾う)。
func readViewpoints(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot, viewpointsPath)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("viewpointsPath (%s) が読めない: %v", viewpointsPath, err)
	}
	return string(b)
}

// viewpointChapters は viewpoints.md の「## N. 見出し」から章番号の集合を作る
var chapterHeading = regexp.MustCompile(`(?m)^## (\d+)\. `)

func viewpointChapters(md string) map[string]bool {
	chapters := map[string]bool{}
	for _, m := range chapterHeading.FindAllStringSubmatch(md, -1) {
		chapters[m[1]] = true
	}
	return chapters
}

// chapterRef はプロンプト内の「N 章」形式の参照
var chapterRef = regexp.MustCompile(`(\d+) 章`)

// TestPromptChapterRefs は各プロンプト定数が参照する「N 章」が
// viewpoints.md に実在する章であることを検証する。
// viewpoints.md で章を追加・並べ替えるとプロンプト側の参照が黙ってズレるため。
func TestPromptChapterRefs(t *testing.T) {
	chapters := viewpointChapters(readViewpoints(t))
	if len(chapters) == 0 {
		t.Fatalf("%s に「## N. 見出し」形式の章が見つからない", viewpointsPath)
	}

	for name, prompt := range viewpointPrompts {
		refs := chapterRef.FindAllStringSubmatch(prompt, -1)
		for _, m := range refs {
			if !chapters[m[1]] {
				t.Errorf("%s が参照する「%s 章」は %s に存在しない", name, m[1], viewpointsPath)
			}
		}
	}
}

// TestPromptsMentionViewpoints は観点を viewpoints に委ねているプロンプトが
// 実際に viewpoints を参照していること (章番号の参照が全滅していないこと) を検証する。
// 観点の記述をプロンプトへ書き戻してしまう退行を防ぐ。
func TestPromptsMentionViewpoints(t *testing.T) {
	for _, name := range []string{"reviewPromptFmt", "fullReviewPromptFmt", "structurePromptFmt", "consultPromptFmt"} {
		prompt := viewpointPrompts[name]
		if !strings.Contains(prompt, "viewpoints") {
			t.Errorf("%s が viewpoints を参照していない", name)
		}
		if !chapterRef.MatchString(prompt) {
			t.Errorf("%s に「N 章」形式の参照が 1 つも無い", name)
		}
	}
}
