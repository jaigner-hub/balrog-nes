package main

// CNROM (mapper 3). PRG is fixed (16KB or 32KB, mirrored as needed). Writes
// to $8000-$FFFF select an 8KB CHR bank. Used by Arkanoid, Gradius, Solomon's
// Key, and a pile of smaller games.

type mapper3 struct {
	c        *Cart
	chrBank  int
	chrBanks int
}

func newMapper3(c *Cart) *mapper3 {
	chrBanks := len(c.CHR) / (8 * 1024)
	if chrBanks == 0 {
		chrBanks = 1
	}
	return &mapper3{c: c, chrBanks: chrBanks}
}

func (m *mapper3) ReadPRG(addr uint16) byte {
	if addr < 0x8000 {
		return 0
	}
	return m.c.PRG[int(addr-0x8000)%len(m.c.PRG)]
}

func (m *mapper3) WritePRG(addr uint16, v byte) {
	if addr >= 0x8000 {
		m.chrBank = int(v) & 0x03
	}
}

func (m *mapper3) ReadCHR(addr uint16) byte {
	bank8 := 8 * 1024
	bank := m.chrBank % m.chrBanks
	return m.c.CHR[bank*bank8+int(addr)]
}
func (m *mapper3) WriteCHR(addr uint16, v byte) {} // CHR is ROM on CNROM
func (m *mapper3) Mirror() Mirroring              { return m.c.initMirror }
