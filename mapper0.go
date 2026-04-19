package main

type mapper0 struct{ c *Cart }

func newMapper0(c *Cart) *mapper0 { return &mapper0{c: c} }

func (m *mapper0) ReadPRG(addr uint16) byte {
	return m.c.PRG[int(addr-0x8000)%len(m.c.PRG)]
}
func (m *mapper0) WritePRG(addr uint16, v byte) {}
func (m *mapper0) ReadCHR(addr uint16) byte     { return m.c.CHR[int(addr)%len(m.c.CHR)] }
func (m *mapper0) WriteCHR(addr uint16, v byte) {
	if m.c.HasCHRRAM {
		m.c.CHR[int(addr)%len(m.c.CHR)] = v
	}
}
func (m *mapper0) Mirror() Mirroring { return m.c.initMirror }
