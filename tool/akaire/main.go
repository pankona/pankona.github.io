// akaire は、AI や人間による赤入れ (レビューコメント) を原稿に重ねて表示しながら
// 編集できるローカル専用エディタのサーバー。
//
// 使い方:
//
//	go run ./akaire -data ./akaire/demo-data -addr :8433
//
// data ディレクトリには編集対象の .md ファイル群を置く。指摘は原稿ごとに
// 同じディレクトリの .<原稿名>.akaire.json に保存される。
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankona/pankona.github.io/tool/imagemeta"
)

//go:embed index.html
var indexHTML embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8433", "listen address")
	dataDir := flag.String("data", "demo-data", "directory containing .md files")
	flag.Parse()

	mux := http.NewServeMux()

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = *dataDir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	gh := func(args ...string) (string, error) {
		cmd := exec.Command("gh", args...)
		cmd.Dir = *dataDir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// 未コミットの変更を WIP コミットに退避する (ブランチ切替で失わないため)。
	// 記事作業のコミットはどのみち赤入れの節目で積まれるので、WIP が混ざっても
	// 履歴の運用は変わらない
	autocommit := func() error {
		st, err := git("status", "--porcelain")
		if err != nil {
			return fmt.Errorf("git status: %v: %s", err, st)
		}
		if strings.TrimSpace(st) == "" {
			return nil
		}
		if out, err := git("add", "-A"); err != nil {
			return fmt.Errorf("git add: %v: %s", err, out)
		}
		if out, err := git("commit", "-m", "wip: ブランチ切替前の自動保存"); err != nil {
			return fmt.Errorf("git commit: %v: %s", err, out)
		}
		return nil
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := indexHTML.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})

	mux.HandleFunc("GET /api/files", func(w http.ResponseWriter, r *http.Request) {
		type entry struct {
			rel   string
			mtime int64
		}
		all := []entry{}
		err := filepath.WalkDir(*dataDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				// 隠しディレクトリや依存物置き場には原稿はない
				if path != *dataDir && (strings.HasPrefix(name, ".") || name == "node_modules") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".md") {
				rel, err := filepath.Rel(*dataDir, path)
				if err != nil {
					return err
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				all = append(all, entry{filepath.ToSlash(rel), docTime(path, info.ModTime()).UnixNano()})
			}
			return nil
		})
		if err != nil {
			httpError(w, err)
			return
		}
		// 原稿があるのは content/ と drafts/ だけ。ただしどちらも無い
		// (demo-data のようなフラットな置き場) 場合は全部を対象にする
		drafts := []entry{}
		for _, e := range all {
			if strings.HasPrefix(e.rel, "content/") || strings.HasPrefix(e.rel, "drafts/") {
				drafts = append(drafts, e)
			}
		}
		if len(drafts) == 0 {
			drafts = all
		}
		// 新しい順 (いま書いているもの・新しい記事が先頭に来るように)
		sort.Slice(drafts, func(i, j int) bool { return drafts[i].mtime > drafts[j].mtime })
		files := make([]string, len(drafts))
		for i, e := range drafts {
			files[i] = e.rel
		}
		// 他のブランチにだけ存在する原稿も選べるように、main との差分から
		// 各ブランチの原稿を拾って添える。main/master と、他の worktree が
		// 掴んでいるブランチ (switch できない) は除く
		branch, _ := git("rev-parse", "--abbrev-ref", "HEAD")
		type other struct {
			Branch string `json:"branch"`
			File   string `json:"file"`
		}
		others := []other{}
		// 比較の基準はローカル main ではなく origin/main (ローカル main は
		// この運用ではまず進まないので当てにしない)
		mainRef := "origin/main"
		if _, err := git("rev-parse", "--verify", mainRef); err != nil {
			mainRef = "main"
		}
		if bl, err := git("for-each-ref", "refs/heads", "--format", "%(refname:short)\t%(worktreepath)"); err == nil && bl != "" {
			for _, line := range strings.Split(bl, "\n") {
				b, wt, _ := strings.Cut(strings.TrimSpace(line), "\t")
				if b == "" || b == branch || b == "main" || b == "master" || wt != "" {
					continue
				}
				// そのブランチで新規追加された原稿だけを「そのブランチの記事」と
				// みなす (既存記事へ触っただけの古いブランチをノイズにしない)
				out, err := git("diff", "--name-only", "--diff-filter=A", "--no-renames", mainRef+"..."+b)
				if err != nil {
					continue
				}
				for _, f := range strings.Split(out, "\n") {
					f = strings.TrimSpace(f)
					if strings.HasSuffix(f, ".md") &&
						(strings.HasPrefix(f, "content/") || strings.HasPrefix(f, "drafts/")) {
						others = append(others, other{b, f})
					}
				}
			}
		}
		writeJSON(w, map[string]any{"files": files, "branch": branch, "others": others})
	})

	// 既存記事の frontmatter からカテゴリを集計する (カテゴリ編集 UI の候補用)
	mux.HandleFunc("GET /api/categories", func(w http.ResponseWriter, r *http.Request) {
		counts := map[string]int{}
		err := filepath.WalkDir(*dataDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != *dataDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".md") {
				for _, c := range readCategories(path) {
					counts[c]++
				}
			}
			return nil
		})
		if err != nil {
			httpError(w, err)
			return
		}
		type cat struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		list := []cat{}
		for n, c := range counts {
			list = append(list, cat{n, c})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Count != list[j].Count {
				return list[i].Count > list[j].Count
			}
			return list[i].Name < list[j].Name
		})
		writeJSON(w, map[string]any{"categories": list})
	})

	mux.HandleFunc("GET /api/doc", func(w http.ResponseWriter, r *http.Request) {
		name, ok := safeName(w, r)
		if !ok {
			return
		}
		b, err := os.ReadFile(filepath.Join(*dataDir, name))
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"file": name, "content": string(b)})
	})

	mux.HandleFunc("PUT /api/doc", func(w http.ResponseWriter, r *http.Request) {
		name, ok := safeName(w, r)
		if !ok {
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			httpError(w, err)
			return
		}
		// エディタ由来の「最終行に改行なし」を補う (dprint 等の formatter が
		// 末尾改行を要求するため)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
		dest := filepath.Join(*dataDir, name)
		// 新規記事 (ページバンドル含む) 用にディレクトリごと作れるようにする
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			httpError(w, err)
			return
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// 画像ペースト/ドロップの保存先。articlegen が issue 添付画像を取り込む
	// ときと同じ「原稿と同じディレクトリに image-N.<ext>」の形に揃える。
	mux.HandleFunc("POST /api/asset", func(w http.ResponseWriter, r *http.Request) {
		name, ok := safeName(w, r)
		if !ok {
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			httpError(w, err)
			return
		}
		ext := imagemeta.ExtFromMagic(b)
		if ext == "" {
			ext = imagemeta.ExtFromContentType(r.Header.Get("Content-Type"))
		}
		if ext == "" {
			http.Error(w, "unsupported image type", http.StatusBadRequest)
			return
		}
		dir := filepath.Join(*dataDir, filepath.Dir(name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			httpError(w, err)
			return
		}
		// 既存の image-N と衝突しない連番を割り当てる
		asset := ""
		for n := 1; ; n++ {
			matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("image-%d.*", n)))
			if err == nil && len(matches) == 0 {
				asset = fmt.Sprintf("image-%d%s", n, ext)
				break
			}
		}
		if err := os.WriteFile(filepath.Join(dir, asset), b, 0o644); err != nil {
			httpError(w, err)
			return
		}
		log.Printf("asset saved: %s (%d bytes)", filepath.Join(filepath.Dir(name), asset), len(b))
		writeJSON(w, map[string]any{"name": asset})
	})

	// エディタから相対参照される画像 (image-1.png など) のプレビュー用
	mux.HandleFunc("GET /api/asset", func(w http.ResponseWriter, r *http.Request) {
		q := filepath.Clean(filepath.FromSlash(r.URL.Query().Get("file")))
		if !filepath.IsLocal(q) {
			http.Error(w, "bad file name", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, filepath.Join(*dataDir, q))
	})

	// 指摘は原稿ごとのファイル (原稿と同じディレクトリの .<原稿名>.akaire.json) に
	// 持つ。記事ブランチと一緒に育ち、並行する記事どうしで衝突しない。
	// 先頭がドットなので Hugo にも拾われない。
	mux.HandleFunc("GET /api/annotations", func(w http.ResponseWriter, r *http.Request) {
		name, ok := safeName(w, r)
		if !ok {
			return
		}
		b, err := os.ReadFile(filepath.Join(*dataDir, annPath(name)))
		if os.IsNotExist(err) {
			// 指摘がまだ無いだけなので空で返す
			b, err = []byte(`{"annotations": []}`), nil
		}
		if err != nil {
			httpError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
	})

	mux.HandleFunc("PUT /api/annotations", func(w http.ResponseWriter, r *http.Request) {
		name, ok := safeName(w, r)
		if !ok {
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			httpError(w, err)
			return
		}
		if !json.Valid(b) {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(*dataDir, annPath(name)), b, 0o644); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// ---- 赤入れ依頼: 裏で claude -p を走らせる --------------------------------

	var review struct {
		sync.Mutex
		running    bool
		mode       string // "diff" (差分) か "full" (通し)
		output     string
		errText    string
		finishedAt time.Time
	}

	mux.HandleFunc("POST /api/review", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode  string `json:"mode"`
			File  string `json:"file"`
			Quote string `json:"quote"` // consult: しっくりきていない箇所の引用
			Note  string `json:"note"`  // consult: 筆者のメモ (任意)
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mode := req.Mode
		if mode == "" {
			mode = "diff"
		}
		name := filepath.Clean(filepath.FromSlash(req.File))
		if !strings.HasSuffix(name, ".md") || !filepath.IsLocal(name) {
			http.Error(w, "bad file name", http.StatusBadRequest)
			return
		}
		doc := filepath.ToSlash(name)
		ann := filepath.ToSlash(annPath(name))
		var prompt string
		switch mode {
		case "diff":
			prompt = fmt.Sprintf(reviewPromptFmt, doc, ann)
		case "full":
			prompt = fmt.Sprintf(fullReviewPromptFmt, doc, ann)
		case "structure":
			prompt = fmt.Sprintf(structurePromptFmt, doc, ann)
		case "consult":
			if strings.TrimSpace(req.Quote) == "" {
				http.Error(w, "quote required", http.StatusBadRequest)
				return
			}
			note := strings.TrimSpace(req.Note)
			if note == "" {
				note = "(特になし。何が引っかかっているかの言語化も含めて相談したい)"
			}
			prompt = fmt.Sprintf(consultPromptFmt, doc, req.Quote, note, ann)
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		review.Lock()
		defer review.Unlock()
		if review.running {
			http.Error(w, "review already running", http.StatusConflict)
			return
		}
		absData, err := filepath.Abs(*dataDir)
		if err != nil {
			httpError(w, err)
			return
		}
		review.running = true
		review.mode = mode
		review.output, review.errText = "", ""
		go func() {
			cmd := exec.Command("claude", "-p", prompt,
				"--allowedTools", "Read,Grep,Glob,Write,Edit,"+
					"Bash(git diff:*),Bash(git log:*),Bash(git status:*),Bash(git show:*),"+
					"Bash(git add:*),Bash(git commit:*),Bash(npx textlint:*)")
			cmd.Dir = absData
			out, err := cmd.CombinedOutput()
			review.Lock()
			defer review.Unlock()
			review.running = false
			review.output = string(out)
			review.finishedAt = time.Now()
			if err != nil {
				review.errText = err.Error()
			}
			log.Printf("review finished (err=%v, %d bytes output)", err, len(out))
		}()
		log.Printf("review started (mode: %s, dir: %s)", mode, absData)
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/review", func(w http.ResponseWriter, r *http.Request) {
		review.Lock()
		defer review.Unlock()
		writeJSON(w, map[string]any{
			"running": review.running,
			"mode":    review.mode,
			"error":   review.errText,
			"output":  review.output,
		})
	})

	// ---- ブランチ自動化: 1 ブランチ = 1 記事 ----------------------------------

	// 赤入れ (claude) が作業ツリーを触っている間のブランチ切替は事故のもと
	reviewRunning := func() bool {
		review.Lock()
		defer review.Unlock()
		return review.running
	}

	// 新しい記事: 未コミット変更を退避 → origin/main から article/<slug> を
	// 生やして切替 → テンプレートを書く。ブランチが既にあれば切替だけする
	mux.HandleFunc("POST /api/new", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			File    string `json:"file"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := filepath.Clean(filepath.FromSlash(req.File))
		if !strings.HasSuffix(name, ".md") || !filepath.IsLocal(name) {
			http.Error(w, "bad file name", http.StatusBadRequest)
			return
		}
		if reviewRunning() {
			http.Error(w, "赤入れの実行中はブランチを切り替えられません。完了を待ってください", http.StatusConflict)
			return
		}
		slug := strings.TrimSuffix(filepath.Base(name), ".md")
		if slug == "index" {
			slug = filepath.Base(filepath.Dir(name))
		}
		branch := "article/" + slug
		if err := autocommit(); err != nil {
			httpError(w, err)
			return
		}
		// 最新の main から生やす。fetch できなければ手元の main で妥協する
		base := "origin/main"
		if out, err := git("fetch", "origin", "main"); err != nil {
			log.Printf("git fetch failed (offline?): %v: %s", err, out)
			base = "main"
		}
		if _, err := git("switch", branch); err != nil {
			if out, err := git("switch", "-c", branch, base); err != nil {
				httpError(w, fmt.Errorf("git switch -c %s: %v: %s", branch, err, out))
				return
			}
		}
		dest := filepath.Join(*dataDir, name)
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				httpError(w, err)
				return
			}
			if err := os.WriteFile(dest, []byte(req.Content), 0o644); err != nil {
				httpError(w, err)
				return
			}
		}
		log.Printf("new article: %s (branch: %s)", name, branch)
		writeJSON(w, map[string]any{"branch": branch, "file": filepath.ToSlash(name)})
	})

	// 別ブランチにある記事を開くための切替。未コミット変更は WIP コミットに退避
	mux.HandleFunc("POST /api/switch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Branch string `json:"branch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Branch == "" || req.Branch == "main" || req.Branch == "master" {
			http.Error(w, "bad branch", http.StatusBadRequest)
			return
		}
		if reviewRunning() {
			http.Error(w, "赤入れの実行中はブランチを切り替えられません。完了を待ってください", http.StatusConflict)
			return
		}
		if err := autocommit(); err != nil {
			httpError(w, err)
			return
		}
		if out, err := git("switch", req.Branch); err != nil {
			httpError(w, fmt.Errorf("git switch %s: %v: %s", req.Branch, err, out))
			return
		}
		log.Printf("switched to %s", req.Branch)
		writeJSON(w, map[string]any{"branch": req.Branch})
	})

	// ---- push: 積んだ赤入れコミットを remote へ -------------------------------

	// ブランチ → PR URL。ヘッダー表示用に 60 秒ポーリングされるので、
	// 毎回 gh を叩かないようキャッシュする
	type prEntry struct {
		url string
		at  time.Time
	}
	var prMu sync.Mutex
	prURLs := map[string]prEntry{}
	prSet := func(branch, url string) {
		prMu.Lock()
		prURLs[branch] = prEntry{url, time.Now()}
		prMu.Unlock()
	}
	prLookup := func(branch string) string {
		prMu.Lock()
		e, ok := prURLs[branch]
		prMu.Unlock()
		if ok && time.Since(e.at) < 10*time.Minute {
			return e.url
		}
		u, err := gh("pr", "view", branch, "--json", "url", "-q", ".url")
		if err != nil {
			u = ""
		}
		prSet(branch, u)
		return u
	}

	mux.HandleFunc("GET /api/push", func(w http.ResponseWriter, r *http.Request) {
		branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			httpError(w, fmt.Errorf("%s: %s", err, branch))
			return
		}
		ahead := -1
		if out, err := git("rev-list", "--count", "@{upstream}..HEAD"); err == nil {
			fmt.Sscanf(out, "%d", &ahead)
		}
		// 保存しただけでコミットされていない変更 (push 時に自動コミットされる)
		dirty := false
		if st, err := git("status", "--porcelain"); err == nil {
			dirty = strings.TrimSpace(st) != ""
		}
		writeJSON(w, map[string]any{"branch": branch, "ahead": ahead, "dirty": dirty, "pr": prLookup(branch)})
	})

	mux.HandleFunc("POST /api/push", func(w http.ResponseWriter, r *http.Request) {
		// main へ直接 push する事故を防ぐ。原稿作業は必ずトピックブランチで行う
		branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			httpError(w, fmt.Errorf("%s: %s", err, branch))
			return
		}
		if branch == "main" || branch == "master" {
			http.Error(w, fmt.Sprintf("%s へは push しません。トピックブランチで作業してください", branch), http.StatusForbidden)
			return
		}
		// 保存しただけでコミットされていない変更があれば、コミットしてから
		// push する (赤入れを挟まない微修正もこれで載る)
		if st, err := git("status", "--porcelain"); err == nil && strings.TrimSpace(st) != "" {
			var changed []string
			for _, l := range strings.Split(st, "\n") {
				if fs := strings.Fields(strings.TrimSpace(l)); len(fs) > 0 {
					changed = append(changed, fs[len(fs)-1])
				}
			}
			msg := "更新: " + strings.Join(changed, ", ")
			if len(changed) > 3 {
				msg = fmt.Sprintf("更新: %s ほか %d ファイル", changed[0], len(changed)-1)
			}
			if out, err := git("add", "-A"); err != nil {
				httpError(w, fmt.Errorf("git add: %v: %s", err, out))
				return
			}
			if out, err := git("commit", "-m", msg); err != nil {
				httpError(w, fmt.Errorf("git commit: %v: %s", err, out))
				return
			}
			log.Printf("committed before push: %s", msg)
		}
		// -u で upstream を設定しないと、akaire が作ったブランチの初回 push 後に
		// @{upstream} が解決できず ahead が数えられなくなる
		out, err := git("push", "-u", "origin", "HEAD")
		if err != nil {
			http.Error(w, fmt.Sprintf("%v\n%s", err, out), http.StatusInternalServerError)
			return
		}
		log.Printf("pushed: %s", out)
		// PR がまだ無ければ draft で作っておく (PR に載っている間が下書き期間)
		prURL, prCreated := "", false
		if u, err := gh("pr", "view", "--json", "url", "-q", ".url"); err == nil {
			prURL = u
		} else if u, err := gh("pr", "create", "--draft", "--fill"); err == nil {
			prURL, prCreated = u, true
			log.Printf("draft PR created: %s", u)
		} else {
			log.Printf("gh pr create failed: %v: %s", err, u)
		}
		if prURL != "" {
			prSet(branch, prURL)
		}
		writeJSON(w, map[string]any{"ok": true, "output": out, "pr": prURL, "prCreated": prCreated})
	})

	// ---- GitHub label 同期 ----------------------------------------------------
	// articlegen は issue の label を記事カテゴリに写すので、akaire で生まれた
	// 新カテゴリは label 側にも作って往復を保つ。リポジトリは data ディレクトリの
	// git remote から gh が解決する

	mux.HandleFunc("POST /api/labels", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := gh("label", "list", "--limit", "200", "--json", "name")
		if err != nil {
			httpError(w, fmt.Errorf("gh label list: %v: %s", err, out))
			return
		}
		var existing []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(out), &existing); err != nil {
			httpError(w, err)
			return
		}
		// GitHub の label 名は大文字小文字を区別せず衝突するので、比較も同様に
		have := map[string]bool{}
		for _, l := range existing {
			have[strings.ToLower(l.Name)] = true
		}
		created := []string{}
		for _, n := range req.Names {
			n = strings.TrimSpace(n)
			if n == "" || have[strings.ToLower(n)] {
				continue
			}
			if out, err := gh("label", "create", n, "--color", labelColor(n), "--description", "blog category"); err != nil {
				httpError(w, fmt.Errorf("gh label create %q: %v: %s", n, err, out))
				return
			}
			have[strings.ToLower(n)] = true
			created = append(created, n)
			log.Printf("github label created: %s", n)
		}
		writeJSON(w, map[string]any{"created": created})
	})

	log.Printf("akaire: http://%s/ (data: %s)", *addr, *dataDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// reviewPromptFmt は赤入れ依頼ボタンで起動する claude -p への指示
// (%[1]s は対象原稿のパス, %[2]s はその指摘ファイルのパス)。
// 文体・観点の詳細はリポジトリの CLAUDE.md にも書かれている前提だが、
// 無くても成り立つよう要点をここに埋め込んでいる。
const reviewPromptFmt = `あなたはブログ原稿の赤入れ (校閲) 係です。このリポジトリで次を行ってください。

1. 対象の原稿は %[1]s。前回の赤入れ以降の変更箇所を特定する。
   基準コミットは「指摘ファイル %[2]s を最後に変更したコミット」
   (git log -n 1 --format=%%H -- %[2]s)。
   指摘ファイルにまだコミットが無ければ、原稿の全文を対象にする。
   基準コミットから作業ツリーまでの diff (未コミット変更を含む) で
   %[1]s の変更箇所を洗い出し、変更がなければ何もせず「変更なし」と
   報告して終了する。

2. リポジトリに .textlintrc* があれば、対象の原稿に textlint をかける:
   npx textlint --format json %[1]s
   出力をそのまま貼らず、吟味して本物の問題だけを手順 4 の形式の指摘に翻訳する:
   - quote は指摘された行・列に対応する原稿の文字列をそのまま切り出す
   - body はルールのメッセージの引き写しではなく、その文に即した具体的な
     修正案として書き起こし、末尾に (textlint: ruleId) と出どころを明記する
   - kind は "red"
   - 意図的な文体・レトリック (口語の差し込みへの ですます warning 等) と
     判断したものは指摘にしない
   .textlintrc* が無い、または textlint が実行できない場合はこの手順を
   スキップして構わない (エラーにしない)。

3. 変更箇所を読み、指摘を %[2]s の annotations 配列に追記する
   (ファイルが無ければ {"annotations": []} から作る)。
   観点:
   - 文の成立 (最優先・kind "red"): 主述の対応 (主語のすり替わり・述語の着地)、
     係り受けが述語まで届いているか、助詞の競合 (「は」の二連続など)、
     修飾関係が意味として成り立っているか
   - 文体: このブログは だ・である調 + 口語崩し が基調。ですます調へ直す提案や、
     「なんだけど」「でもって」「あと」「で、」等の口語接続詞の除去提案はしない
   - 構成・読みやすさ・重複などは kind "pencil"
   - 変更箇所と前後の段落のつながりも確認する

4. 指摘の形式:
   {"id": 一意な文字列, "file": リポジトリ相対パス, "quote": 原稿に完全一致する引用,
    "body": 指摘本文 (修正案を添える), "author": "claude", "kind": "red" か "pencil"}
   quote は原稿の文字列をそのまま抜くこと (アンカーに使われるため)。
   既存の指摘 (dismissed = 見送り, resolved = 対応済み を含む) は変更しない。
   同じ趣旨の指摘を重複して出さない (resolved/dismissed になったものの蒸し返しも不可。
   ただし対応後の文に新たな問題があれば、それは新しい指摘として出してよい)。

5. 原稿本文 (.md) は一切書き換えない。直すのは筆者の仕事。

6. 追記した %[2]s と原稿 %[1]s を git add し、
   「赤入れ: %[1]s」の形でコミットする (これが次回の基準になる)。
   原稿が参照している画像などのアセットが未追跡で残っていないか git status で
   確認し (原稿と同じディレクトリの image-*.png 等)、あれば一緒に add する。`

// fullReviewPromptFmt は「通し」赤入れの指示
// (%[1]s は対象原稿のパス, %[2]s はその指摘ファイルのパス)。
// 差分ではなく全文を頭から読み、章ごとの差分赤入れでは原理的に見つからない
// 指摘 (章をまたいだ表記ゆれ・重複・構成) を拾うための最終パス。
const fullReviewPromptFmt = `あなたはブログ原稿の赤入れ (校閲) 係です。今回は差分ではなく「通し」の赤入れです。このリポジトリで次を行ってください。

1. 対象の原稿は %[1]s。これを頭から最後まで通して読む。
   diff は見なくてよい (章ごとの差分赤入れは済んでいる前提の、仕上げの通読)。

2. リポジトリに .textlintrc* があれば、対象の原稿に textlint をかける:
   npx textlint --format json %[1]s
   出力をそのまま貼らず、吟味して本物の問題だけを手順 4 の形式の指摘に翻訳する:
   - quote は指摘された行・列に対応する原稿の文字列をそのまま切り出す
   - body はルールのメッセージの引き写しではなく、その文に即した具体的な
     修正案として書き起こし、末尾に (textlint: ruleId) と出どころを明記する
   - kind は "red"
   - 意図的な文体・レトリック (口語の差し込みへの ですます warning 等) と
     判断したものは指摘にしない
   .textlintrc* が無い、または textlint が実行できない場合はこの手順を
   スキップして構わない (エラーにしない)。

3. 全文を通して読み、指摘を %[2]s の annotations 配列に追記する
   (ファイルが無ければ {"annotations": []} から作る)。
   通しでしか見つからない観点を重視する:
   - 文の成立 (最優先・kind "red"): 主述の対応 (主語のすり替わり・述語の着地)、
     係り受けが述語まで届いているか、助詞の競合、修飾関係の成立
   - 章をまたいだ表記ゆれ (漢字/ひらがな、数字、三点リーダー等の記号の不揃い)
   - 離れた場所での同語反復・近い言い回しの繰り返し
   - 見出しと本文の整合、タイトルの問いに結びが答えているか、章の順序とつながり
   - 文体: このブログは だ・である調 + 口語崩し が基調。ですます調へ直す提案や、
     「なんだけど」「でもって」「あと」「で、」等の口語接続詞の除去提案はしない
   構成・読みやすさ・重複などは kind "pencil"。

4. 指摘の形式:
   {"id": 一意な文字列, "file": リポジトリ相対パス, "quote": 原稿に完全一致する引用,
    "body": 指摘本文 (修正案を添える), "author": "claude", "kind": "red" か "pencil"}
   quote は原稿の文字列をそのまま抜くこと (アンカーに使われるため)。
   既存の指摘 (dismissed = 見送り, resolved = 対応済み を含む) は変更しない。
   同じ趣旨の指摘を重複して出さない (resolved/dismissed になったものの蒸し返しも不可。
   ただし対応後の文に新たな問題があれば、それは新しい指摘として出してよい)。

5. 原稿本文 (.md) は一切書き換えない。直すのは筆者の仕事。

6. 追記した %[2]s と対象の原稿を git add し、
   「赤入れ: %[1]s (通し)」の形でコミットする (これが次回の差分赤入れの基準になる)。
   原稿が参照している画像などのアセットが未追跡で残っていないか git status で
   確認し (原稿と同じディレクトリの image-*.png 等)、あれば一緒に add する。`

// structurePromptFmt は「構成レビュー」の指示
// (%[1]s は対象原稿のパス, %[2]s はその指摘ファイルのパス)。
// 一文一文の校閲ではなく、記事全体のストーリーの通りを見る。
// コミットはしない (指摘ファイルへの追記だけ。基準コミットを進めると
// 書きかけの本文が次回の差分赤入れから漏れるため、次の赤入れコミットに相乗りさせる)。
const structurePromptFmt = `あなたはブログの構成エディタです。今回は一文一文の校閲ではなく「構成レビュー」です。このリポジトリで次を行ってください。

1. 対象の原稿 %[1]s を頭から最後まで通して読む。

2. 構成の観点だけでレビューする:
   - タイトルは記事の結論・見どころと噛み合っているか (ズレがあれば方向性の
     違う代案を 2〜3 個。ただし決めるのは筆者)
   - 章立て・章の順序は妥当か。1 つの章に別の話題や時制が混ざっていないか
   - 上から一度読んだだけで話が通るか。記事を「何の話」として読ませるかの
     フレームは立っているか
   - 足すべき要素 (主張を支える実物・具体例・スクリーンショットの不足)、
     引くべき要素 (冗長な繰り返し、無くても通る段落)
   - 結びはタイトルの問いに答えているか

   ただし原稿がまだ章立てされていない場合 (音声入力の書き起こしなど、見出しが
   ほぼ無く、話題が行ったり来たりする一枚岩のテキスト) は、上記の代わりに
   「構成の立ち上げ」を行う:
   - 原稿に含まれる話題を洗い出し、話題の切れ目と行き来を特定する
   - 章立て案を 1 つ提示する (見出し案の列挙 + 各章に入る内容の一言説明)。
     記事全体への指摘 1 件にまとめる
   - 各段落 (冒頭の一文で指す) をどの章に収めるかの移動指示を出す。
     章ごとに 1 件にまとめてよい
   - 同じ話題が離れた場所で繰り返されている箇所は、どちらに寄せるかを指摘する
   - 提示するのは章立て案と移動指示まで。実際の並べ替え・見出しの記入・
     つなぎの文章は筆者が行う

3. 指摘を %[2]s の annotations 配列に追記する
   (ファイルが無ければ {"annotations": []} から作る)。アンカーの取り方:
   - 記事全体に関わる指摘 → 先頭の見出し (# …) の行を quote にする
     (見出しが無い原稿では先頭の一文)
   - 章に関わる指摘 → その章の見出し (## …) の行を quote にする
   - 特定の段落に関わる指摘 → その段落の冒頭の一文を quote にする
   kind はすべて "pencil"。1 指摘 1 論点。件数は絞る (構成の指摘が 10 件を
   超えるならそれは焦点が定まっていない)。誤字脱字・文単位の言い回しの指摘は
   このモードでは出さない。
   代案は方向性の提示に留め、実際の文章は筆者が書く。

4. 指摘の形式:
   {"id": 一意な文字列, "file": リポジトリ相対パス, "quote": 原稿に完全一致する引用,
    "body": 指摘本文, "author": "claude", "kind": "pencil"}
   既存の指摘 (dismissed/resolved 含む) は変更せず、同じ趣旨の蒸し返しもしない。

5. 原稿本文 (.md) は一切書き換えない。git commit もしない
   (%[2]s への追記だけで終了する)。`

// consultPromptFmt は「相談」の指示 (%[1]s: 原稿パス, %[2]s: 筆者が選択した引用,
// %[3]s: 筆者のメモ, %[4]s: 指摘ファイルのパス)。
// 筆者がしっくりきていない箇所を診断し、言い換えの方向性を返す。
// structurePromptFmt と同じ理由でコミットはしない。
const consultPromptFmt = `あなたはブログ原稿の赤入れ (校閲) 係です。今回は筆者からの「相談」です。筆者が自分でしっくりきていない箇所を指定してきたので、壁打ち相手になってください。

対象の原稿: %[1]s

筆者がしっくりきていない箇所 (原稿からの引用):
%[2]s

筆者のメモ:
%[3]s

1. 原稿全体を読んで、該当箇所の文脈 (前後の流れ・記事全体での役割) をつかむ。

2. %[4]s の annotations 配列に、この相談への返しを 1 件だけ追記する
   (ファイルが無ければ {"annotations": []} から作る):
   - quote は上の引用をそのまま使う (原稿と完全一致していることを確認する)
   - body には次を書く:
     a. 診断: なぜしっくりこないのか、考えられる原因 (リズム・情報の順序・
        前後とのつながり・言葉のチョイス等) を筆者のメモも踏まえて具体的に
     b. 方向性の違う書き換え案を 2〜3 個。どれも筆者の語彙と文体 (だ・である調
        + 口語崩し) を尊重し、ですます調にしない。案ごとに狙いの違いを一言添える
     c. 「今のままでも成立する」なら正直にそう言ってよい
   - kind は "pencil"、author は "claude"
   採用するかどうか、どの案を選ぶかは筆者が決める。

3. 原稿本文 (.md) は一切書き換えない。git commit もしない
   (%[4]s への追記だけで終了する)。`

// docTime は原稿の「新しさ」を返す。Hugo 記事は frontmatter の date
// (git checkout では mtime が当てにならないため)、それ以外は mtime。
// annPath は原稿の指摘ファイルのパスを返す。原稿と同じディレクトリに置き、
// 先頭をドットにして Hugo から見えないようにする。
// 例: content/posts/foo/index.md → content/posts/foo/.index.akaire.json
func annPath(doc string) string {
	dir, base := filepath.Split(doc)
	return dir + "." + strings.TrimSuffix(base, ".md") + ".akaire.json"
}

// labelColor は label 名から決定的にパステル系の色を選ぶ (gh label create 用)
func labelColor(name string) string {
	palette := []string{"bfdadc", "f9d0c4", "c2e0c6", "fef2c0", "d4c5f9", "bfd4f2", "e99695", "c5def5"}
	h := 0
	for _, r := range name {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	return palette[h%len(palette)]
}

// readCategories は .md の frontmatter から categories を取り出す。
// インライン形式 (categories: ["A","B"] / [go]) と、続く行に - で並べる
// リスト形式の両方を受け付ける。frontmatter が無ければ nil を返す。
func readCategories(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	body, ok := strings.CutPrefix(string(b), "---\n")
	if !ok {
		return nil
	}
	fm, _, ok := strings.Cut(body, "\n---")
	if !ok {
		return nil
	}
	lines := strings.Split(fm, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, "categories:")
		if !ok {
			continue
		}
		var cats []string
		add := func(s string) {
			if c := strings.Trim(strings.TrimSpace(s), `"'`); c != "" {
				cats = append(cats, c)
			}
		}
		rest = strings.TrimSpace(rest)
		if inner, ok := strings.CutPrefix(rest, "["); ok {
			for _, f := range strings.Split(strings.TrimSuffix(inner, "]"), ",") {
				add(f)
			}
			return cats
		}
		for _, l := range lines[i+1:] {
			item, ok := strings.CutPrefix(strings.TrimSpace(l), "- ")
			if !ok {
				break
			}
			add(item)
		}
		return cats
	}
	return nil
}

func docTime(path string, mtime time.Time) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return mtime
	}
	defer f.Close()
	head := make([]byte, 2048)
	n, _ := io.ReadFull(f, head)
	lines := strings.Split(string(head[:n]), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return mtime
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "date:"); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			// 新しい記事は RFC3339、Octopress 時代の記事は空白区切り
			for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700"} {
				if t, err := time.Parse(layout, v); err == nil {
					return t
				}
			}
		}
	}
	return mtime
}

// safeName はクエリの file パラメータを data ディレクトリ配下の相対パスに限定する。
// サブディレクトリは許すが、絶対パスや ".." での脱出は拒否する。
func safeName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := filepath.Clean(filepath.FromSlash(r.URL.Query().Get("file")))
	if !strings.HasSuffix(name, ".md") || !filepath.IsLocal(name) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return "", false
	}
	return name, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	if os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("%v", err), http.StatusInternalServerError)
}
