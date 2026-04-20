package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

// Crop a horizontal band [y0,y1] and upscale 4x for inspection.
func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: zoomband <in.png> <out.png> <y0> <y1>")
		os.Exit(1)
	}
	var y0, y1 int
	fmt.Sscanf(os.Args[3], "%d", &y0)
	fmt.Sscanf(os.Args[4], "%d", &y1)
	f, _ := os.Open(os.Args[1])
	src, _ := png.Decode(f)
	f.Close()
	const scale = 4
	w := src.Bounds().Dx()
	h := y1 - y0 + 1
	dst := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(x, y0+y)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					dst.Set(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	out, _ := os.Create(os.Args[2])
	defer out.Close()
	png.Encode(out, dst)
}
