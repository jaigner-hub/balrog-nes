package main

import (
	"fmt"
	"image/png"
	"os"
)

// Dump a row of pixels from a PNG.
// usage: pixdump <file> <y> [x0] [x1]
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: pixdump <file> <y> [x0] [x1]")
		os.Exit(1)
	}
	f, _ := os.Open(os.Args[1])
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	var y, x0, x1 int
	fmt.Sscanf(os.Args[2], "%d", &y)
	x0 = 0
	x1 = img.Bounds().Dx()
	if len(os.Args) >= 4 {
		fmt.Sscanf(os.Args[3], "%d", &x0)
	}
	if len(os.Args) >= 5 {
		fmt.Sscanf(os.Args[4], "%d", &x1)
	}
	for x := x0; x < x1; x++ {
		r, g, b, _ := img.At(x, y).RGBA()
		fmt.Printf("x=%d #%02X%02X%02X\n", x, byte(r>>8), byte(g>>8), byte(b>>8))
	}
}
