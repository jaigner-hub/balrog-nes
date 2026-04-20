package main

import (
	"fmt"
	"image/png"
	"os"
)

// Render a compact "row map" for given y range: W for white, B for black, '.' otherwise.
// usage: rowmap <file> <y0> <y1> <step_x>
func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: rowmap <file> <y0> <y1> <step_x>")
		os.Exit(1)
	}
	f, _ := os.Open(os.Args[1])
	img, _ := png.Decode(f)
	f.Close()
	var y0, y1, step int
	fmt.Sscanf(os.Args[2], "%d", &y0)
	fmt.Sscanf(os.Args[3], "%d", &y1)
	fmt.Sscanf(os.Args[4], "%d", &step)
	w := img.Bounds().Dx()
	for y := y0; y <= y1; y++ {
		fmt.Printf("y=%3d: ", y)
		for x := 0; x < w; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			r8, g8, b8 := byte(r>>8), byte(g>>8), byte(b>>8)
			if r8 > 240 && g8 > 240 && b8 > 240 {
				fmt.Print("W")
			} else if r8 < 16 && g8 < 16 && b8 < 16 {
				fmt.Print("B")
			} else {
				fmt.Print(".")
			}
		}
		fmt.Println()
	}
}
