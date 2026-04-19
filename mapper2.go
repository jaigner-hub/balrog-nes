package main

// UxROM (mapper 2). Used by Castlevania, Mega Man, Contra, DuckTales and many
// others. Dead simple:
//   $8000-$BFFF: switchable 16KB PRG bank (selected by any $8000-$FFFF write)
//   $C000-$FFFF: fixed to the last 16KB PRG bank
//   CHR: 8KB of CHR-RAM (almost always — some games ship CHR-ROM)

type mapper2 struct {
	c        *Cart
	prgBank  int
	prgBanks int
}

func newMapper2(c *Cart) *mapper2 {
	return &mapper2{c: c, prgBanks: len(c.PRG) / (16 * 1024)}
}

func (m *mapper2) ReadPRG(addr uint16) byte {
	if addr < 0x8000 {
		return 0
	}
	bank16 := 16 * 1024
	var bank int
	if addr < 0xC000 {
		bank = m.prgBank % m.prgBanks
	} else {
		bank = m.prgBanks - 1
	}
	off := int(addr) & (bank16 - 1)
	return m.c.PRG[bank*bank16+off]
}

func (m *mapper2) WritePRG(addr uint16, v byte) {
	if addr >= 0x8000 {
		m.prgBank = int(v) & 0x0F
	}
}

func (m *mapper2) ReadCHR(addr uint16) byte { return m.c.CHR[int(addr)%len(m.c.CHR)] }
func (m *mapper2) WriteCHR(addr uint16, v byte) {
	if m.c.HasCHRRAM {
		m.c.CHR[int(addr)%len(m.c.CHR)] = v
	}
}
func (m *mapper2) Mirror() Mirroring { return m.c.initMirror }
