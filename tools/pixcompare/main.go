package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

// Reads two PNGs and prints the RGB of specific (y, x) pairs.
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: pixcompare <a.png> <b.png> [y1 x1 [y2 x2 ...]]")
		os.Exit(1)
	}
	a := load(os.Args[1])
	b := load(os.Args[2])
	args := os.Args[3:]
	if len(args) == 0 {
		// Default: dump around y=614-620, x=0..760 step 20
		for _, y := range []int{614, 615, 616, 617, 618, 619, 620} {
			for x := 0; x < 770; x += 20 {
				dump(a, b, x, y)
			}
			fmt.Println()
		}
		return
	}
	for i := 0; i+1 < len(args); i += 2 {
		var y, x int
		fmt.Sscanf(args[i], "%d", &y)
		fmt.Sscanf(args[i+1], "%d", &x)
		dump(a, b, x, y)
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

func dump(a, b image.Image, x, y int) {
	ar, ag, ab, _ := a.At(x, y).RGBA()
	br, bg, bbl, _ := b.At(x, y).RGBA()
	ar8, ag8, ab8 := byte(ar>>8), byte(ag>>8), byte(ab>>8)
	br8, bg8, bb8 := byte(br>>8), byte(bg>>8), byte(bbl>>8)
	tag := "       "
	if ar != br || ag != bg || ab != bbl {
		tag = " DIFF  "
	}
	fmt.Printf("y=%d x=%d:  a=#%02X%02X%02X  b=#%02X%02X%02X%s\n", y, x, ar8, ag8, ab8, br8, bg8, bb8, tag)
}
