package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/sqweek/dialog"
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
	// nesPtr holds *NES atomically so the audio reader (running on its own
	// goroutine) can swap to a new emulator instance when the user opens a
	// different ROM without tearing down the audio player.
	nesPtr  atomic.Pointer[NES]
	screen  *ebiten.Image
	pixels  []byte
	frames  uint64
	snaps   []snapSpec
	presses []pressSpec
	exitAt  uint64

	romName     string
	loading     atomic.Bool
	statusMsg   string
	statusUntil time.Time
}

func (g *Game) nes() *NES { return g.nesPtr.Load() }

func (g *Game) saveSnapshot(path string) {
	nes := g.nes()
	if nes == nil {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	for i, c := range nes.PPU.Frame {
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

func (g *Game) readGamepad() byte {
	var b byte
	ids := ebiten.AppendGamepadIDs(nil)
	for _, id := range ids {
		if ebiten.IsStandardGamepadLayoutAvailable(id) {
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightRight) {
				b |= 0x01
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightBottom) {
				b |= 0x02
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightLeft) {
				b |= 0x02
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightTop) {
				b |= 0x01
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterLeft) {
				b |= 0x04
			}
			if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterRight) {
				b |= 0x08
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

// openROMDialog runs the native file picker on a goroutine and, on success,
// installs the new NES instance via atomic swap. We don't block the UI thread.
func (g *Game) openROMDialog() {
	if !g.loading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer g.loading.Store(false)
		path, err := dialog.File().Filter("NES ROM", "nes").Title("Open ROM").Load()
		if err != nil {
			// dialog.ErrCancelled is normal — user closed the picker
			if err.Error() != "Cancelled" {
				g.setStatus("dialog: "+err.Error(), 3*time.Second)
			}
			return
		}
		g.loadROM(path)
	}()
}

func (g *Game) loadROM(path string) {
	cart, err := LoadCart(path)
	if err != nil {
		g.setStatus("load failed: "+err.Error(), 4*time.Second)
		return
	}
	g.installCart(cart, filepath.Base(path))
}

func (g *Game) installCart(cart *Cart, name string) {
	nes := NewNES(cart, float64(audioSampleRate))
	g.nesPtr.Store(nes)
	g.romName = name
	ebiten.SetWindowTitle("balrog NES - " + name)
	g.frames = 0
	g.setStatus(fmt.Sprintf("loaded %s (mapper %d)", name, cart.MapperID), 2*time.Second)
}

// loadDroppedROM walks the dropped fs.FS, finds the first .nes file, reads it,
// and installs it. Ebiten exposes dropped files as a virtual filesystem rather
// than absolute paths (for WASM portability), so we read the bytes directly.
func (g *Game) loadDroppedROM(files fs.FS) {
	var picked string
	_ = fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ".nes" {
			picked = p
			return fs.SkipAll
		}
		return nil
	})
	if picked == "" {
		g.setStatus("no .nes file in drop", 3*time.Second)
		return
	}
	f, err := files.Open(picked)
	if err != nil {
		g.setStatus("open dropped: "+err.Error(), 3*time.Second)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		g.setStatus("read dropped: "+err.Error(), 3*time.Second)
		return
	}
	cart, err := LoadCartBytes(data)
	if err != nil {
		g.setStatus("load failed: "+err.Error(), 4*time.Second)
		return
	}
	g.installCart(cart, filepath.Base(picked))
}

func (g *Game) setStatus(s string, d time.Duration) {
	g.statusMsg = s
	g.statusUntil = time.Now().Add(d)
}

func (g *Game) Update() error {
	// Hotkeys: F1 / Ctrl+O = open file dialog; F5 = reset
	if inpututil.IsKeyJustPressed(ebiten.KeyF1) ||
		(ebiten.IsKeyPressed(ebiten.KeyControl) && inpututil.IsKeyJustPressed(ebiten.KeyO)) {
		g.openROMDialog()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF5) {
		if nes := g.nes(); nes != nil {
			nes.CPU.Reset()
			g.setStatus("reset", time.Second)
		}
	}
	// Drag-and-drop: load any .nes file dropped on the window.
	if files := ebiten.DroppedFiles(); files != nil {
		go g.loadDroppedROM(files)
	}

	nes := g.nes()
	if nes == nil {
		return nil
	}

	var b byte
	if ebiten.IsKeyPressed(ebiten.KeyX) {
		b |= 0x01
	}
	if ebiten.IsKeyPressed(ebiten.KeyZ) {
		b |= 0x02
	}
	if ebiten.IsKeyPressed(ebiten.KeyShiftRight) {
		b |= 0x04
	}
	if ebiten.IsKeyPressed(ebiten.KeyEnter) {
		b |= 0x08
	}
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
	b |= g.readGamepad()
	for _, p := range g.presses {
		if g.frames >= p.from && g.frames < p.to {
			b |= p.button
		}
	}
	nes.Bus.Ctrl[0].Buttons = b

	nes.StepFrame()
	src := nes.PPU.Frame[:]
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
	if g.nes() != nil {
		op := &ebiten.DrawImageOptions{}
		screen.DrawImage(g.screen, op)
	} else {
		// Placeholder when no ROM is loaded.
		screen.Fill(color.RGBA{0x10, 0x10, 0x18, 0xFF})
		ebitenutil.DebugPrintAt(screen, "balrog NES emulator", 8, 90)
		ebitenutil.DebugPrintAt(screen, "Press F1 (or Ctrl+O) to open a ROM", 8, 110)
		ebitenutil.DebugPrintAt(screen, "or drag a .nes file onto this window", 8, 122)
	}
	if time.Now().Before(g.statusUntil) {
		ebitenutil.DebugPrintAt(screen, g.statusMsg, 4, screenH-12)
	} else if g.nes() != nil {
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%.1f FPS", ebiten.ActualFPS()), 4, 4)
	}
}

func (g *Game) Layout(ow, oh int) (int, int) { return screenW, screenH }

// apuReader pulls samples from whatever NES instance is currently loaded.
// Returns silence when no ROM is loaded or the buffer underflows.
type apuReader struct {
	game *Game
}

func (r *apuReader) Read(p []byte) (int, error) {
	n := len(p) / 4
	nes := r.game.nes()
	for i := 0; i < n; i++ {
		var s float32
		if nes != nil {
			if v, ok := nes.APU.PullSample(); ok {
				s = v
			}
		}
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
	g := &Game{
		screen: ebiten.NewImage(screenW, screenH),
		pixels: make([]byte, screenW*screenH*4),
	}
	// Optional positional ROM arg
	romArg := ""
	if len(os.Args) >= 2 && len(os.Args[1]) > 0 && os.Args[1][0] != '-' {
		romArg = os.Args[1]
	}
	if romArg != "" {
		g.loadROM(romArg)
	}

	actx := audio.NewContext(audioSampleRate)
	player, err := actx.NewPlayer(&apuReader{game: g})
	if err != nil {
		log.Fatalf("audio player: %v", err)
	}
	player.SetBufferSize(200 * time.Millisecond)
	player.Play()

	// CLI scripting flags (work whether or not a ROM was given upfront)
	btns := map[string]byte{
		"A": 0x01, "B": 0x02, "SELECT": 0x04, "START": 0x08,
		"UP": 0x10, "DOWN": 0x20, "LEFT": 0x40, "RIGHT": 0x80,
	}
	start := 1
	if romArg != "" {
		start = 2
	}
	for i := start; i < len(os.Args); i++ {
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
				g.presses = append(g.presses, pressSpec{from: from, to: to, button: btns[os.Args[i+3]]})
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
	if g.romName == "" {
		ebiten.SetWindowTitle("balrog NES")
	}
	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}
