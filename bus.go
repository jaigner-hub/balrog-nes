package main

type Controller struct {
	Buttons byte // A B Select Start Up Down Left Right (bit0 = A)
	shift   byte
	strobe  bool
}

func (c *Controller) Write(v byte) {
	c.strobe = v&1 != 0
	if c.strobe {
		c.shift = c.Buttons
	}
}

func (c *Controller) Read() byte {
	if c.strobe {
		return c.Buttons & 1
	}
	r := c.shift & 1
	c.shift = (c.shift >> 1) | 0x80 // return 1s after buttons shifted out
	return r
}

type NESBus struct {
	RAM  [2048]byte
	Cart *Cart
	PPU  *PPU
	APU  *APU
	CPU  *CPU
	Ctrl [2]Controller
}

func (b *NESBus) Read(addr uint16) byte {
	switch {
	case addr < 0x2000:
		return b.RAM[addr&0x07FF]
	case addr < 0x4000:
		return b.PPU.CPURead(addr & 0x2007)
	case addr == 0x4015:
		return b.APU.CPURead(addr)
	case addr == 0x4016:
		return 0x40 | (b.Ctrl[0].Read() & 1)
	case addr == 0x4017:
		return 0x40 | (b.Ctrl[1].Read() & 1)
	case addr >= 0x4020:
		return b.Cart.ReadPRG(addr)
	}
	return 0
}

func (b *NESBus) Write(addr uint16, v byte) {
	switch {
	case addr < 0x2000:
		b.RAM[addr&0x07FF] = v
	case addr < 0x4000:
		b.PPU.CPUWrite(addr&0x2007, v)
	case addr == 0x4014:
		// OAM DMA: copy 256 bytes from $XX00-$XXFF to OAM
		page := make([]byte, 256)
		base := uint16(v) << 8
		for i := 0; i < 256; i++ {
			page[i] = b.Read(base + uint16(i))
		}
		b.PPU.OAMDMA(page)
		// DMA takes 513 or 514 CPU cycles
		b.CPU.stall += 513
		if b.CPU.cycles&1 != 0 {
			b.CPU.stall++
		}
	case addr == 0x4016:
		b.Ctrl[0].Write(v)
		b.Ctrl[1].Write(v)
	case (addr >= 0x4000 && addr <= 0x4013) || addr == 0x4015 || addr == 0x4017:
		b.APU.CPUWrite(addr, v)
	case addr >= 0x4020:
		b.Cart.WritePRG(addr, v)
	}
}
