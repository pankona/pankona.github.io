// Package imagemeta は画像バイト列や Content-Type からファイル拡張子を
// 判定するヘルパ。articlegen (issue からの画像取り込み) と akaire
// (エディタへの画像ペースト) で同じ判定を使い、生成される記事の形を揃える。
package imagemeta

import (
	"bytes"
	"strings"
)

// ExtFromContentType は Content-Type ヘッダから拡張子を返す。不明なら空文字。
func ExtFromContentType(ct string) string {
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

// ExtFromMagic は先頭バイトのマジックナンバーから拡張子を返す。不明なら空文字。
func ExtFromMagic(b []byte) string {
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
