package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

// Counts per-scanline SIGNIFICANT pixel differences (ignores compression noise).
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: framediff <a.png> <b.png> [threshold]")
		os.Exit(1)
	}
	a := load(os.Args[1])
	b := load(os.Args[2])
	threshold := 16 // per-channel
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &threshold)
	}
	w := a.Bounds().Dx()
	h := a.Bounds().Dy()
	for y := 0; y < h; y++ {
		diffs := 0
		firstX := -1
		for x := 0; x < w; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bbl, _ := b.At(x, y).RGBA()
			ar8, ag8, ab8 := int(ar>>8), int(ag>>8), int(ab>>8)
			br8, bg8, bb8 := int(br>>8), int(bg>>8), int(bbl>>8)
			dr := ar8 - br8
			if dr < 0 {
				dr = -dr
			}
			dg := ag8 - bg8
			if dg < 0 {
				dg = -dg
			}
			db := ab8 - bb8
			if db < 0 {
				db = -db
			}
			if dr > threshold || dg > threshold || db > threshold {
				if diffs == 0 {
					firstX = x
				}
				diffs++
			}
		}
		if diffs > 0 {
			fmt.Printf("scanline %d: %d pixels differ significantly, first at x=%d\n", y, diffs, firstX)
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
