package main

import (
	"image/color"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// Top-of-window menu bar. Drawn in *native* window pixels above the
// scaled NES output — so the menu font stays a normal size regardless of
// the emulator scale factor. Dropdowns open on click, close on click-away
// or after a selection.

const (
	menuBarH  = 20
	menuItemH = 16
	charW     = 6  // ebitenutil.DebugPrint glyph width
	charH     = 13 // ebitenutil.DebugPrint glyph height
)

type menuAction func()

type menuItem struct {
	label     string
	shortcut  string
	action    menuAction
	enabled   func() bool // nil = always enabled
	separator bool
}

type menuSection struct {
	label string
	items []menuItem
}

type menuBar struct {
	menus   []menuSection
	openIdx int // -1 when nothing is open
}

func newMenuBar(g *Game) *menuBar {
	hasNES := func() bool { return g.nes() != nil }
	return &menuBar{
		openIdx: -1,
		menus: []menuSection{
			{
				label: "File",
				items: []menuItem{
					{label: "Open ROM...", shortcut: "F1", action: g.openROMDialog},
					{separator: true},
					{label: "Save State", shortcut: "F2", action: g.saveState, enabled: hasNES},
					{label: "Load State", shortcut: "F4", action: g.loadState, enabled: hasNES},
					{separator: true},
					{label: "Exit", action: func() { os.Exit(0) }},
				},
			},
			{
				label: "Emulation",
				items: []menuItem{
					{label: "Reset", shortcut: "F5", action: func() {
						if nes := g.nes(); nes != nil {
							nes.CPU.Reset()
							g.setStatus("reset", time.Second)
						}
					}, enabled: hasNES},
					{separator: true},
					{label: "Configure Input...", action: func() {
						if g.inputDialog != nil {
							g.inputDialog.show()
						}
					}},
					{separator: true},
					{label: "Snap 4 frames", shortcut: "F11", action: func() {
						if nes := g.nes(); nes != nil {
							start := nes.PPU.FrameCount()
							for i, suf := range []string{"a", "b", "c", "d"} {
								g.snaps = append(g.snaps, snapSpec{at: start + uint64(i), path: snapFilename(start, suf)})
							}
							g.setStatus("snap burst armed", 2*time.Second)
						}
					}, enabled: hasNES},
				},
			},
			{
				label: "Help",
				items: []menuItem{
					{label: "About", action: func() {
						g.setStatus("balrog NES — written by Jeff Aigner (Go + Ebitengine)", 5*time.Second)
					}},
				},
			},
		},
	}
}

// labelWidth returns the on-screen width of a menu bar label including
// horizontal padding on both sides.
func (m *menuBar) labelWidth(i int) int { return len(m.menus[i].label)*charW + 16 }

func (m *menuBar) dropdownX(i int) int {
	x := 0
	for k := 0; k < i; k++ {
		x += m.labelWidth(k)
	}
	return x
}

func (m *menuBar) dropdownWidth(i int) int {
	max := 0
	for _, it := range m.menus[i].items {
		if it.separator {
			continue
		}
		w := len(it.label)*charW + len(it.shortcut)*charW + 32
		if w > max {
			max = w
		}
	}
	if max < 120 {
		max = 120
	}
	return max
}

// hit returns (menuIdx, -1) for a bar click, (menuIdx, itemIdx) for a
// dropdown item click, or (-1, -1) for anywhere else.
func (m *menuBar) hit(mx, my int) (int, int) {
	if my >= 0 && my < menuBarH {
		x := 0
		for i := range m.menus {
			w := m.labelWidth(i)
			if mx >= x && mx < x+w {
				return i, -1
			}
			x += w
		}
		return -1, -1
	}
	if m.openIdx < 0 {
		return -1, -1
	}
	dx := m.dropdownX(m.openIdx)
	dw := m.dropdownWidth(m.openIdx)
	dh := len(m.menus[m.openIdx].items) * menuItemH
	if mx >= dx && mx < dx+dw && my >= menuBarH && my < menuBarH+dh {
		return m.openIdx, (my - menuBarH) / menuItemH
	}
	return -1, -1
}

func (m *menuBar) update(g *Game) bool {
	consumed := false
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mx, my := ebiten.CursorPosition()
		mi, ii := m.hit(mx, my)
		switch {
		case mi >= 0 && ii < 0: // clicked a menu bar label
			if m.openIdx == mi {
				m.openIdx = -1
			} else {
				m.openIdx = mi
			}
			consumed = true
		case mi >= 0 && ii >= 0: // clicked a dropdown item
			it := m.menus[mi].items[ii]
			if !it.separator && it.action != nil {
				if it.enabled == nil || it.enabled() {
					it.action()
				}
			}
			m.openIdx = -1
			consumed = true
		default: // clicked away
			if m.openIdx >= 0 {
				m.openIdx = -1
				consumed = true
			}
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) && m.openIdx >= 0 {
		m.openIdx = -1
		consumed = true
	}
	return consumed
}

func (m *menuBar) draw(dst *ebiten.Image) {
	w := dst.Bounds().Dx()
	// Bar background
	ebitenutil.DrawRect(dst, 0, 0, float64(w), menuBarH, color.RGBA{0x1E, 0x1E, 0x24, 0xFF})
	ebitenutil.DrawRect(dst, 0, menuBarH-1, float64(w), 1, color.RGBA{0x40, 0x40, 0x48, 0xFF})

	// Labels
	x := 0
	for i, mn := range m.menus {
		lw := m.labelWidth(i)
		if i == m.openIdx {
			ebitenutil.DrawRect(dst, float64(x), 0, float64(lw), menuBarH, color.RGBA{0x38, 0x40, 0x60, 0xFF})
		}
		ebitenutil.DebugPrintAt(dst, mn.label, x+8, (menuBarH-charH)/2)
		x += lw
	}

	// Open dropdown
	if m.openIdx >= 0 {
		mn := m.menus[m.openIdx]
		dx := m.dropdownX(m.openIdx)
		dw := m.dropdownWidth(m.openIdx)
		dh := len(mn.items) * menuItemH

		// Drop shadow first (below + right)
		ebitenutil.DrawRect(dst, float64(dx+3), float64(menuBarH+3), float64(dw), float64(dh), color.RGBA{0, 0, 0, 0x60})
		// Background
		ebitenutil.DrawRect(dst, float64(dx), float64(menuBarH), float64(dw), float64(dh), color.RGBA{0x24, 0x24, 0x2C, 0xFF})
		// Border
		ebitenutil.DrawRect(dst, float64(dx), float64(menuBarH), float64(dw), 1, color.RGBA{0x50, 0x50, 0x58, 0xFF})
		ebitenutil.DrawRect(dst, float64(dx), float64(menuBarH+dh-1), float64(dw), 1, color.RGBA{0x50, 0x50, 0x58, 0xFF})
		ebitenutil.DrawRect(dst, float64(dx), float64(menuBarH), 1, float64(dh), color.RGBA{0x50, 0x50, 0x58, 0xFF})
		ebitenutil.DrawRect(dst, float64(dx+dw-1), float64(menuBarH), 1, float64(dh), color.RGBA{0x50, 0x50, 0x58, 0xFF})

		// Hover highlight
		mx, my := ebiten.CursorPosition()
		_, hoverItem := m.hit(mx, my)

		y := menuBarH
		for idx, it := range mn.items {
			if it.separator {
				ebitenutil.DrawRect(dst, float64(dx+6), float64(y+menuItemH/2), float64(dw-12), 1, color.RGBA{0x48, 0x48, 0x50, 0xFF})
			} else {
				if idx == hoverItem {
					ebitenutil.DrawRect(dst, float64(dx+1), float64(y), float64(dw-2), float64(menuItemH), color.RGBA{0x38, 0x40, 0x60, 0xFF})
				}
				ty := y + (menuItemH-charH)/2
				ebitenutil.DebugPrintAt(dst, it.label, dx+10, ty)
				if it.shortcut != "" {
					sw := len(it.shortcut) * charW
					ebitenutil.DebugPrintAt(dst, it.shortcut, dx+dw-sw-10, ty)
				}
			}
			y += menuItemH
		}
	}
}

// snapFilename keeps the F11 burst naming consistent between main.go and
// the menu action.
func snapFilename(start uint64, suffix string) string {
	return "snap_" + uitoa(start) + "_" + suffix + ".png"
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
