package main

import (
	_ "embed"

	"github.com/hajimehoshi/ebiten/v2"
)

// CRT filter — a Kage shader that adds scanlines and an RGB phosphor
// mask to the scaled NES output, giving it a rough aperture-grille CRT
// look. Enabled via the Emulation menu or F3 hotkey; the choice is
// persisted to balrog.cfg.
//
// The shader runs in "pixels" unit mode so dstPos is the destination
// fragment coordinate (output pixels after scaling) and srcPos is the
// source image coordinate (NES pixels 0..256, 0..240). That means:
//
//   - Scanlines track destination rows — every other output row is
//     dimmed, giving visible scanlines regardless of integer scale.
//   - Phosphor mask tracks destination columns — each group of three
//     output columns tints R / G / B to fake an aperture-grille
//     subpixel pattern.
//
// The whole output is brightness-compensated so the mask and
// scanlines don't make the picture look dim.

//go:embed crt.kage
var crtShaderSrc []byte

// makeCRTShader compiles the embedded Kage shader. Called once at
// startup; a nil shader disables the filter gracefully rather than
// crashing the emulator.
func makeCRTShader() *ebiten.Shader {
	s, err := ebiten.NewShader(crtShaderSrc)
	if err != nil {
		return nil
	}
	return s
}
