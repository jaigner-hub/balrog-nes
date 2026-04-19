package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

const audioSampleRate = 44100

const (
	screenW = 256
	screenH = 240
	scale   = 3
)

type pressSpec struct {
	from, to uint64
	button   byte
}

type snapSpec struct {
	at   uint64
	path string
}

type Game struct {
	nes     *NES
	screen  *ebiten.Image
	pixels  []byte
	frames  uint64
	snaps   []snapSpec
	presses []pressSpec
	exitAt  uint64
}

func (g *Game) saveSnapshot(path string) {
	img := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	for i, c := range g.nes.PPU.Frame {
		img.SetRGBA(i%screenW, i/screenW, color.RGBA{
			R: byte(c >> 24), G: byte(c >> 16), B: byte(c >> 8), A: 0xFF,
		})
	}
	f, err := os.Create(path)
	if err != nil {
		log.Printf("snapshot: %v", err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
	log.Printf("snapshot saved to %s (frame %d)", path, g.frames)
}

// readGamepad scans connected gamepads and returns NES button bits.
// Maps the physical Nintendo layout (A=right-button, B=bottom-button) so an
// 8bitdo in SNES/Switch mode works naturally. If the pad reports a standard
// layout, we use Ebiten's StandardGamepadButton API (works across 8bitdo,
// Xbox, DualShock, etc.). Otherwise we fall back to raw axes for D-pad.
func (g *Game) readGamepad() byte {
	var b byte
	ids := ebiten.AppendGamepadIDs(nil)
	for _, id := range ids {
		if ebiten.IsStandardGamepadLayoutAvailable(id) {
			// NES A <- SNES/Switch A (right-face-right) or Xbox B
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightRight) {
				b |= 0x01
			}
			// NES B <- SNES/Switch B (right-face-bottom) or Xbox A
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightBottom) {
				b |= 0x02
			}
			// Also accept Y as NES B and X as NES A for turbo comfort
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightLeft) {
				b |= 0x02
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightTop) {
				b |= 0x01
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterLeft) {
				b |= 0x04 // Select / Back
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterRight) {
				b |= 0x08 // Start / Menu
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftTop) {
				b |= 0x10
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftBottom) {
				b |= 0x20
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftLeft) {
				b |= 0x40
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftRight) {
				b |= 0x80
			}
			// Analog-stick fallback on top of D-pad
			ax := ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickHorizontal)
			ay := ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickVertical)
			const dead = 0.4
			if ay < -dead {
				b |= 0x10
			}
			if ay > dead {
				b |= 0x20
			}
			if ax < -dead {
				b |= 0x40
			}
			if ax > dead {
				b |= 0x80
			}
		}
	}
	return b
}

func (g *Game) Update() error {
	// Map keyboard -> controller 1 (A B Sel Start Up Down Left Right)
	var b byte
	if ebiten.IsKeyPressed(ebiten.KeyX) {
		b |= 0x01
	} // A
	if ebiten.IsKeyPressed(ebiten.KeyZ) {
		b |= 0x02
	} // B
	if ebiten.IsKeyPressed(ebiten.KeyShiftRight) {
		b |= 0x04
	} // Select
	if ebiten.IsKeyPressed(ebiten.KeyEnter) {
		b |= 0x08
	} // Start
	if ebiten.IsKeyPressed(ebiten.KeyArrowUp) {
		b |= 0x10
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowDown) {
		b |= 0x20
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowLeft) {
		b |= 0x40
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowRight) {
		b |= 0x80
	}
	// Gamepad (8bitdo, Xbox, etc.) -> NES controller 1
	b |= g.readGamepad()
	// Scripted inputs (overlay on real keys)
	for _, p := range g.presses {
		if g.frames >= p.from && g.frames < p.to {
			b |= p.button
		}
	}
	g.nes.Bus.Ctrl[0].Buttons = b

	g.nes.StepFrame()
	src := g.nes.PPU.Frame[:]
	for i, c := range src {
		o := i * 4
		g.pixels[o] = byte(c >> 24)
		g.pixels[o+1] = byte(c >> 16)
		g.pixels[o+2] = byte(c >> 8)
		g.pixels[o+3] = byte(c)
	}
	g.screen.WritePixels(g.pixels)
	g.frames++
	for _, s := range g.snaps {
		if g.frames == s.at {
			g.saveSnapshot(s.path)
		}
	}
	if g.exitAt > 0 && g.frames >= g.exitAt {
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	op := &ebiten.DrawImageOptions{}
	screen.DrawImage(g.screen, op)
	ebitenutil.DebugPrint(screen, fmt.Sprintf("%.1f FPS", ebiten.ActualFPS()))
}

func (g *Game) Layout(ow, oh int) (int, int) { return screenW, screenH }

// apuReader adapts the APU ring buffer into the 16-bit stereo PCM stream
// Ebiten's audio player expects. If the buffer underflows we output silence.
type apuReader struct {
	apu *APU
}

func (r *apuReader) Read(p []byte) (int, error) {
	// Each stereo frame is 4 bytes (int16 L + int16 R).
	n := len(p) / 4
	for i := 0; i < n; i++ {
		s, ok := r.apu.PullSample()
		if !ok {
			s = 0
		}
		// APU output is roughly 0..1; scale and clip to int16.
		v := s * 20000
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		iv := int16(v)
		p[i*4+0] = byte(iv)
		p[i*4+1] = byte(iv >> 8)
		p[i*4+2] = byte(iv)
		p[i*4+3] = byte(iv >> 8)
	}
	return n * 4, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: balrog <rom.nes>")
		os.Exit(1)
	}
	cart, err := LoadCart(os.Args[1])
	if err != nil {
		log.Fatalf("load cart: %v", err)
	}
	fmt.Printf("Loaded ROM: mapper=%d PRG=%d CHR=%d mirror=%d\n",
		cart.MapperID, len(cart.PRG), len(cart.CHR), cart.initMirror)
	g := &Game{
		nes:    NewNES(cart, float64(audioSampleRate)),
		screen: ebiten.NewImage(screenW, screenH),
		pixels: make([]byte, screenW*screenH*4),
	}
	// Set up audio: Ebiten audio context at 44.1 kHz, stereo 16-bit.
	actx := audio.NewContext(audioSampleRate)
	player, err := actx.NewPlayer(&apuReader{apu: g.nes.APU})
	if err != nil {
		log.Fatalf("audio player: %v", err)
	}
	// Buffer ~50ms; larger = less underflow but more latency.
	player.SetBufferSize(200 * time.Millisecond)
	player.Play()
	// CLI scripting:
	//   --snap <frame> <path>           capture single frame to PNG
	//   --press <from> <to> <button>    hold button in [from,to). Button names:
	//                                   A B SELECT START UP DOWN LEFT RIGHT
	//   --exit <frame>                  exit after this frame
	btns := map[string]byte{
		"A": 0x01, "B": 0x02, "SELECT": 0x04, "START": 0x08,
		"UP": 0x10, "DOWN": 0x20, "LEFT": 0x40, "RIGHT": 0x80,
	}
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--snap":
			if i+2 < len(os.Args) {
				var n uint64
				fmt.Sscanf(os.Args[i+1], "%d", &n)
				g.snaps = append(g.snaps, snapSpec{at: n, path: os.Args[i+2]})
				i += 2
			}
		case "--press":
			if i+3 < len(os.Args) {
				var from, to uint64
				fmt.Sscanf(os.Args[i+1], "%d", &from)
				fmt.Sscanf(os.Args[i+2], "%d", &to)
				btn := btns[os.Args[i+3]]
				g.presses = append(g.presses, pressSpec{from: from, to: to, button: btn})
				i += 3
			}
		case "--exit":
			if i+1 < len(os.Args) {
				var n uint64
				fmt.Sscanf(os.Args[i+1], "%d", &n)
				g.exitAt = n
				i++
			}
		}
	}
	ebiten.SetWindowSize(screenW*scale, screenH*scale)
	ebiten.SetWindowTitle("balrog NES - " + os.Args[1])
	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}
