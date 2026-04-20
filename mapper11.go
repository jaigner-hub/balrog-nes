package main

// Color Dreams (mapper 11). Used by the unlicensed Color Dreams catalog and
// Wisdom Tree's Bible/Christian games (Bible Adventures, Spiritual Warfare,
// Exodus, etc.). One register at $8000-$FFFF:
//   bits 0-1: 32KB PRG bank at $8000-$FFFF
//   bits 4-7: 8KB  CHR bank at $0000-$1FFF
// Mirroring is fixed from the iNES header. CHR is usually ROM.

type mapper11 struct {
	c        *Cart
	prgBank  int
	chrBank  int
	prgBanks int
	chrBanks int
}

func newMapper11(c *Cart) *mapper11 {
	prgBanks := len(c.PRG) / (32 * 1024)
	if prgBanks == 0 {
		prgBanks = 1
	}
	chrBanks := len(c.CHR) / (8 * 1024)
	if chrBanks == 0 {
		chrBanks = 1
	}
	return &mapper11{c: c, prgBanks: prgBanks, chrBanks: chrBanks}
}

func (m *mapper11) ReadPRG(addr uint16) byte {
	if addr < 0x8000 {
		return 0
	}
	bank32 := 32 * 1024
	bank := m.prgBank % m.prgBanks
	off := int(addr-0x8000) & (bank32 - 1)
	return m.c.PRG[bank*bank32+off]
}

func (m *mapper11) WritePRG(addr uint16, v byte) {
	if addr >= 0x8000 {
		m.prgBank = int(v) & 0x03
		m.chrBank = int(v>>4) & 0x0F
	}
}

func (m *mapper11) ReadCHR(addr uint16) byte {
	bank8 := 8 * 1024
	bank := m.chrBank % m.chrBanks
	return m.c.CHR[bank*bank8+int(addr)]
}

func (m *mapper11) WriteCHR(addr uint16, v byte) {
	if m.c.HasCHRRAM {
		m.c.CHR[int(addr)%len(m.c.CHR)] = v
	}
}

func (m *mapper11) Mirror() Mirroring { return m.c.initMirror }
