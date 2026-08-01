package main

import (
	"image"
	_ "image/png"
	"io"
	"testing"
)

// decodePNG reads a 1-pixel-per-module QR image into a matrix. Dark = true.
func decodePNG(t *testing.T, r io.Reader) [][]bool {
	t.Helper()
	img, _, err := image.Decode(r)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	b := img.Bounds()
	out := make([][]bool, b.Dy())
	for y := range out {
		out[y] = make([]bool, b.Dx())
		for x := range out[y] {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			out[y][x] = r+g+bl < 0x8000*3 // dark module
		}
	}
	return out
}
