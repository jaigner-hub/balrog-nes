package main

// NES 2C02 PPU — cycle-accurate rendering.
//
// The PPU runs at 3x CPU rate: 341 cycles per scanline, 262 scanlines per
// frame. Scanlines: 0..239 visible, 240 post-render, 241..260 VBlank, 261
// pre-render. Cycle 0 is idle; cycles 1..256 output BG pixels and fetch BG
// tiles for the current scanline; cycles 257..320 fetch sprites for the
// next scanline; cycles 321..336 fetch the first two BG tiles for the next
// scanline.
//
// Each PPU Step() advances one PPU cycle. Fetches go through vramRead,
// which also drives the A12 line — the PPU A12 rising edges are what
// clocks the MMC3 scanline counter. With this running per-fetch, MMC3
// games like SMB3 and Kirby get IRQs on the right scanline every frame.

type PPU struct {
	cart *Cart

	// CPU-visible registers
	ctrl    byte // $2000
	mask    byte // $2001
	status  byte // $2002
	oamAddr byte // $2003
	busLat  byte // open bus / last write
	dataBuf byte // $2007 read buffer

	// Loopy scroll registers
	v uint16 // current VRAM address (15 bits)
	t uint16 // temporary VRAM address (15 bits)
	x byte   // fine X (3 bits)
	w byte   // write toggle (1 bit)

	// Memory
	nt      [2][1024]byte
	palette [32]byte
	oam     [256]byte

	// Timing
	scanline int
	cycle    int
	frame    uint64
	odd      bool

	// Output framebuffer
	Frame [256 * 240]uint32

	// Interrupt line to CPU
	NMIPending bool

	// --- BG rendering state ---
	// Latches filled during tile fetches
	bgNTByte      byte
	bgATByte      byte
	bgPatternLo   byte
	bgPatternHi   byte
	// 16-bit shift registers. Each cycle shifts left one bit. Every 8 cycles
	// a freshly fetched tile is loaded into the LOW byte (and the current tile
	// naturally moves to the HIGH byte via shifts). For attributes, all 8
	// pixels of a tile share the same palette bits, so on load we fill the
	// low byte with 0x00 or 0xFF depending on the attribute bit — otherwise
	// the left and right halves of a tile end up on different palettes.
	bgShiftPatternLo uint16
	bgShiftPatternHi uint16
	bgShiftAttribLo  uint16
	bgShiftAttribHi  uint16

	// --- Sprite rendering state ---
	secondaryOAM [8 * 4]byte
	numSprites   int
	spriteHasS0  bool // current scanline's secondaryOAM includes sprite 0
	nextHasS0    bool // set during eval for the next scanline
	// Per-output-sprite latches (for current scanline)
	sprPatternLo [8]byte
	sprPatternHi [8]byte
	sprAttr      [8]byte
	sprX         [8]byte
	sprIsS0      [8]bool

}

const (
	ctrlNmi       = 0x80
	ctrlSprSize   = 0x20
	ctrlBgPtable  = 0x10
	ctrlSprPtable = 0x08
	ctrlVramInc   = 0x04
	ctrlNtSelect  = 0x03

	maskRender  = 0x18
	maskShowBg  = 0x08
	maskShowSpr = 0x10
	maskLeftBg  = 0x02
	maskLeftSpr = 0x04

	statVBlank = 0x80
	statSpr0   = 0x40
	statSprOv  = 0x20
)

// NES palette (2C02 RGB approximation)
var nesPalette = [64]uint32{
	0x626262FF, 0x001FB2FF, 0x2404C8FF, 0x5200B2FF, 0x730076FF, 0x800024FF, 0x730B00FF, 0x522800FF,
	0x244400FF, 0x005700FF, 0x005C00FF, 0x005324FF, 0x003C76FF, 0x000000FF, 0x000000FF, 0x000000FF,
	0xABABABFF, 0x0D57FFFF, 0x4B30FFFF, 0x8A13FFFF, 0xBC08D6FF, 0xD21269FF, 0xC72E00FF, 0x9D5400FF,
	0x607B00FF, 0x209800FF, 0x00A300FF, 0x009942FF, 0x007DB4FF, 0x000000FF, 0x000000FF, 0x000000FF,
	0xFFFFFFFF, 0x53AEFFFF, 0x9085FFFF, 0xD365FFFF, 0xFF57FFFF, 0xFF5DCFFF, 0xFF7757FF, 0xFA9E00FF,
	0xBDC700FF, 0x7AE700FF, 0x43F611FF, 0x26EF7EFF, 0x2CD5F6FF, 0x4E4E4EFF, 0x000000FF, 0x000000FF,
	0xFFFFFFFF, 0xB6E1FFFF, 0xCED1FFFF, 0xE9C3FFFF, 0xFFBCFFFF, 0xFFBDF4FF, 0xFFC6C3FF, 0xFFD59AFF,
	0xE9E681FF, 0xCEF481FF, 0xB6FB9AFF, 0xA9FAC3FF, 0xA9F0F4FF, 0xB8B8B8FF, 0x000000FF, 0x000000FF,
}

func NewPPU(c *Cart) *PPU { return &PPU{cart: c, scanline: 261} }

// Nametable mirroring
func (p *PPU) mirrorNT(addr uint16) (bank, idx int) {
	addr = (addr - 0x2000) & 0x0FFF
	table := addr / 0x0400
	offs := int(addr & 0x03FF)
	switch p.cart.MirrorMode() {
	case MirrorHorizontal:
		if table < 2 {
			return 0, offs
		}
		return 1, offs
	case MirrorVertical:
		return int(table & 1), offs
	case MirrorSingle0:
		return 0, offs
	case MirrorSingle1:
		return 1, offs
	}
	return int(table & 1), offs
}

func (p *PPU) vramRead(addr uint16) byte {
	addr &= 0x3FFF
	switch {
	case addr < 0x2000:
		return p.cart.ReadCHR(addr)
	case addr < 0x3F00:
		b, i := p.mirrorNT(addr)
		return p.nt[b][i]
	default:
		i := addr & 0x1F
		if i == 0x10 || i == 0x14 || i == 0x18 || i == 0x1C {
			i -= 0x10
		}
		return p.palette[i] & 0x3F
	}
}

func (p *PPU) vramWrite(addr uint16, v byte) {
	addr &= 0x3FFF
	switch {
	case addr < 0x2000:
		p.cart.WriteCHR(addr, v)
	case addr < 0x3F00:
		b, i := p.mirrorNT(addr)
		p.nt[b][i] = v
	default:
		i := addr & 0x1F
		if i == 0x10 || i == 0x14 || i == 0x18 || i == 0x1C {
			i -= 0x10
		}
		p.palette[i] = v & 0x3F
	}
}

// CPU <-> PPU register interface
func (p *PPU) CPURead(addr uint16) byte {
	reg := addr & 7
	switch reg {
	case 2: // PPUSTATUS
		r := (p.status & 0xE0) | (p.busLat & 0x1F)
		p.status &^= statVBlank
		p.w = 0
		p.busLat = r
		return r
	case 4: // OAMDATA
		r := p.oam[p.oamAddr]
		p.busLat = r
		return r
	case 7: // PPUDATA
		a := p.v & 0x3FFF
		var r byte
		if a < 0x3F00 {
			r = p.dataBuf
			p.dataBuf = p.vramRead(a)
		} else {
			r = p.vramRead(a)
			p.dataBuf = p.vramRead(a - 0x1000)
		}
		if p.ctrl&ctrlVramInc != 0 {
			p.v += 32
		} else {
			p.v++
		}
		p.busLat = r
		return r
	}
	return p.busLat
}

func (p *PPU) CPUWrite(addr uint16, val byte) {
	p.busLat = val
	reg := addr & 7
	switch reg {
	case 0:
		oldNMI := p.ctrl&ctrlNmi != 0
		p.ctrl = val
		p.t = (p.t & 0xF3FF) | (uint16(val&0x03) << 10)
		if !oldNMI && p.ctrl&ctrlNmi != 0 && p.status&statVBlank != 0 {
			p.NMIPending = true
		}
	case 1:
		p.mask = val
	case 3:
		p.oamAddr = val
	case 4:
		p.oam[p.oamAddr] = val
		p.oamAddr++
	case 5:
		if p.w == 0 {
			p.t = (p.t & 0xFFE0) | uint16(val>>3)
			p.x = val & 0x07
			p.w = 1
		} else {
			p.t = (p.t & 0x8FFF) | (uint16(val&0x07) << 12)
			p.t = (p.t & 0xFC1F) | (uint16(val&0xF8) << 2)
			p.w = 0
		}
	case 6:
		if p.w == 0 {
			p.t = (p.t & 0x00FF) | (uint16(val&0x3F) << 8)
			p.w = 1
		} else {
			p.t = (p.t & 0xFF00) | uint16(val)
			p.v = p.t
			p.w = 0
		}
	case 7:
		p.vramWrite(p.v&0x3FFF, val)
		if p.ctrl&ctrlVramInc != 0 {
			p.v += 32
		} else {
			p.v++
		}
	}
}

// Scroll register helpers
func (p *PPU) incCoarseX() {
	if p.v&0x001F == 31 {
		p.v &^= 0x001F
		p.v ^= 0x0400
	} else {
		p.v++
	}
}

func (p *PPU) incY() {
	if p.v&0x7000 != 0x7000 {
		p.v += 0x1000
	} else {
		p.v &^= 0x7000
		y := (p.v & 0x03E0) >> 5
		switch y {
		case 29:
			y = 0
			p.v ^= 0x0800
		case 31:
			y = 0
		default:
			y++
		}
		p.v = (p.v & 0xFC1F) | (y << 5)
	}
}

func (p *PPU) copyX() { p.v = (p.v & 0xFBE0) | (p.t & 0x041F) }
func (p *PPU) copyY() { p.v = (p.v & 0x841F) | (p.t & 0x7BE0) }

// --- BG fetch pipeline ---
//
// On real hardware, each 8-cycle slot fetches: NT byte (cycle 1), AT byte
// (cycle 3), pattern lo (cycle 5), pattern hi (cycle 7). At cycle 0 of the
// next slot, the 8-bit pattern latches become the high byte of the 16-bit
// shift registers, and the attribute latches are refilled from the 2-bit
// palette bits for that tile.

func (p *PPU) fetchNT() {
	addr := 0x2000 | (p.v & 0x0FFF)
	p.bgNTByte = p.vramRead(addr)
}

func (p *PPU) fetchAT() {
	addr := 0x23C0 | (p.v & 0x0C00) | ((p.v >> 4) & 0x38) | ((p.v >> 2) & 0x07)
	attr := p.vramRead(addr)
	shift := ((p.v >> 4) & 4) | (p.v & 2)
	p.bgATByte = (attr >> shift) & 0x03
}

func (p *PPU) fetchBGPatternLo() {
	fineY := (p.v >> 12) & 7
	ptBase := uint16(0)
	if p.ctrl&ctrlBgPtable != 0 {
		ptBase = 0x1000
	}
	p.bgPatternLo = p.vramRead(ptBase + uint16(p.bgNTByte)*16 + fineY)
}

func (p *PPU) fetchBGPatternHi() {
	fineY := (p.v >> 12) & 7
	ptBase := uint16(0)
	if p.ctrl&ctrlBgPtable != 0 {
		ptBase = 0x1000
	}
	p.bgPatternHi = p.vramRead(ptBase + uint16(p.bgNTByte)*16 + fineY + 8)
}

func (p *PPU) loadBGShifters() {
	p.bgShiftPatternLo = (p.bgShiftPatternLo & 0xFF00) | uint16(p.bgPatternLo)
	p.bgShiftPatternHi = (p.bgShiftPatternHi & 0xFF00) | uint16(p.bgPatternHi)
	// Fill the low byte of each attribute shift register with the tile's
	// palette bit repeated 8 times — all 8 pixels share it.
	var loFill, hiFill uint16
	if p.bgATByte&1 != 0 {
		loFill = 0x00FF
	}
	if p.bgATByte&2 != 0 {
		hiFill = 0x00FF
	}
	p.bgShiftAttribLo = (p.bgShiftAttribLo & 0xFF00) | loFill
	p.bgShiftAttribHi = (p.bgShiftAttribHi & 0xFF00) | hiFill
}

func (p *PPU) shiftBG() {
	if p.mask&maskShowBg == 0 {
		return
	}
	p.bgShiftPatternLo <<= 1
	p.bgShiftPatternHi <<= 1
	p.bgShiftAttribLo <<= 1
	p.bgShiftAttribHi <<= 1
}

// --- Sprite evaluation & fetch ---

// evaluateSpritesForNextLine picks up to 8 sprites whose Y range covers the
// next scanline, copies them to secondary OAM in OAM order, and notes
// whether sprite 0 is among them. We do it at cycle 257 in one shot —
// real hardware spreads this over cycles 65..256 of the current scanline,
// but the end result (which sprites end up in secondary OAM) is the same.
//
// On the pre-render scanline (261), "next line" is scanline 0 — that's what
// hardware prepares for during pre-render's 257..320 window.
func (p *PPU) evaluateSpritesForNextLine() {
	var nextLine int
	if p.scanline == 261 {
		nextLine = 0
	} else {
		nextLine = p.scanline + 1
	}
	if nextLine >= 240 {
		p.numSprites = 0
		p.nextHasS0 = false
		return
	}
	spriteH := 8
	if p.ctrl&ctrlSprSize != 0 {
		spriteH = 16
	}
	count := 0
	hasS0 := false
	// Track which output slot each OAM sprite went into. Sprite 0 isn't
	// always at slot 0 — if earlier OAM entries aren't on this scanline,
	// sprite 0 could land at any slot (or not at all).
	var slotIsS0 [8]bool
	for i := 0; i < 64 && count < 8; i++ {
		sy := int(p.oam[i*4]) + 1
		if nextLine < sy || nextLine >= sy+spriteH {
			continue
		}
		p.secondaryOAM[count*4+0] = p.oam[i*4+0]
		p.secondaryOAM[count*4+1] = p.oam[i*4+1]
		p.secondaryOAM[count*4+2] = p.oam[i*4+2]
		p.secondaryOAM[count*4+3] = p.oam[i*4+3]
		if i == 0 {
			hasS0 = true
			slotIsS0[count] = true
		}
		count++
	}
	p.sprIsS0 = slotIsS0
	// Approximate sprite-overflow: set the flag if there are more than 8
	// sprites on the next line. Real hardware has a specific buggy scan that
	// can also set this falsely; we're going with the simpler version.
	if count == 8 {
		for i := 8; i < 64; i++ {
			sy := int(p.oam[i*4]) + 1
			if nextLine >= sy && nextLine < sy+spriteH {
				p.status |= statSprOv
				break
			}
		}
	}
	p.numSprites = count
	p.nextHasS0 = hasS0
}

// fetchSpriteTile loads the pattern bytes + attribute + X for one sprite
// in secondaryOAM. Called once per sprite slot during cycles 257-320.
// Crucially, these fetches drive A12 — their addresses depend on each
// sprite's pattern table selection, which is what MMC3 scanline counter
// is counting.
func (p *PPU) fetchSpriteTile(slot int) {
	if slot >= p.numSprites {
		// Fetch dummy tile to maintain A12 timing (mirrors real hardware)
		dummy := uint16(0x1FF0)
		if p.ctrl&ctrlSprPtable == 0 && p.ctrl&ctrlSprSize == 0 {
			dummy = 0x0FF0
		}
		p.vramRead(dummy)
		p.vramRead(dummy + 8)
		return
	}
	sy := int(p.secondaryOAM[slot*4+0]) + 1
	tile := p.secondaryOAM[slot*4+1]
	attr := p.secondaryOAM[slot*4+2]
	xPos := p.secondaryOAM[slot*4+3]

	spriteH := 8
	if p.ctrl&ctrlSprSize != 0 {
		spriteH = 16
	}
	// Row within the sprite for the NEXT scanline — same "next line" logic
	// as evaluateSpritesForNextLine uses.
	nextLine := p.scanline + 1
	if p.scanline == 261 {
		nextLine = 0
	}
	row := nextLine - sy
	flipV := attr&0x80 != 0
	if flipV {
		row = spriteH - 1 - row
	}

	var addr uint16
	if spriteH == 16 {
		base := uint16(tile&1) * 0x1000
		tileIdx := uint16(tile & 0xFE)
		if row >= 8 {
			tileIdx++
			row -= 8
		}
		addr = base + tileIdx*16 + uint16(row)
	} else {
		base := uint16(0)
		if p.ctrl&ctrlSprPtable != 0 {
			base = 0x1000
		}
		addr = base + uint16(tile)*16 + uint16(row)
	}
	lo := p.vramRead(addr)
	hi := p.vramRead(addr + 8)
	// Horizontal flip: reverse bits
	if attr&0x40 != 0 {
		lo = reverseByte(lo)
		hi = reverseByte(hi)
	}
	p.sprPatternLo[slot] = lo
	p.sprPatternHi[slot] = hi
	p.sprAttr[slot] = attr
	p.sprX[slot] = xPos
}

func reverseByte(b byte) byte {
	b = (b&0xF0)>>4 | (b&0x0F)<<4
	b = (b&0xCC)>>2 | (b&0x33)<<2
	b = (b&0xAA)>>1 | (b&0x55)<<1
	return b
}

// --- Pixel output ---

// outputPixel computes the final pixel at (cycle-1, scanline), combining BG
// and sprite sources, and writes it to the frame buffer. Handles sprite-0
// hit and sprite priority.
func (p *PPU) outputPixel() {
	x := p.cycle - 1
	y := p.scanline

	// Background pixel
	var bgC, bgPal byte
	if p.mask&maskShowBg != 0 && (x >= 8 || p.mask&maskLeftBg != 0) {
		bit := uint16(0x8000) >> p.x
		loBit := byte(0)
		if p.bgShiftPatternLo&bit != 0 {
			loBit = 1
		}
		hiBit := byte(0)
		if p.bgShiftPatternHi&bit != 0 {
			hiBit = 1
		}
		bgC = loBit | (hiBit << 1)
		if bgC != 0 {
			paLo := byte(0)
			if p.bgShiftAttribLo&bit != 0 {
				paLo = 1
			}
			paHi := byte(0)
			if p.bgShiftAttribHi&bit != 0 {
				paHi = 1
			}
			bgPal = paLo | (paHi << 1)
		}
	}

	// Sprite pixel (first non-transparent sprite in order)
	var sprC, sprPal, sprAttr byte
	sprFound := false
	sprSlot := -1
	if p.mask&maskShowSpr != 0 && (x >= 8 || p.mask&maskLeftSpr != 0) {
		for slot := 0; slot < p.numSprites; slot++ {
			sx := int(p.sprX[slot])
			dx := x - sx
			if dx < 0 || dx > 7 {
				continue
			}
			lo := (p.sprPatternLo[slot] >> (7 - dx)) & 1
			hi := (p.sprPatternHi[slot] >> (7 - dx)) & 1
			c := lo | (hi << 1)
			if c == 0 {
				continue
			}
			sprC = c
			sprPal = p.sprAttr[slot] & 0x03
			sprAttr = p.sprAttr[slot]
			sprSlot = slot
			sprFound = true
			break
		}
	}

	// Combine — sprite-0 hit fires when both BG and sprite 0 have opaque
	// pixels at the same position (with the usual exclusions).
	var pal byte
	if !sprFound && bgC == 0 {
		// Universal background color
		pal = p.palette[0] & 0x3F
	} else if !sprFound {
		pal = p.palette[bgPal*4+bgC] & 0x3F
	} else if bgC == 0 {
		pal = p.palette[0x10+sprPal*4+sprC] & 0x3F
	} else {
		if sprSlot >= 0 && p.sprIsS0[sprSlot] && p.spriteHasS0 && x != 255 {
			p.status |= statSpr0
		}
		if sprAttr&0x20 != 0 {
			pal = p.palette[bgPal*4+bgC] & 0x3F
		} else {
			pal = p.palette[0x10+sprPal*4+sprC] & 0x3F
		}
	}

	p.Frame[y*256+x] = nesPalette[pal]
}

// Step advances the PPU one cycle.
func (p *PPU) Step() {
	visible := p.scanline < 240
	preRender := p.scanline == 261
	renderLine := visible || preRender
	renderingOn := p.mask&(maskShowBg|maskShowSpr) != 0

	// VBlank start
	if p.scanline == 241 && p.cycle == 1 {
		p.status |= statVBlank
		if p.ctrl&ctrlNmi != 0 {
			p.NMIPending = true
		}
	}
	// Pre-render cycle 1: clear status flags
	if preRender && p.cycle == 1 {
		p.status &^= statVBlank | statSpr0 | statSprOv
	}

	if renderLine && renderingOn {
		inFetch := (p.cycle >= 1 && p.cycle <= 256) || (p.cycle >= 321 && p.cycle <= 336)
		// Output FIRST (reads current register state for this pixel), THEN
		// shift — matches real NES order. If we shifted before output, pixel
		// 0 would come from a shifted register and always be wrong.
		if visible && p.cycle >= 1 && p.cycle <= 256 {
			p.outputPixel()
		}
		if inFetch {
			p.shiftBG()
			// Fetch pipeline per 8-cycle slot:
			//   cycle % 8 == 1: NT byte
			//   cycle % 8 == 3: AT byte
			//   cycle % 8 == 5: pattern lo
			//   cycle % 8 == 7: pattern hi
			//   cycle % 8 == 0: load fetched latches into shift registers
			//                    and incCoarseX (or incY at cycle 256).
			switch p.cycle % 8 {
			case 1:
				p.fetchNT()
			case 3:
				p.fetchAT()
			case 5:
				p.fetchBGPatternLo()
			case 7:
				p.fetchBGPatternHi()
			case 0:
				p.loadBGShifters()
				if p.cycle == 256 {
					p.incY()
				} else {
					p.incCoarseX()
				}
			}
		}
		// Copy horizontal bits at cycle 257
		if p.cycle == 257 {
			p.copyX()
			// Reset oamAddr during sprite eval per hardware quirk
			p.oamAddr = 0
			// Finish sprite eval for next scanline (we do it in bulk here)
			if visible {
				p.evaluateSpritesForNextLine()
			} else {
				// Pre-render: prepare sprites for scanline 0
				p.evaluateSpritesForNextLine()
			}
		}
		// Copy vertical bits during pre-render 280..304
		if preRender && p.cycle >= 280 && p.cycle <= 304 {
			p.copyY()
		}
		// Sprite tile fetches during 257..320.
		if p.cycle >= 257 && p.cycle <= 320 {
			slot := (p.cycle - 257) / 8
			cycleInSlot := (p.cycle - 257) & 7
			if cycleInSlot == 5 {
				p.fetchSpriteTile(slot)
			}
		}
		// MMC3 scanline counter clock: fire once per visible / pre-render
		// scanline. Using cycle 280 (matching fogleman's working Go impl;
		// slightly later than the "correct" 260 but consistently works for
		// SMB3 etc. because it gives the IRQ handler a more predictable
		// window before the next scanline starts).
		if p.cycle == 280 && (visible || preRender) {
			if sc, ok := p.cart.mapper.(scanlineCounter); ok {
				sc.ClockScanline()
			}
		}
		// Latch "has sprite 0" for the scanline we're about to start
		// rendering (happens as sprite 0's sprite-0 flag gets carried
		// through the fetch).
		if p.cycle == 320 {
			p.spriteHasS0 = p.nextHasS0
		}
	}

	// Rendering-disabled path: fill with universal BG color.
	if visible && !renderingOn && p.cycle >= 1 && p.cycle <= 256 {
		p.Frame[p.scanline*256+p.cycle-1] = nesPalette[p.palette[0]&0x3F]
	}

	// Advance
	p.cycle++
	if p.cycle == 341 {
		p.cycle = 0
		p.scanline++
		if p.scanline == 262 {
			p.scanline = 0
			p.frame++
			p.odd = !p.odd
		}
	}
}

// FrameDone returns true once per frame, right after the last scanline
// advances back to scanline 0.
func (p *PPU) FrameDone() bool {
	return p.scanline == 0 && p.cycle == 0
}

// OAM DMA — called by the bus when CPU writes $4014
func (p *PPU) OAMDMA(page []byte) {
	for i := 0; i < 256; i++ {
		p.oam[(uint16(p.oamAddr)+uint16(i))&0xFF] = page[i]
	}
}
