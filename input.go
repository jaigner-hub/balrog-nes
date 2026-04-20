package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// NES controller button bits (match the $4016 shift register order).
const (
	nesBitA      byte = 0x01
	nesBitB      byte = 0x02
	nesBitSelect byte = 0x04
	nesBitStart  byte = 0x08
	nesBitUp     byte = 0x10
	nesBitDown   byte = 0x20
	nesBitLeft   byte = 0x40
	nesBitRight  byte = 0x80
)

// buttonOrder is the display order used by the Configure Input dialog.
// Matches how an NES controller reads from top-down.
var buttonOrder = []struct {
	bit  byte
	name string
}{
	{nesBitUp, "Up"},
	{nesBitDown, "Down"},
	{nesBitLeft, "Left"},
	{nesBitRight, "Right"},
	{nesBitSelect, "Select"},
	{nesBitStart, "Start"},
	{nesBitA, "A"},
	{nesBitB, "B"},
}

// buttonBinding holds the user's bindings for a single NES button. Key and
// Pad are stored as ints (ebiten.Key / ebiten.StandardGamepadButton) so the
// config serializes cleanly as JSON; -1 means "unbound".
type buttonBinding struct {
	Key int `json:"key"`
	Pad int `json:"pad"`
}

const unbound = -1

// InputConfig maps each NES button bit to a keyboard key + gamepad button.
// Multiple NES buttons could bind to the same key, but the normal case is
// one-to-one and the dialog doesn't prevent duplicates (NES games don't
// care if both Up and W fire the Up bit, for instance).
//
// CRTFilter is stored here too so we only have one config file (balrog.cfg)
// — the name is a holdover from when this was input-only.
type InputConfig struct {
	Bindings  map[string]buttonBinding `json:"bindings"`
	CRTFilter bool                     `json:"crt_filter,omitempty"`
}

// bitKey converts a byte bit (0x01, 0x02, ...) into the JSON key used to
// persist the binding. We use the NES button name rather than the raw
// number so the config file is human-readable/editable.
func bitKey(bit byte) string {
	for _, b := range buttonOrder {
		if b.bit == bit {
			return b.name
		}
	}
	return ""
}

func defaultInputConfig() *InputConfig {
	return &InputConfig{
		Bindings: map[string]buttonBinding{
			"A":      {Key: int(ebiten.KeyX), Pad: int(ebiten.StandardGamepadButtonRightRight)},
			"B":      {Key: int(ebiten.KeyZ), Pad: int(ebiten.StandardGamepadButtonRightBottom)},
			"Select": {Key: int(ebiten.KeyShiftRight), Pad: int(ebiten.StandardGamepadButtonCenterLeft)},
			"Start":  {Key: int(ebiten.KeyEnter), Pad: int(ebiten.StandardGamepadButtonCenterRight)},
			"Up":     {Key: int(ebiten.KeyArrowUp), Pad: int(ebiten.StandardGamepadButtonLeftTop)},
			"Down":   {Key: int(ebiten.KeyArrowDown), Pad: int(ebiten.StandardGamepadButtonLeftBottom)},
			"Left":   {Key: int(ebiten.KeyArrowLeft), Pad: int(ebiten.StandardGamepadButtonLeftLeft)},
			"Right":  {Key: int(ebiten.KeyArrowRight), Pad: int(ebiten.StandardGamepadButtonLeftRight)},
		},
	}
}

// readController builds the NES controller byte ($4016 read format) from
// the currently-bound keyboard keys and the first connected standard
// gamepad, plus the left analog stick for D-pad.
func (c *InputConfig) readController() byte {
	var b byte
	for _, btn := range buttonOrder {
		bm, ok := c.Bindings[btn.name]
		if !ok {
			continue
		}
		if bm.Key != unbound && ebiten.IsKeyPressed(ebiten.Key(bm.Key)) {
			b |= btn.bit
		}
	}
	ids := ebiten.AppendGamepadIDs(nil)
	for _, id := range ids {
		if !ebiten.IsStandardGamepadLayoutAvailable(id) {
			continue
		}
		for _, btn := range buttonOrder {
			bm, ok := c.Bindings[btn.name]
			if !ok {
				continue
			}
			if bm.Pad != unbound && ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButton(bm.Pad)) {
				b |= btn.bit
			}
		}
		// Left analog stick always maps to the D-pad, on top of any
		// button bindings. Most controllers have both a stick and a
		// D-pad so this just means either works.
		const dead = 0.4
		ax := ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickHorizontal)
		ay := ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickVertical)
		if ay < -dead {
			b |= nesBitUp
		}
		if ay > dead {
			b |= nesBitDown
		}
		if ax < -dead {
			b |= nesBitLeft
		}
		if ax > dead {
			b |= nesBitRight
		}
	}
	return b
}

// inputConfigPath: next to the emulator executable, so the config travels
// with the portable binary. Falls back to cwd if we can't find the exe.
func inputConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "balrog.cfg"
	}
	return filepath.Join(filepath.Dir(exe), "balrog.cfg")
}

func loadInputConfig() *InputConfig {
	p := inputConfigPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return defaultInputConfig()
	}
	var c InputConfig
	if err := json.Unmarshal(data, &c); err != nil || len(c.Bindings) == 0 {
		return defaultInputConfig()
	}
	// Fill in any missing buttons from defaults so a partial config file
	// doesn't leave the user with unbound core buttons.
	def := defaultInputConfig()
	for _, btn := range buttonOrder {
		if _, ok := c.Bindings[btn.name]; !ok {
			c.Bindings[btn.name] = def.Bindings[btn.name]
		}
	}
	return &c
}

func (c *InputConfig) save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(inputConfigPath(), data, 0644)
}

// --- Key / button name helpers for display ---

func keyName(k int) string {
	if k == unbound {
		return "(none)"
	}
	s := ebiten.Key(k).String()
	// ebiten.Key.String() returns things like "ArrowUp", "ShiftRight".
	// These are already pretty readable — just strip the "Key" prefix if
	// it's there for older Ebiten versions.
	s = strings.TrimPrefix(s, "Key")
	return s
}

func padButtonName(p int) string {
	if p == unbound {
		return "(none)"
	}
	// Ebiten standard-gamepad names are positional (RightBottom etc.).
	// We translate to the more common face-button vocabulary so it's
	// obvious what the user is binding regardless of controller brand.
	switch ebiten.StandardGamepadButton(p) {
	case ebiten.StandardGamepadButtonRightBottom:
		return "South (Xbox A)"
	case ebiten.StandardGamepadButtonRightRight:
		return "East (Xbox B)"
	case ebiten.StandardGamepadButtonRightLeft:
		return "West (Xbox X)"
	case ebiten.StandardGamepadButtonRightTop:
		return "North (Xbox Y)"
	case ebiten.StandardGamepadButtonFrontTopLeft:
		return "L1 / LB"
	case ebiten.StandardGamepadButtonFrontTopRight:
		return "R1 / RB"
	case ebiten.StandardGamepadButtonFrontBottomLeft:
		return "L2 / LT"
	case ebiten.StandardGamepadButtonFrontBottomRight:
		return "R2 / RT"
	case ebiten.StandardGamepadButtonCenterLeft:
		return "Select / Back"
	case ebiten.StandardGamepadButtonCenterRight:
		return "Start / Menu"
	case ebiten.StandardGamepadButtonLeftStick:
		return "Left Stick"
	case ebiten.StandardGamepadButtonRightStick:
		return "Right Stick"
	case ebiten.StandardGamepadButtonLeftTop:
		return "D-pad Up"
	case ebiten.StandardGamepadButtonLeftBottom:
		return "D-pad Down"
	case ebiten.StandardGamepadButtonLeftLeft:
		return "D-pad Left"
	case ebiten.StandardGamepadButtonLeftRight:
		return "D-pad Right"
	}
	return fmt.Sprintf("Button%d", p)
}

// =============================================================================
// Input configuration dialog
// =============================================================================

// InputDialog is a modal overlay drawn on top of the NES area. It lets the
// user rebind each NES button to a keyboard key or gamepad button. Click a
// cell to enter "press any key" capture mode; Esc cancels; Delete unbinds.
type InputDialog struct {
	cfg  *InputConfig
	open bool
	// capture mode: the dialog is waiting for the user to press a key
	// (kind=0) or gamepad button (kind=1) to bind to a specific NES button.
	capture struct {
		active bool
		name   string // NES button name being rebound
		kind   int    // 0=key, 1=pad
	}
	// Cached layout from the most recent draw, used by update() to hit-test
	// without recomputing. Populated lazily in draw().
	layout dialogLayout
}

type dialogLayout struct {
	x, y, w, h int
	// per-row cell rects for the 8 buttons (each row has key + pad cells)
	rowY     [8]int // top y of each button row
	keyX     int    // left x of keyboard cell
	keyW     int
	padX     int
	padW     int
	rowH     int
	resetX   int // Reset button
	resetY   int
	resetW   int
	resetH   int
	closeX   int
	closeY   int
	closeW   int
	closeH   int
}

func newInputDialog(cfg *InputConfig) *InputDialog {
	return &InputDialog{cfg: cfg}
}

func (d *InputDialog) show()   { d.open = true; d.capture.active = false }
func (d *InputDialog) hide()   { d.open = false; d.capture.active = false }
func (d *InputDialog) isOpen() bool { return d.open }

// computeLayout sets d.layout based on the current window size so everything
// stays centered when the window is resized.
func (d *InputDialog) computeLayout(winW, winH int) {
	d.layout.w = 480
	d.layout.h = 320
	d.layout.x = (winW - d.layout.w) / 2
	d.layout.y = (winH - d.layout.h) / 2
	// Column positions (relative to dialog x)
	d.layout.keyX = d.layout.x + 110
	d.layout.keyW = 150
	d.layout.padX = d.layout.x + 270
	d.layout.padW = 196
	d.layout.rowH = 22
	rowTop := d.layout.y + 56 // below title + header
	for i := range d.layout.rowY {
		d.layout.rowY[i] = rowTop + i*d.layout.rowH
	}
	// Bottom buttons
	bottomY := d.layout.y + d.layout.h - 44
	d.layout.resetX = d.layout.x + 14
	d.layout.resetY = bottomY
	d.layout.resetW = 140
	d.layout.resetH = 28
	d.layout.closeW = 100
	d.layout.closeH = 28
	d.layout.closeX = d.layout.x + d.layout.w - d.layout.closeW - 14
	d.layout.closeY = bottomY
}

// update returns true if it consumed input (so the rest of the game should
// ignore mouse/keyboard for this frame).
func (d *InputDialog) update(winW, winH int) bool {
	if !d.open {
		return false
	}
	d.computeLayout(winW, winH)

	if d.capture.active {
		// Esc cancels capture
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
			d.capture.active = false
			return true
		}
		// Backspace / Delete unbinds
		if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) || inpututil.IsKeyJustPressed(ebiten.KeyDelete) {
			d.assign(unbound)
			return true
		}
		if d.capture.kind == 0 {
			keys := inpututil.AppendJustPressedKeys(nil)
			if len(keys) > 0 {
				d.assign(int(keys[0]))
			}
		} else {
			ids := ebiten.AppendGamepadIDs(nil)
			for _, id := range ids {
				if !ebiten.IsStandardGamepadLayoutAvailable(id) {
					continue
				}
				for i := 0; i <= int(ebiten.StandardGamepadButtonMax); i++ {
					if inpututil.IsStandardGamepadButtonJustPressed(id, ebiten.StandardGamepadButton(i)) {
						d.assign(i)
						break
					}
				}
				if !d.capture.active {
					break
				}
			}
		}
		return true // consume input while capturing
	}

	// Esc closes dialog
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		d.hide()
		return true
	}

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mx, my := ebiten.CursorPosition()
		// Reset defaults?
		if pointInRect(mx, my, d.layout.resetX, d.layout.resetY, d.layout.resetW, d.layout.resetH) {
			*d.cfg = *defaultInputConfig()
			_ = d.cfg.save()
			return true
		}
		// Close?
		if pointInRect(mx, my, d.layout.closeX, d.layout.closeY, d.layout.closeW, d.layout.closeH) {
			d.hide()
			return true
		}
		// One of the cells?
		for i, btn := range buttonOrder {
			ry := d.layout.rowY[i]
			if pointInRect(mx, my, d.layout.keyX, ry, d.layout.keyW, d.layout.rowH-2) {
				d.capture.active = true
				d.capture.name = btn.name
				d.capture.kind = 0
				return true
			}
			if pointInRect(mx, my, d.layout.padX, ry, d.layout.padW, d.layout.rowH-2) {
				d.capture.active = true
				d.capture.name = btn.name
				d.capture.kind = 1
				return true
			}
		}
		// Click inside dialog but on a non-interactive area is still
		// consumed — prevents click-through to the menu bar / game.
		if pointInRect(mx, my, d.layout.x, d.layout.y, d.layout.w, d.layout.h) {
			return true
		}
		// Click outside the dialog closes it (common modal idiom).
		d.hide()
		return true
	}
	return false
}

// assign commits a captured binding and saves the config to disk.
func (d *InputDialog) assign(code int) {
	bm, ok := d.cfg.Bindings[d.capture.name]
	if !ok {
		bm = buttonBinding{Key: unbound, Pad: unbound}
	}
	if d.capture.kind == 0 {
		bm.Key = code
	} else {
		bm.Pad = code
	}
	d.cfg.Bindings[d.capture.name] = bm
	_ = d.cfg.save()
	d.capture.active = false
}

func pointInRect(px, py, rx, ry, rw, rh int) bool {
	return px >= rx && px < rx+rw && py >= ry && py < ry+rh
}

func (d *InputDialog) draw(dst *ebiten.Image) {
	if !d.open {
		return
	}
	winW := dst.Bounds().Dx()
	winH := dst.Bounds().Dy()
	d.computeLayout(winW, winH)
	l := d.layout

	// Dim backdrop
	ebitenutil.DrawRect(dst, 0, 0, float64(winW), float64(winH), color.RGBA{0, 0, 0, 0xA0})

	// Dialog background + border
	ebitenutil.DrawRect(dst, float64(l.x), float64(l.y), float64(l.w), float64(l.h), color.RGBA{0x22, 0x24, 0x2C, 0xFF})
	drawBorder(dst, l.x, l.y, l.w, l.h, color.RGBA{0x60, 0x60, 0x70, 0xFF})

	// Title
	ebitenutil.DrawRect(dst, float64(l.x), float64(l.y), float64(l.w), 24, color.RGBA{0x30, 0x34, 0x40, 0xFF})
	ebitenutil.DebugPrintAt(dst, "Configure Input", l.x+12, l.y+(24-charH)/2)

	// Column headers
	headerY := l.y + 32
	ebitenutil.DebugPrintAt(dst, "Button", l.x+14, headerY)
	ebitenutil.DebugPrintAt(dst, "Keyboard", l.keyX+6, headerY)
	ebitenutil.DebugPrintAt(dst, "Gamepad", l.padX+6, headerY)

	// Rows
	mx, my := ebiten.CursorPosition()
	for i, btn := range buttonOrder {
		ry := l.rowY[i]
		bm := d.cfg.Bindings[btn.name]
		// Zebra stripe
		if i%2 == 0 {
			ebitenutil.DrawRect(dst, float64(l.x+1), float64(ry), float64(l.w-2), float64(l.rowH-2), color.RGBA{0x28, 0x2A, 0x34, 0xFF})
		}
		ebitenutil.DebugPrintAt(dst, btn.name, l.x+14, ry+(l.rowH-2-charH)/2)
		// Key cell
		d.drawCell(dst, l.keyX, ry, l.keyW, l.rowH-2, keyName(bm.Key),
			d.capture.active && d.capture.name == btn.name && d.capture.kind == 0,
			pointInRect(mx, my, l.keyX, ry, l.keyW, l.rowH-2))
		// Pad cell
		d.drawCell(dst, l.padX, ry, l.padW, l.rowH-2, padButtonName(bm.Pad),
			d.capture.active && d.capture.name == btn.name && d.capture.kind == 1,
			pointInRect(mx, my, l.padX, ry, l.padW, l.rowH-2))
	}

	// Help line
	helpY := l.y + l.h - 74
	if d.capture.active {
		msg := "Press a key..."
		if d.capture.kind == 1 {
			msg = "Press a gamepad button..."
		}
		ebitenutil.DebugPrintAt(dst, msg+"   (Esc=cancel, Del=unbind)", l.x+14, helpY)
	} else {
		ebitenutil.DebugPrintAt(dst, "Click a cell to rebind. Esc closes this dialog.", l.x+14, helpY)
	}

	// Bottom buttons
	d.drawButton(dst, l.resetX, l.resetY, l.resetW, l.resetH, "Reset Defaults",
		pointInRect(mx, my, l.resetX, l.resetY, l.resetW, l.resetH))
	d.drawButton(dst, l.closeX, l.closeY, l.closeW, l.closeH, "Close",
		pointInRect(mx, my, l.closeX, l.closeY, l.closeW, l.closeH))
}

func (d *InputDialog) drawCell(dst *ebiten.Image, x, y, w, h int, text string, active, hover bool) {
	bg := color.RGBA{0x1C, 0x1E, 0x26, 0xFF}
	if active {
		bg = color.RGBA{0x60, 0x40, 0x20, 0xFF}
	} else if hover {
		bg = color.RGBA{0x30, 0x38, 0x50, 0xFF}
	}
	ebitenutil.DrawRect(dst, float64(x), float64(y), float64(w), float64(h), bg)
	drawBorder(dst, x, y, w, h, color.RGBA{0x50, 0x50, 0x58, 0xFF})
	// Clip text if it would overflow the cell.
	maxChars := (w - 10) / charW
	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars]
	}
	ebitenutil.DebugPrintAt(dst, text, x+6, y+(h-charH)/2)
}

func (d *InputDialog) drawButton(dst *ebiten.Image, x, y, w, h int, text string, hover bool) {
	bg := color.RGBA{0x38, 0x40, 0x60, 0xFF}
	if hover {
		bg = color.RGBA{0x50, 0x5C, 0x80, 0xFF}
	}
	ebitenutil.DrawRect(dst, float64(x), float64(y), float64(w), float64(h), bg)
	drawBorder(dst, x, y, w, h, color.RGBA{0x70, 0x78, 0x90, 0xFF})
	// Center the label
	tx := x + (w-len(text)*charW)/2
	ty := y + (h-charH)/2
	ebitenutil.DebugPrintAt(dst, text, tx, ty)
}

func drawBorder(dst *ebiten.Image, x, y, w, h int, c color.RGBA) {
	ebitenutil.DrawRect(dst, float64(x), float64(y), float64(w), 1, c)
	ebitenutil.DrawRect(dst, float64(x), float64(y+h-1), float64(w), 1, c)
	ebitenutil.DrawRect(dst, float64(x), float64(y), 1, float64(h), c)
	ebitenutil.DrawRect(dst, float64(x+w-1), float64(y), 1, float64(h), c)
}
