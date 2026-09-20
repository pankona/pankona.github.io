package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestShrinkImage(t *testing.T) {
	// 小さくて不透明: 手を加えない
	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	in := encodePNG(t, small)
	out, ext, note := shrinkImage(in, ".png")
	if !bytes.Equal(out, in) || ext != ".png" || note != "" {
		t.Errorf("small image should be untouched: ext=%s note=%q", ext, note)
	}

	// 幅が大きい: 縮む
	wide := image.NewRGBA(image.Rect(0, 0, 3200, 200))
	out, ext, note = shrinkImage(encodePNG(t, wide), ".png")
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != shrinkMaxWidth || img.Bounds().Dy() != 100 {
		t.Errorf("got %v, want %dx100 (%s)", img.Bounds(), shrinkMaxWidth, note)
	}
	if ext != ".png" {
		t.Errorf("uniform image should stay png, got %s (%s)", ext, note)
	}

	// 大きくてノイズだらけの不透明 PNG: JPEG になる
	noisy := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	seed := uint32(1)
	for i := 0; i < len(noisy.Pix); i += 4 {
		seed = seed*1664525 + 1013904223
		noisy.Pix[i], noisy.Pix[i+1], noisy.Pix[i+2], noisy.Pix[i+3] = uint8(seed>>24), uint8(seed>>16), uint8(seed>>8), 255
	}
	in = encodePNG(t, noisy)
	out, ext, note = shrinkImage(in, ".png")
	if ext != ".jpg" || len(out) >= len(in) {
		t.Errorf("noisy opaque png should become smaller jpeg: ext=%s in=%d out=%d (%s)", ext, len(in), len(out), note)
	}

	// 透過があれば大きくても PNG のまま
	alpha := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for i := 0; i < len(alpha.Pix); i += 4 {
		seed = seed*1664525 + 1013904223
		alpha.Pix[i], alpha.Pix[i+1], alpha.Pix[i+2], alpha.Pix[i+3] = uint8(seed>>24), uint8(seed>>16), uint8(seed>>8), 128
	}
	_, ext, _ = shrinkImage(encodePNG(t, alpha), ".png")
	if ext != ".png" {
		t.Errorf("transparent png must stay png, got %s", ext)
	}
	_ = color.RGBA{}
}
