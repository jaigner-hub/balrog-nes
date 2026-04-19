package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: zoomtop <in.png> <out.png>")
		os.Exit(1)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	// Crop top 40 rows, upscale 4x with nearest-neighbor so we can read pixels.
	const cropH = 40
	const scale = 4
	w, h := src.Bounds().Dx(), cropH
	if h > src.Bounds().Dy() {
		h = src.Bounds().Dy()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(x, y)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					dst.Set(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	out, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	defer out.Close()
	png.Encode(out, dst)
}
