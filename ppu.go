package main

// NES 2C02 PPU. Scanline-accurate enough to run Mapper-0 games.
// Timing: 262 scanlines/frame, 341 cycles/scanline. Scanlines:
//   -1 (pre-render), 0..239 (visible), 240 (post), 241..260 (VBlank).

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
	nt      [2][1024]byte // two 1KB nametables
	palette [32]byte
	oam     [256]byte

	// Timing
	scanline int
	cycle    int
	frame    uint64
	odd      bool

	// Output framebuffer: 256x240 palette indices -> RGBA
	Frame [256 * 240]uint32

	// Interrupt line to CPU
	NMIPending bool

	// For sprite0 hit check
	spr0Line bool
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

// NES palette — 2C02 RGB approximation (common Nintendulator/Kevtris table)
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

// Mirroring: convert nametable VRAM address to internal index.
func (p *PPU) mirrorNT(addr uint16) (bank, idx int) {
	addr = (addr - 0x2000) & 0x0FFF
	table := addr / 0x0400
	offs := int(addr & 0x03FF)
	switch p.cart.MirrorMode() {
	case MirrorHorizontal:
		// NT0/NT1 share, NT2/NT3 share -> map to bank 0 and 1
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

// CPU reads $2000-$2007 (mirrored 0x2000-0x3FFF).
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
	case 0: // PPUCTRL
		oldNMI := p.ctrl&ctrlNmi != 0
		p.ctrl = val
		// t: ....BA.. ........ = d: ......BA
		p.t = (p.t & 0xF3FF) | (uint16(val&0x03) << 10)
		// Toggling NMI on during VBlank triggers NMI
		if !oldNMI && p.ctrl&ctrlNmi != 0 && p.status&statVBlank != 0 {
			p.NMIPending = true
		}
	case 1: // PPUMASK
		p.mask = val
	case 3: // OAMADDR
		p.oamAddr = val
	case 4: // OAMDATA
		p.oam[p.oamAddr] = val
		p.oamAddr++
	case 5: // PPUSCROLL
		if p.w == 0 {
			// t: ........ ...HGFED = d: HGFED...
			// x: CBA              = d: .....CBA
			p.t = (p.t & 0xFFE0) | uint16(val>>3)
			p.x = val & 0x07
			p.w = 1
		} else {
			// t: .CBA..HG FED..... = d: HGFEDCBA
			p.t = (p.t & 0x8FFF) | (uint16(val&0x07) << 12)
			p.t = (p.t & 0xFC1F) | (uint16(val&0xF8) << 2)
			p.w = 0
		}
	case 6: // PPUADDR
		if p.w == 0 {
			p.t = (p.t & 0x00FF) | (uint16(val&0x3F) << 8)
			p.w = 1
		} else {
			p.t = (p.t & 0xFF00) | uint16(val)
			p.v = p.t
			p.w = 0
		}
	case 7: // PPUDATA
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
		p.v ^= 0x0400 // switch horizontal NT
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
			p.v ^= 0x0800 // switch vertical NT
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

// Render a single scanline's background pixels (tile-granular fetching).
// Produces indices 0..3 per pixel (palette-within-palette) and palette set 0..3.
// To keep the implementation small we operate per-pixel using current v/x.
func (p *PPU) renderScanline(y int) {
	row := &p.Frame
	universal := p.palette[0] & 0x3F
	bg := nesPalette[universal]
	// Fill background universal color first
	for x := 0; x < 256; x++ {
		row[y*256+x] = bg
	}
	if p.mask&maskRender == 0 {
		return
	}

	// Render background
	type bgPix struct {
		color byte // palette index (0..63) or 0 if transparent
		pal   byte // lower 2 bits of pattern (for sprite priority check)
	}
	var bgRow [256]bgPix

	if p.mask&maskShowBg != 0 {
		for px := 0; px < 256; px++ {
			if px < 8 && p.mask&maskLeftBg == 0 {
				continue
			}
			fineX := (int(p.x) + px) & 7
			if px > 0 && fineX == 0 {
				p.incCoarseX()
			}
			// Fetch tile info
			ntAddr := 0x2000 | (p.v & 0x0FFF)
			tile := p.vramRead(ntAddr)
			atAddr := 0x23C0 | (p.v & 0x0C00) | ((p.v >> 4) & 0x38) | ((p.v >> 2) & 0x07)
			attr := p.vramRead(atAddr)
			shift := ((p.v >> 4) & 4) | (p.v & 2)
			palSel := (attr >> shift) & 0x03
			fineY := (p.v >> 12) & 7
			ptBase := uint16(0)
			if p.ctrl&ctrlBgPtable != 0 {
				ptBase = 0x1000
			}
			lo := p.vramRead(ptBase + uint16(tile)*16 + fineY)
			hi := p.vramRead(ptBase + uint16(tile)*16 + fineY + 8)
			bit := byte(7 - fineX)
			c := ((lo >> bit) & 1) | (((hi >> bit) & 1) << 1)
			if c != 0 {
				palIdx := p.palette[palSel*4+c] & 0x3F
				bgRow[px] = bgPix{color: palIdx | 0x80, pal: c}
			}
		}
		// end of scanline: incY + copyX
		p.incY()
		p.copyX()
	}

	// Sprites
	if p.mask&maskShowSpr != 0 {
		spriteH := 8
		if p.ctrl&ctrlSprSize != 0 {
			spriteH = 16
		}
		// Collect up to 8 sprites on this scanline
		type sp struct{ idx, x, y, tile, attr int }
		var sprs []sp
		for i := 0; i < 64; i++ {
			sy := int(p.oam[i*4])
			if y < sy || y >= sy+spriteH {
				continue
			}
			sprs = append(sprs, sp{
				idx: i, y: sy,
				tile: int(p.oam[i*4+1]),
				attr: int(p.oam[i*4+2]),
				x:    int(p.oam[i*4+3]),
			})
			if len(sprs) == 8 {
				p.status |= statSprOv
				break
			}
		}
		// Later sprites (higher idx) drawn first so earlier overwrite
		for i := len(sprs) - 1; i >= 0; i-- {
			s := sprs[i]
			flipH := s.attr&0x40 != 0
			flipV := s.attr&0x80 != 0
			palSel := byte(s.attr & 0x03)
			bgPri := s.attr&0x20 != 0
			row := y - s.y
			if flipV {
				row = spriteH - 1 - row
			}
			var patBase uint16
			var tileIdx int
			if spriteH == 16 {
				patBase = uint16(s.tile&1) * 0x1000
				tileIdx = s.tile & 0xFE
				if row >= 8 {
					tileIdx++
					row -= 8
				}
			} else {
				if p.ctrl&ctrlSprPtable != 0 {
					patBase = 0x1000
				}
				tileIdx = s.tile
			}
			lo := p.vramRead(patBase + uint16(tileIdx)*16 + uint16(row))
			hi := p.vramRead(patBase + uint16(tileIdx)*16 + uint16(row) + 8)
			for px := 0; px < 8; px++ {
				sx := s.x + px
				if sx < 0 || sx >= 256 {
					continue
				}
				if sx < 8 && p.mask&maskLeftSpr == 0 {
					continue
				}
				bit := byte(7 - px)
				if flipH {
					bit = byte(px)
				}
				c := ((lo >> bit) & 1) | (((hi >> bit) & 1) << 1)
				if c == 0 {
					continue
				}
				// Sprite 0 hit
				if s.idx == 0 && bgRow[sx].color&0x80 != 0 && p.mask&maskShowBg != 0 && sx != 255 {
					p.status |= statSpr0
				}
				if bgPri && bgRow[sx].color&0x80 != 0 {
					continue
				}
				palIdx := p.palette[0x10+palSel*4+c] & 0x3F
				bgRow[sx] = bgPix{color: palIdx | 0x80}
			}
		}
	}

	// Commit bgRow + background fallback to frame
	for px := 0; px < 256; px++ {
		if bgRow[px].color&0x80 != 0 {
			row[y*256+px] = nesPalette[bgRow[px].color&0x3F]
		}
	}
}

// predictSprite0Hit walks sprite 0 against the background for scanline y
// and returns the first pixel where they both contain an opaque pixel
// (which is the pixel that would set $2002 bit 6 on real hardware). Returns
// -1 if no hit on this scanline.
//
// This lets StepFrame pre-set the flag at the correct mid-scanline cycle
// instead of only at end-of-scanline, which is what caused the status-bar
// jitter in Super Mario Bros: the coin animation shifts the hit scanline,
// and without mid-scanline timing the split wanders by a whole scanline.
func (p *PPU) predictSprite0Hit(y int) int {
	if p.mask&maskShowBg == 0 || p.mask&maskShowSpr == 0 {
		return -1
	}
	sy := int(p.oam[0])
	height := 8
	if p.ctrl&ctrlSprSize != 0 {
		height = 16
	}
	if y < sy || y >= sy+height {
		return -1
	}
	row := y - sy
	tile := p.oam[1]
	attr := p.oam[2]
	x0 := int(p.oam[3])
	flipH := attr&0x40 != 0
	flipV := attr&0x80 != 0
	if flipV {
		row = height - 1 - row
	}
	var patBase uint16
	var tileIdx int
	if height == 16 {
		patBase = uint16(tile&1) * 0x1000
		tileIdx = int(tile & 0xFE)
		if row >= 8 {
			tileIdx++
			row -= 8
		}
	} else {
		if p.ctrl&ctrlSprPtable != 0 {
			patBase = 0x1000
		}
		tileIdx = int(tile)
	}
	sprLo := p.vramRead(patBase + uint16(tileIdx)*16 + uint16(row))
	sprHi := p.vramRead(patBase + uint16(tileIdx)*16 + uint16(row) + 8)

	// Snapshot BG state so we can advance v while scanning without disturbing
	// the real registers.
	origV := p.v
	defer func() { p.v = origV }()

	for px := 0; px < 8; px++ {
		sx := x0 + px
		if sx >= 255 { // sprite 0 hit never fires on pixel 255
			return -1
		}
		if sx < 0 {
			continue
		}
		if sx < 8 && (p.mask&maskLeftSpr == 0 || p.mask&maskLeftBg == 0) {
			continue
		}
		bit := byte(7 - px)
		if flipH {
			bit = byte(px)
		}
		sprC := ((sprLo >> bit) & 1) | (((sprHi >> bit) & 1) << 1)
		if sprC == 0 {
			continue
		}
		if p.bgOpaqueAt(sx) {
			return sx
		}
	}
	return -1
}

// bgOpaqueAt returns whether the background pattern is opaque at screen X px,
// assuming the current v register is at the start of the scanline. Advances
// p.v as it walks pixels, but predictSprite0Hit snapshots/restores v so this
// is safe to call mid-scanline.
func (p *PPU) bgOpaqueAt(px int) bool {
	p.v = (p.v &^ 0)
	for x := 0; x <= px; x++ {
		fineX := (int(p.x) + x) & 7
		if x > 0 && fineX == 0 {
			p.incCoarseX()
		}
		if x != px {
			continue
		}
		ntAddr := 0x2000 | (p.v & 0x0FFF)
		tile := p.vramRead(ntAddr)
		fineY := (p.v >> 12) & 7
		ptBase := uint16(0)
		if p.ctrl&ctrlBgPtable != 0 {
			ptBase = 0x1000
		}
		lo := p.vramRead(ptBase + uint16(tile)*16 + fineY)
		hi := p.vramRead(ptBase + uint16(tile)*16 + fineY + 8)
		bit := byte(7 - fineX)
		c := ((lo >> bit) & 1) | (((hi >> bit) & 1) << 1)
		return c != 0
	}
	return false
}

// Step the PPU one scanline. Returns true if a frame was completed.
func (p *PPU) StepScanline() bool {
	// Pre-render
	if p.scanline == 261 {
		p.status &^= statVBlank | statSpr0 | statSprOv
		if p.mask&maskRender != 0 {
			// copyY happens on this scanline in real HW
			p.copyY()
			// Also copyX at cycle 257 conceptually
			p.copyX()
		}
		p.scanline = 0
		return false
	}
	// Visible
	if p.scanline < 240 {
		if p.mask&maskRender != 0 {
			// Ensure v has correct initial state for this scanline:
			// In real HW, copyX happens each scanline at cycle 257.
			// We handle incY+copyX inside renderScanline.
		}
		p.renderScanline(p.scanline)
		p.scanline++
		return false
	}
	// Post-render (240)
	if p.scanline == 240 {
		p.scanline++
		return false
	}
	// Enter VBlank
	if p.scanline == 241 {
		p.status |= statVBlank
		if p.ctrl&ctrlNmi != 0 {
			p.NMIPending = true
		}
		p.scanline++
		return false
	}
	// VBlank lines 242..260
	if p.scanline < 261 {
		p.scanline++
		if p.scanline == 261 {
			// Frame is done — next call starts pre-render
			p.frame++
			return true
		}
	}
	return false
}

// OAM DMA — called by the bus when CPU writes $4014
func (p *PPU) OAMDMA(page []byte) {
	for i := 0; i < 256; i++ {
		p.oam[(uint16(p.oamAddr)+uint16(i))&0xFF] = page[i]
	}
}
