package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v33/github"
	"golang.org/x/oauth2"
)

func main() {
	issueNum := flag.Int("issue-num", 0, "specify issue number to convert to blog post")
	slug := flag.String("slug", "", "page bundle directory name (required)")
	contentDir := flag.String("content-dir", "content/posts", "destination content directory")
	flag.Parse()

	if *slug == "" {
		fmt.Fprintln(os.Stderr, "--slug is required")
		os.Exit(2)
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	issue, resp, err := client.Issues.Get(ctx, "pankona", "pankona.github.io", *issueNum)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	bundleDir := filepath.Join(*contentDir, *slug)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		panic(err)
	}

	body := *issue.Body
	title := extractTitleFromBody(body)
	body = removeTitleFromBody(body)

	dl := &imageDownloader{bundleDir: bundleDir, client: http.DefaultClient}
	body, err = transformBody(body, dl)
	if err != nil {
		panic(err)
	}

	out, err := os.Create(filepath.Join(bundleDir, "index.md"))
	if err != nil {
		panic(err)
	}
	defer out.Close()

	err = write(out, Article{
		Title: title,
		Body:  body,
		Date:  *issue.CreatedAt,
		Draft: strings.Contains(*issue.Title, "[draft]"),
		Categories: func() []string {
			ret := make([]string, 0, len(issue.Labels))
			for _, label := range issue.Labels {
				labelName := label.GetName()
				if labelName != "article" && labelName != "" {
					ret = append(ret, labelName)
				}
			}
			return ret
		}(),
	})
	if err != nil {
		panic(err)
	}
}

func extractTitleFromBody(body string) string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	if scanner.Scan() {
		return scanner.Text()
	}
	return "no title"
}

func removeTitleFromBody(body string) string {
	scanner := bufio.NewScanner(strings.NewReader(body))

	ret := []string{}
	i := 0
	for scanner.Scan() {
		if i == 0 || i == 1 {
			i++
			continue
		}
		ret = append(ret, scanner.Text())
	}
	return strings.Join(ret, "\n")
}

// transformBody は本文を行単位で走査し、(1) GitHub の添付画像 URL を
// バンドル直下にダウンロードして相対参照へ書き換え、(2) 単独行の URL を
// {{< linkcard "..." >}} ショートコードへ置換する。コードブロック (``` で
// 囲まれた範囲) の中は変換対象にしない。
// linkcard / 画像参照は前後に空行を挟まないと Markdown 解析時に段落へ
// 取り込まれてしまうので、置換した行の前後に空行を挿入してから最後に連続
// 空行を 1 つへ正規化する。
func transformBody(body string, dl *imageDownloader) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []string
	inCode := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCode = !inCode
			out = append(out, line)
			continue
		}
		if inCode {
			out = append(out, line)
			continue
		}

		rewritten, err := rewriteImages(line, dl)
		if err != nil {
			return "", err
		}
		rewritten = rewriteLinkcard(rewritten)

		if needsBlankSurround(rewritten) {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, rewritten, "")
			continue
		}
		out = append(out, rewritten)
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	joined := strings.Join(out, "\n")
	joined = regexp.MustCompile(`\n{3,}`).ReplaceAllString(joined, "\n\n")
	return joined, nil
}

// needsBlankSurround は行が linkcard ショートコードか、または画像のみの
// 行であるかを判定する。これらはブロック要素として独立段落になってほしい
// ので前後を空行で挟む。
func needsBlankSurround(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{{<") && strings.HasSuffix(trimmed, ">}}") {
		return true
	}
	if imgOnlyRe.MatchString(trimmed) {
		return true
	}
	return false
}

var (
	htmlImgRe = regexp.MustCompile(`(?i)<img\s+[^>]*>`)
	htmlSrcRe = regexp.MustCompile(`(?i)\bsrc=["']([^"']+)["']`)
	htmlAltRe = regexp.MustCompile(`(?i)\balt=["']([^"']*)["']`)
	mdImgRe   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	urlOnlyRe = regexp.MustCompile(`^https?://\S+$`)
	imgOnlyRe = regexp.MustCompile(`^!\[[^\]]*\]\([^)]+\)$`)
)

func rewriteImages(line string, dl *imageDownloader) (string, error) {
	var firstErr error

	line = htmlImgRe.ReplaceAllStringFunc(line, func(tag string) string {
		srcMatch := htmlSrcRe.FindStringSubmatch(tag)
		if len(srcMatch) < 2 {
			return tag
		}
		url := srcMatch[1]
		if !isAttachedImage(url) {
			return tag
		}
		filename, err := dl.fetch(url)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return tag
		}
		alt := ""
		if m := htmlAltRe.FindStringSubmatch(tag); len(m) >= 2 {
			alt = m[1]
		}
		if alt == "" || strings.EqualFold(alt, "image") {
			alt = "image"
		}
		return fmt.Sprintf("![%s](%s)", alt, filename)
	})
	if firstErr != nil {
		return "", firstErr
	}

	line = mdImgRe.ReplaceAllStringFunc(line, func(m string) string {
		ms := mdImgRe.FindStringSubmatch(m)
		if len(ms) < 3 {
			return m
		}
		alt, url := ms[1], ms[2]
		if !isAttachedImage(url) {
			return m
		}
		filename, err := dl.fetch(url)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		return fmt.Sprintf("![%s](%s)", alt, filename)
	})
	if firstErr != nil {
		return "", firstErr
	}

	return line, nil
}

func rewriteLinkcard(line string) string {
	trimmed := strings.TrimSpace(line)
	if !urlOnlyRe.MatchString(trimmed) {
		return line
	}
	return fmt.Sprintf(`{{< linkcard "%s" >}}`, trimmed)
}

func isAttachedImage(url string) bool {
	return strings.HasPrefix(url, "https://github.com/user-attachments/") ||
		strings.HasPrefix(url, "https://user-images.githubusercontent.com/") ||
		strings.HasPrefix(url, "https://private-user-images.githubusercontent.com/")
}

// imageDownloader は URL → ローカルファイル名 のマッピングを保持し、同一 URL に対しては
// 同じファイル名を返す。連番でファイル名を割り当てる。
type imageDownloader struct {
	bundleDir string
	client    *http.Client
	cache     map[string]string
	counter   int
}

func (d *imageDownloader) fetch(url string) (name string, err error) {
	if d.cache == nil {
		d.cache = map[string]string{}
	}
	if name, ok := d.cache[url]; ok {
		return name, nil
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "articlegen/1.0 (+https://github.com/pankona/pankona.github.io)")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("articlegen: GET %s returned HTTP %d", url, resp.StatusCode)
	}

	d.counter++
	buf := &bytes.Buffer{}
	if _, err := io.CopyN(buf, resp.Body, 512); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	ext := extFromContentType(resp.Header.Get("Content-Type"))
	if ext == "" {
		ext = extFromMagic(buf.Bytes())
	}
	if ext == "" {
		return "", fmt.Errorf("articlegen: cannot determine image extension for %s (Content-Type=%q)", url, resp.Header.Get("Content-Type"))
	}

	name = fmt.Sprintf("image-%d%s", d.counter, ext)
	path := filepath.Join(d.bundleDir, name)
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	if _, err = out.Write(buf.Bytes()); err != nil {
		return "", err
	}
	if _, err = io.Copy(out, resp.Body); err != nil {
		return "", err
	}

	d.cache[url] = name
	return name, nil
}

func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	}
	return ""
}

func extFromMagic(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return ".png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return ".jpg"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	}
	return ""
}

type Article struct {
	Title      string
	Body       string
	Date       time.Time
	Draft      bool
	Categories []string
}

func write(w io.Writer, article Article) error {
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return err
	}
	data := map[string]interface{}{
		"Title": article.Title,
		"Date":  article.Date.In(jst).Format(time.RFC3339),
		"Draft": article.Draft,
		"Categories": func() string {
			ret := make([]string, 0, len(article.Categories))
			for _, c := range article.Categories {
				ret = append(ret, strconv.Quote(c))
			}
			return strings.Join(ret, ",")
		}(),
		"Body": article.Body,
	}

	const articleTemplate = `---
title: >-
  {{.Title}}
date: {{.Date}}
draft: {{.Draft}}
categories: [{{.Categories}}]
---

{{.Body}}
`

	t, err := template.New("article").Parse(articleTemplate)
	if err != nil {
		return err
	}

	return t.Execute(w, data)
}
