package main

import "fmt"

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
		// OAM DMA: copy 256 bytes from $XX00-$XXFF to OAM, ticking each
		// cycle so PPU/APU advance in lockstep (513 or 514 CPU cycles).
		base := uint16(v) << 8
		b.CPU.tick() // T0: dummy halt cycle
		if b.CPU.cycles&1 != 0 {
			b.CPU.tick() // T1: extra alignment cycle on odd
		}
		for i := 0; i < 256; i++ {
			src := b.Read(base + uint16(i))
			b.CPU.tick() // read tick
			b.PPU.oam[(uint16(b.PPU.oamAddr)+uint16(i))&0xFF] = src
			b.CPU.tick() // write tick
		}
	case addr == 0x4016:
		b.Ctrl[0].Write(v)
		b.Ctrl[1].Write(v)
	case (addr >= 0x4000 && addr <= 0x4013) || addr == 0x4015 || addr == 0x4017:
		b.APU.CPUWrite(addr, v)
	case addr >= 0x4020:
		if debugIrqLog && b.PPU.frame == debugIrqFrame && addr >= 0xC000 && debugIrqLogFile != nil {
			debugIrqLogFile.WriteString(fmt.Sprintf("  [sc=%d cy=%d] MMC3 $%04X <- $%02X\n", b.PPU.scanline, b.PPU.cycle, addr, v))
		}
		b.Cart.WritePRG(addr, v)
	}
}
