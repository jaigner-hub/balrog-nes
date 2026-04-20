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
	"strconv"
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

	romPath     string
	romName     string
	loading     atomic.Bool
	statusMsg   string
	statusUntil time.Time

	audioPlayer  *audio.Player
	audioStarted bool

	autoLoadState     bool
	autoLoadStateDone bool
	cycPerFrame       bool
	lastFrameCycles   uint64
	traceCpuFrame     uint64

	stateSlot int // currently-active save state slot (0..numStateSlots-1)

	menuBar     *menuBar
	inputCfg    *InputConfig
	inputDialog *InputDialog

	// --test-dialog: at the given frame, open the input config dialog
	// and capture the whole window (menu bar + dialog overlay) to path.
	// Purely for UI validation; not useful to end users.
	testDialogAt   uint64
	testDialogPath string
	pendingWinSnap string // set by testDialog logic, consumed in Draw
}

// Number of save-state slots exposed to the user (0-9). Plenty for
// Mario-style practice saves without turning the file picker into
// a novel; the keyboard UI (F6/F7 to cycle) is still quick at 10.
const numStateSlots = 10

// statePathSlot returns the save-state file path for the current ROM
// and the given slot. Slot 0 keeps the legacy "<rom>.state" name so
// save states written by older balrog builds still load. Slots 1–9
// use "<rom>.stateN".
func (g *Game) statePathSlot(slot int) string {
	if g.romPath == "" {
		return ""
	}
	ext := filepath.Ext(g.romPath)
	base := g.romPath[:len(g.romPath)-len(ext)]
	if slot == 0 {
		return base + ".state"
	}
	return base + ".state" + strconv.Itoa(slot)
}

// statePath returns the path for the currently-active slot.
func (g *Game) statePath() string { return g.statePathSlot(g.stateSlot) }

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

// Controller reading moved to InputConfig.readController (input.go), which
// consults user-editable bindings stored in balrog.cfg.

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
	g.romPath = path
	g.installCart(cart, filepath.Base(path))
}

func (g *Game) installCart(cart *Cart, name string) {
	nes := NewNES(cart, float64(audioSampleRate))
	g.nesPtr.Store(nes)
	g.romName = name
	g.refreshWindowTitle()
	g.frames = 0
	g.setStatus(fmt.Sprintf("loaded %s (mapper %d)", name, cart.MapperID), 2*time.Second)
	// Start audio on first ROM load. Holding off until now keeps the audio
	// pipeline from buffering up silence while the user picks a file.
	if g.audioPlayer != nil && !g.audioStarted {
		g.audioPlayer.Play()
		g.audioStarted = true
	}
}

// loadDroppedROM walks the dropped fs.FS, finds the first .nes file, and
// installs it. Ebiten exposes dropped files as a virtual filesystem for WASM
// compatibility, but on desktop the underlying fs.File is an *os.File whose
// Name() method returns the real absolute path. We grab that so save states
// can write next to the ROM (same as when it's opened via CLI or dialog).
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
	// Recover the real filesystem path if available (desktop/glfw case).
	realPath := picked
	if nf, ok := f.(interface{ Name() string }); ok {
		realPath = nf.Name()
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		g.setStatus("read dropped: "+err.Error(), 3*time.Second)
		return
	}
	cart, err := LoadCartBytes(data)
	if err != nil {
		g.setStatus("load failed: "+err.Error(), 4*time.Second)
		return
	}
	g.romPath = realPath
	g.installCart(cart, filepath.Base(realPath))
}

func (g *Game) setStatus(s string, d time.Duration) {
	g.statusMsg = s
	g.statusUntil = time.Now().Add(d)
}

func (g *Game) saveState() {
	nes := g.nes()
	if nes == nil {
		g.setStatus("no ROM loaded", 2*time.Second)
		return
	}
	path := g.statePath()
	if path == "" {
		g.setStatus("no ROM path; can't save state", 2*time.Second)
		return
	}
	if err := WriteStateFile(path, nes.Snapshot()); err != nil {
		g.setStatus("save state: "+err.Error(), 4*time.Second)
		return
	}
	g.setStatus(fmt.Sprintf("state saved to slot %d -> %s", g.stateSlot, filepath.Base(path)), 2*time.Second)
}

func (g *Game) loadState() {
	nes := g.nes()
	if nes == nil {
		g.setStatus("no ROM loaded", 2*time.Second)
		return
	}
	path := g.statePath()
	if path == "" {
		g.setStatus("no ROM path; can't load state", 2*time.Second)
		return
	}
	st, err := ReadStateFile(path)
	if err != nil {
		g.setStatus("load state: "+err.Error(), 4*time.Second)
		return
	}
	if err := nes.Restore(st); err != nil {
		g.setStatus("restore: "+err.Error(), 4*time.Second)
		return
	}
	g.setStatus(fmt.Sprintf("state loaded from slot %d <- %s", g.stateSlot, filepath.Base(path)), 2*time.Second)
}

// setStateSlot updates the active save-state slot and tells the user
// whether a save already exists there — so "F7 to slot 3, F4 load" is
// obvious even before the load happens. Also folds the slot into the
// window title when it's non-zero, so the feedback stays visible after
// the status message fades.
func (g *Game) setStateSlot(n int) {
	if n < 0 {
		n = numStateSlots - 1
	}
	if n >= numStateSlots {
		n = 0
	}
	g.stateSlot = n
	note := "(empty)"
	if path := g.statePath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			note = "(has save)"
		}
	}
	g.setStatus(fmt.Sprintf("slot %d %s", g.stateSlot, note), 2*time.Second)
	g.refreshWindowTitle()
}

// refreshWindowTitle rebuilds the OS window title from the current ROM
// name and the active save-state slot.
func (g *Game) refreshWindowTitle() {
	t := "balrog NES"
	if g.romName != "" {
		t += " - " + g.romName
	}
	if g.stateSlot != 0 {
		t += fmt.Sprintf(" [slot %d]", g.stateSlot)
	}
	ebiten.SetWindowTitle(t)
}

func (g *Game) nextStateSlot() { g.setStateSlot(g.stateSlot + 1) }
func (g *Game) prevStateSlot() { g.setStateSlot(g.stateSlot - 1) }

func (g *Game) Update() error {
	// Auto-load savestate once, on the first Update after a ROM is loaded.
	if g.autoLoadState && !g.autoLoadStateDone && g.nes() != nil {
		g.loadState()
		g.autoLoadStateDone = true
	}
	// Input dialog is modal: when open, it consumes all input this frame
	// so nothing leaks through to hotkeys, the menu, or the NES.
	if g.inputDialog != nil {
		sw, sh := ebiten.WindowSize()
		if g.inputDialog.update(sw, sh) {
			return nil
		}
	}
	// Menu bar update — if it swallowed the click, skip gamepad/keyboard
	// input for this frame so the user isn't accidentally feeding the NES
	// while they're navigating menus.
	if g.menuBar != nil {
		g.menuBar.update(g)
	}
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
	if inpututil.IsKeyJustPressed(ebiten.KeyF2) {
		g.saveState()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF4) {
		g.loadState()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF6) {
		g.prevStateSlot()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF7) {
		g.nextStateSlot()
	}
	// F11: capture next 4 frames as snap_<frame>_a/b/c/d.png — useful for
	// diagnosing per-frame flicker in gameplay.
	if inpututil.IsKeyJustPressed(ebiten.KeyF11) {
		if nes := g.nes(); nes != nil {
			start := nes.PPU.FrameCount()
			for i, suf := range []string{"a", "b", "c", "d"} {
				g.snaps = append(g.snaps, snapSpec{at: start + uint64(i), path: fmt.Sprintf("snap_%d_%s.png", start, suf)})
			}
			g.setStatus("snap burst armed", 2*time.Second)
		}
	}
	// F12: dump IRQ trace for next frame to irqlog.txt
	if inpututil.IsKeyJustPressed(ebiten.KeyF12) {
		if nes := g.nes(); nes != nil {
			f, err := os.Create("irqlog.txt")
			if err == nil {
				debugIrqLogFile = f
				debugIrqLog = true
				debugIrqFrame = nes.PPU.FrameCount() + 1
				g.setStatus(fmt.Sprintf("IRQ trace armed for frame %d", debugIrqFrame), 2*time.Second)
			}
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

	// Read live controller state from the user's bindings. Skip this
	// entirely while the Configure Input dialog is capturing — otherwise
	// the key the user just pressed to rebind would also be sent into the
	// game on the same frame.
	var b byte
	if g.inputDialog == nil || !g.inputDialog.isOpen() {
		b = g.inputCfg.readController()
	}
	for _, p := range g.presses {
		if g.frames >= p.from && g.frames < p.to {
			b |= p.button
		}
	}
	nes.Bus.Ctrl[0].Buttons = b

	before := nes.CPU.cycles
	nes.StepFrame()
	if g.cycPerFrame {
		fmt.Fprintf(os.Stderr, "frame %d: %d CPU cycles\n", nes.PPU.FrameCount(), nes.CPU.cycles-before)
	}
	_ = before
	_ = g.traceCpuFrame
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
	// --test-dialog: force the input dialog open and arm a full-window
	// snapshot that Draw will write on the next render.
	if g.testDialogAt > 0 && g.frames == g.testDialogAt {
		if g.inputDialog != nil {
			g.inputDialog.show()
		}
		g.pendingWinSnap = g.testDialogPath
	}
	if g.exitAt > 0 && g.frames >= g.exitAt {
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{0, 0, 0, 0xFF})
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	nesAreaY := menuBarH
	nesAreaH := sh - menuBarH
	nesAreaW := sw
	if g.nes() != nil {
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(float64(nesAreaW)/screenW, float64(nesAreaH)/screenH)
		op.GeoM.Translate(0, float64(nesAreaY))
		screen.DrawImage(g.screen, op)
	} else {
		screen.Fill(color.RGBA{0x10, 0x10, 0x18, 0xFF})
		ebitenutil.DebugPrintAt(screen, "balrog NES emulator", 16, nesAreaY+90)
		ebitenutil.DebugPrintAt(screen, "File > Open ROM… or drag a .nes here", 16, nesAreaY+110)
		ebitenutil.DebugPrintAt(screen, "(or press F1)", 16, nesAreaY+122)
	}
	if time.Now().Before(g.statusUntil) {
		ebitenutil.DebugPrintAt(screen, g.statusMsg, 6, sh-16)
	}
	if g.menuBar != nil {
		g.menuBar.draw(screen)
	}
	// Input dialog draws on top of everything.
	if g.inputDialog != nil {
		g.inputDialog.draw(screen)
	}
	// Full-window snapshot captures the final composited screen (NES
	// frame + menu bar + any overlay dialog). Used by --winsnap for
	// debugging UI layout; also lets us pixel-diff the dialog.
	if g.pendingWinSnap != "" {
		g.savePNG(screen, g.pendingWinSnap)
		g.pendingWinSnap = ""
	}
}

// savePNG writes an *ebiten.Image to disk as PNG.
func (g *Game) savePNG(src *ebiten.Image, path string) {
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src.ReadPixels(img.Pix)
	f, err := os.Create(path)
	if err != nil {
		log.Printf("winsnap: %v", err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
	log.Printf("window snapshot saved to %s (frame %d)", path, g.frames)
}

func (g *Game) Layout(ow, oh int) (int, int) {
	// Match the outside window exactly — 1 logical pixel per device pixel
	// so the menu bar's text renders at its natural size and the NES output
	// fills whatever room is left (DrawImage handles the scaling).
	return ow, oh
}

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
	// --trace <rom> <out.log>: nestest automation mode. Runs the ROM from
	// PC=$C000 headless and logs every instruction in nestest.log format.
	for i, a := range os.Args {
		if a == "--trace" && i+2 < len(os.Args) {
			runNestestTrace(os.Args[i+1], os.Args[i+2])
			return
		}
	}
	g := &Game{
		screen: ebiten.NewImage(screenW, screenH),
		pixels: make([]byte, screenW*screenH*4),
	}
	g.inputCfg = loadInputConfig()
	g.inputDialog = newInputDialog(g.inputCfg)
	g.menuBar = newMenuBar(g)
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
	g.audioPlayer = player
	// If a ROM was passed on the command line, loadROM already ran and
	// installCart will have started the player. Otherwise we leave it paused
	// until the user opens a ROM via F1 / drag-drop.
	if g.nes() != nil && !g.audioStarted {
		player.Play()
		g.audioStarted = true
	}

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
		case "--test-dialog":
			// --test-dialog <frame> <path>: at the given frame, force the
			// input config dialog open and capture the whole window (NES
			// + menu + dialog) to <path>. Purely a dev aid.
			if i+2 < len(os.Args) {
				fmt.Sscanf(os.Args[i+1], "%d", &g.testDialogAt)
				g.testDialogPath = os.Args[i+2]
				i += 2
			}
		case "--load-state":
			// Auto-load the ROM's savestate at startup. Pair with F2 once
			// to pre-save a useful point (e.g. inside a level), then every
			// subsequent launch resumes there.
			g.autoLoadState = true
		case "--cyc-per-frame":
			// Diagnostic: print CPU cycles consumed during each frame.
			g.cycPerFrame = true
		case "--trace-cpu":
			// --trace-cpu <frame>: enable full per-instruction trace for
			// exactly one frame.
			if i+1 < len(os.Args) {
				var n uint64
				fmt.Sscanf(os.Args[i+1], "%d", &n)
				g.traceCpuFrame = n
				i++
			}
		case "--debug-irq":
			if i+1 < len(os.Args) {
				var n uint64
				fmt.Sscanf(os.Args[i+1], "%d", &n)
				debugIrqLog = true
				debugIrqFrame = n
				f, err := os.Create("irqlog.txt")
				if err == nil {
					debugIrqLogFile = f
					mmc3LogFile = f
				}
				i++
			}
		}
	}

	ebiten.SetWindowSize(screenW*scale, screenH*scale+menuBarH)
	if g.romName == "" {
		ebiten.SetWindowTitle("balrog NES")
	}
	if err := ebiten.RunGame(g); err != nil {
		log.Fatal(err)
	}
}
