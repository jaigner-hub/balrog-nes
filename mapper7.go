package main

// Mapper 7 (AxROM / ANROM / AMROM / AOROM). Used by Battletoads, Wizards
// & Warriors, RC Pro-Am, Marble Madness, Solar Jetman, and a bunch of
// other Rare/Acclaim games.
//
// Very simple:
//   - One 32KB PRG bank switchable via any write to $8000-$FFFF
//     (bits 0-2 select the bank, giving up to 256KB of PRG)
//   - 8KB CHR-RAM (no CHR-ROM)
//   - Single-screen mirroring, the high bit (bit 4) of the bank register
//     picks which nametable (0 or 1) is mapped everywhere

type mapper7 struct {
	c      *Cart
	bank   byte // low 3 bits of the last bank-select write
	mirror Mirroring
}

func newMapper7(c *Cart) *mapper7 {
	return &mapper7{c: c, mirror: MirrorSingle0}
}

func (m *mapper7) ReadPRG(addr uint16) byte {
	if addr < 0x8000 {
		return 0
	}
	bankSize := 32 * 1024
	banks := len(m.c.PRG) / bankSize
	if banks == 0 {
		banks = 1
	}
	bank := int(m.bank) % banks
	off := int(addr - 0x8000)
	return m.c.PRG[bank*bankSize+off]
}

func (m *mapper7) WritePRG(addr uint16, v byte) {
	if addr < 0x8000 {
		return
	}
	m.bank = v & 0x07
	if v&0x10 != 0 {
		m.mirror = MirrorSingle1
	} else {
		m.mirror = MirrorSingle0
	}
}

func (m *mapper7) ReadCHR(addr uint16) byte {
	return m.c.CHR[int(addr)%len(m.c.CHR)]
}

func (m *mapper7) WriteCHR(addr uint16, v byte) {
	if m.c.HasCHRRAM {
		m.c.CHR[int(addr)%len(m.c.CHR)] = v
	}
}

func (m *mapper7) Mirror() Mirroring { return m.mirror }
