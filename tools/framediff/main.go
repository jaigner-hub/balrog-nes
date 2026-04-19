package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: framediff <a.png> <b.png>")
		os.Exit(1)
	}
	a := load(os.Args[1])
	b := load(os.Args[2])
	w := a.Bounds().Dx()
	h := a.Bounds().Dy()
	for y := 0; y < h; y++ {
		diffs := 0
		firstX := -1
		for x := 0; x < w; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb {
				if diffs == 0 {
					firstX = x
				}
				diffs++
			}
		}
		if diffs > 0 {
			fmt.Printf("scanline %d: %d pixels differ, first at x=%d\n", y, diffs, firstX)
		}
	}
}

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		panic(err)
	}
	return img
}
