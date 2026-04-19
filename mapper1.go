package main

// MMC1 / SxROM (mapper 1).
//
// Registers are written by a 5-bit serial shift register. CPU writes to any
// address in $8000-$FFFF:
//   - If bit 7 of the value is set: reset shift register, OR control reg with 0x0C.
//   - Else: shift in bit 0 of the value. On the 5th shift, commit the 5-bit
//     latch into one of 4 internal registers, selected by bits 13-14 of the
//     write address:
//       $8000-$9FFF: control
//       $A000-$BFFF: CHR bank 0
//       $C000-$DFFF: CHR bank 1
//       $E000-$FFFF: PRG bank
//
// Control register:
//   bits 0-1: mirroring (0: single0, 1: single1, 2: vertical, 3: horizontal)
//   bits 2-3: PRG mode (0,1: 32KB; 2: fix first @8000, switch @C000;
//                       3: fix last @C000, switch @8000)
//   bit 4   : CHR mode (0: 8KB banks, 1: 4KB banks)

type mapper1 struct {
	c *Cart

	shift  byte
	count  byte
	ctrl   byte
	chr0   byte
	chr1   byte
	prg    byte
	prgRAM [0x2000]byte

	prgBanks int
	chrBanks int
}

func newMapper1(c *Cart) *mapper1 {
	m := &mapper1{
		c:        c,
		ctrl:     0x0C, // default: PRG mode 3 (last fixed), CHR 8KB, horizontal
		shift:    0x10, // bit 4 set = empty marker for shift register
		prgBanks: len(c.PRG) / (16 * 1024),
		chrBanks: len(c.CHR) / (4 * 1024),
	}
	if m.chrBanks == 0 {
		m.chrBanks = 2 // 8KB of CHR RAM = 2 4KB banks
	}
	return m
}

func (m *mapper1) writeReg(addr uint16) {
	val := m.shift
	switch {
	case addr < 0xA000:
		m.ctrl = val & 0x1F
	case addr < 0xC000:
		m.chr0 = val & 0x1F
	case addr < 0xE000:
		m.chr1 = val & 0x1F
	default:
		m.prg = val & 0x0F // bit 4 is PRG RAM enable; ignore for now
	}
}

func (m *mapper1) WritePRG(addr uint16, v byte) {
	if addr >= 0x6000 && addr < 0x8000 {
		m.prgRAM[addr-0x6000] = v
		return
	}
	if addr < 0x8000 {
		return
	}
	if v&0x80 != 0 {
		m.shift = 0x10
		m.count = 0
		m.ctrl |= 0x0C
		return
	}
	m.shift = (m.shift >> 1) | ((v & 1) << 4)
	m.count++
	if m.count == 5 {
		m.writeReg(addr)
		m.shift = 0x10
		m.count = 0
	}
}

func (m *mapper1) ReadPRG(addr uint16) byte {
	if addr >= 0x6000 && addr < 0x8000 {
		return m.prgRAM[addr-0x6000]
	}
	if addr < 0x8000 {
		return 0
	}
	prgMode := (m.ctrl >> 2) & 3
	bank16 := 16 * 1024
	var bankIdx int
	lastBank := m.prgBanks - 1
	switch prgMode {
	case 0, 1: // 32KB, ignore low bit
		bankIdx = int(m.prg & 0x0E)
		if addr >= 0xC000 {
			bankIdx++
		}
	case 2: // fix first at 8000, switch at C000
		if addr < 0xC000 {
			bankIdx = 0
		} else {
			bankIdx = int(m.prg & 0x0F)
		}
	case 3: // fix last at C000, switch at 8000
		if addr < 0xC000 {
			bankIdx = int(m.prg & 0x0F)
		} else {
			bankIdx = lastBank
		}
	}
	off := int(addr) & (bank16 - 1)
	return m.c.PRG[(bankIdx%m.prgBanks)*bank16+off]
}

func (m *mapper1) ReadCHR(addr uint16) byte {
	bank4 := 4 * 1024
	var bankIdx int
	if m.ctrl&0x10 != 0 { // 4KB mode
		if addr < 0x1000 {
			bankIdx = int(m.chr0)
		} else {
			bankIdx = int(m.chr1)
		}
	} else { // 8KB mode
		bankIdx = int(m.chr0 & 0x1E)
		if addr >= 0x1000 {
			bankIdx++
		}
	}
	if m.chrBanks == 0 {
		return 0
	}
	off := int(addr) & (bank4 - 1)
	return m.c.CHR[(bankIdx%m.chrBanks)*bank4+off]
}

func (m *mapper1) WriteCHR(addr uint16, v byte) {
	if !m.c.HasCHRRAM {
		return
	}
	bank4 := 4 * 1024
	var bankIdx int
	if m.ctrl&0x10 != 0 {
		if addr < 0x1000 {
			bankIdx = int(m.chr0)
		} else {
			bankIdx = int(m.chr1)
		}
	} else {
		bankIdx = int(m.chr0 & 0x1E)
		if addr >= 0x1000 {
			bankIdx++
		}
	}
	off := int(addr) & (bank4 - 1)
	m.c.CHR[(bankIdx%m.chrBanks)*bank4+off] = v
}

func (m *mapper1) Mirror() Mirroring {
	switch m.ctrl & 3 {
	case 0:
		return MirrorSingle0
	case 1:
		return MirrorSingle1
	case 2:
		return MirrorVertical
	default:
		return MirrorHorizontal
	}
}
