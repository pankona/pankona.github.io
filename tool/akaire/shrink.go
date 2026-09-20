// 貼り付けられた画像の自動縮小。
//
// スクショをそのまま貼ると 1 MB 級の PNG になりがちなので、保存前に
// (1) 幅を shrinkMaxWidth まで落とし、(2) 透過が無く PNG のままだと大きい場合は
// JPEG に変換する。ImageMagick 等の外部コマンドに頼らず標準ライブラリだけで行う。
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

const (
	shrinkMaxWidth    = 1600      // ブログ本文幅の 2 倍程度 (Retina を考慮)。これより広ければ縮める
	shrinkPNGLimit    = 300 << 10 // 縮小後の PNG がこれより大きく、かつ不透明なら JPEG にする
	shrinkJPEGQuality = 85
)

// shrinkImage は PNG/JPEG を必要に応じて縮小・変換して返す。
// 返り値は (画像データ, 拡張子, 人間向けの説明)。手を加えなかったときは説明が空。
// 対応外の形式やデコードに失敗したときは入力をそのまま返す。
func shrinkImage(b []byte, ext string) ([]byte, string, string) {
	if ext != ".png" && ext != ".jpg" {
		return b, ext, ""
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return b, ext, ""
	}
	w := img.Bounds().Dx()
	var note string
	if w > shrinkMaxWidth {
		img = scaleWidth(img, shrinkMaxWidth)
		note = fmt.Sprintf("%dpx → %dpx", w, shrinkMaxWidth)
	}
	var out bytes.Buffer
	switch ext {
	case ".jpg":
		if note == "" {
			return b, ext, ""
		}
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: shrinkJPEGQuality}); err != nil {
			return b, ext, ""
		}
	case ".png":
		if err := png.Encode(&out, img); err != nil {
			return b, ext, ""
		}
		if out.Len() > shrinkPNGLimit && isOpaque(img) {
			var j bytes.Buffer
			if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: shrinkJPEGQuality}); err == nil && j.Len() < out.Len() {
				out, ext = j, ".jpg"
				note += " PNG → JPEG"
			}
		}
		if note == "" {
			return b, ext, "" // 縮小も変換も不要なら元のバイト列を尊重する
		}
	}
	note = fmt.Sprintf("%s (%dKB → %dKB)", note, len(b)>>10, out.Len()>>10)
	return out.Bytes(), ext, note
}

// isOpaque は画像に透過ピクセルが無いかを (間引いて) 調べる
func isOpaque(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 5 {
		for x := b.Min.X; x < b.Max.X; x += 5 {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// scaleWidth は幅 w に縮小する。各出力ピクセルに対応する元の矩形の平均を取る
// (box filter)。拡大は想定しない
func scaleWidth(src image.Image, w int) image.Image {
	b := src.Bounds()
	h := b.Dy() * w / b.Dx()
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fx, fy := float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)
	for y := 0; y < h; y++ {
		y0, y1 := int(float64(y)*fy), int(float64(y+1)*fy)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0, x1 := int(float64(x)*fx), int(float64(x+1)*fx)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] =
				uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), uint8(a/n>>8)
		}
	}
	return dst
}
