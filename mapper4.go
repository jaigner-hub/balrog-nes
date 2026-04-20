package main

import (
	"fmt"
	"os"
)

var mmc3LogFile *os.File

// MMC3 (mapper 4). Used by SMB3, Kirby's Adventure, Mega Man 3-6, Crystalis,
// Dragon Warrior III/IV, Final Fantasy, and many more. Big jump in complexity
// from MMC1:
//
//   - 8 bank registers (R0..R7) selected via $8000/$8001
//   - Two PRG layouts and two CHR layouts (bit 6 / bit 7 of $8000)
//   - Runtime mirroring control via $A000
//   - 8KB PRG-RAM at $6000-$7FFF
//   - Scanline IRQ: a counter clocked once per visible scanline (when BG
//     rendering is on). When it hits 0, the mapper asserts IRQ until the
//     CPU writes $E000 to acknowledge.

type mapper4 struct {
	c *Cart

	banks      [8]byte // R0..R7
	bankSelect byte    // low 3 bits select which Rn is written by $8001
	prgMode    byte    // bit 6 of $8000
	chrMode    byte    // bit 7 of $8000
	mirror     Mirroring
	prgRAM     [0x2000]byte

	// IRQ counter state
	irqLatch  byte
	irqCount  byte
	irqReload bool
	irqEnable bool
	irqFlag   bool // pending IRQ asserted until $E000 acknowledge

	// Diagnostic counters
	clockCount      int
	irqClocks       int
	lastClockFired  bool

	prgBanks int // count of 8KB PRG banks
	chrBanks int // count of 1KB CHR banks
}

func newMapper4(c *Cart) *mapper4 {
	chrBanks := len(c.CHR) / 1024
	if chrBanks == 0 {
		chrBanks = 8 // 8KB CHR-RAM = 8 1KB banks
	}
	m := &mapper4{
		c:        c,
		prgBanks: len(c.PRG) / 8192,
		chrBanks: chrBanks,
		mirror:   c.initMirror,
	}
	return m
}

// --- PRG ---

func (m *mapper4) ReadPRG(addr uint16) byte {
	switch {
	case addr >= 0x6000 && addr < 0x8000:
		return m.prgRAM[addr-0x6000]
	case addr < 0x8000:
		return 0
	}
	bank8 := 8192
	var bank int
	last := m.prgBanks - 1
	secondLast := m.prgBanks - 2
	switch {
	case addr < 0xA000: // $8000-$9FFF
		if m.prgMode == 0 {
			bank = int(m.banks[6]) & 0x3F
		} else {
			bank = secondLast
		}
	case addr < 0xC000: // $A000-$BFFF
		bank = int(m.banks[7]) & 0x3F
	case addr < 0xE000: // $C000-$DFFF
		if m.prgMode == 0 {
			bank = secondLast
		} else {
			bank = int(m.banks[6]) & 0x3F
		}
	default: // $E000-$FFFF
		bank = last
	}
	off := int(addr) & (bank8 - 1)
	return m.c.PRG[(bank%m.prgBanks)*bank8+off]
}

func (m *mapper4) WritePRG(addr uint16, v byte) {
	if addr >= 0x6000 && addr < 0x8000 {
		m.prgRAM[addr-0x6000] = v
		return
	}
	if addr < 0x8000 {
		return
	}
	even := addr&1 == 0
	switch {
	case addr < 0xA000: // $8000-$9FFF
		if even {
			m.bankSelect = v & 0x07
			m.prgMode = (v >> 6) & 1
			m.chrMode = (v >> 7) & 1
		} else {
			m.banks[m.bankSelect] = v
		}
	case addr < 0xC000: // $A000-$BFFF
		if even {
			if v&1 == 0 {
				m.mirror = MirrorVertical
			} else {
				m.mirror = MirrorHorizontal
			}
		}
		// $A001 is PRG-RAM protect; ignored (we always allow writes)
	case addr < 0xE000: // $C000-$DFFF
		if even {
			m.irqLatch = v
			if mmc3LogFile != nil {
				mmc3LogFile.WriteString(fmt.Sprintf("  $C000 <- latch=%d\n", v))
			}
		} else {
			m.irqCount = 0
			m.irqReload = true
			if mmc3LogFile != nil {
				mmc3LogFile.WriteString("  $C001 <- reload=true count=0\n")
			}
		}
	default: // $E000-$FFFF
		if even {
			m.irqEnable = false
			m.irqFlag = false
			if mmc3LogFile != nil {
				mmc3LogFile.WriteString("  $E000 <- irqEnable=false\n")
			}
		} else {
			m.irqEnable = true
			if mmc3LogFile != nil {
				mmc3LogFile.WriteString("  $E001 <- irqEnable=true\n")
			}
		}
	}
}

// --- CHR ---

func (m *mapper4) chrBankFor(addr uint16) int {
	// Bank is a 1KB unit. Map PPU $0000-$1FFF to a bank index, which we look
	// up against R0..R5 based on chrMode.
	a := int(addr) & 0x1FFF
	kb := a / 1024
	if m.chrMode == 0 {
		// 2KB/2KB/1KB/1KB/1KB/1KB layout
		switch kb {
		case 0:
			return int(m.banks[0] & 0xFE)
		case 1:
			return int(m.banks[0]&0xFE) + 1
		case 2:
			return int(m.banks[1] & 0xFE)
		case 3:
			return int(m.banks[1]&0xFE) + 1
		case 4:
			return int(m.banks[2])
		case 5:
			return int(m.banks[3])
		case 6:
			return int(m.banks[4])
		case 7:
			return int(m.banks[5])
		}
	} else {
		// 1KB/1KB/1KB/1KB/2KB/2KB layout (inverted A12)
		switch kb {
		case 0:
			return int(m.banks[2])
		case 1:
			return int(m.banks[3])
		case 2:
			return int(m.banks[4])
		case 3:
			return int(m.banks[5])
		case 4:
			return int(m.banks[0] & 0xFE)
		case 5:
			return int(m.banks[0]&0xFE) + 1
		case 6:
			return int(m.banks[1] & 0xFE)
		case 7:
			return int(m.banks[1]&0xFE) + 1
		}
	}
	return 0
}

func (m *mapper4) ReadCHR(addr uint16) byte {
	bank := m.chrBankFor(addr) % m.chrBanks
	off := int(addr) & 0x3FF
	return m.c.CHR[bank*1024+off]
}

func (m *mapper4) WriteCHR(addr uint16, v byte) {
	if !m.c.HasCHRRAM {
		return
	}
	bank := m.chrBankFor(addr) % m.chrBanks
	off := int(addr) & 0x3FF
	m.c.CHR[bank*1024+off] = v
}

func (m *mapper4) Mirror() Mirroring { return m.mirror }

// --- Scanline IRQ ---
//
// Real MMC3 counts A12 rising edges on the PPU bus. The closest practical
// emulation — and what the nestest/mmc3 test ROMs expect — is to clock the
// counter once per visible scanline, at the point where the PPU has finished
// rendering visible pixels and is about to start fetching sprites (around
// PPU cycle 260). Here the PPU calls ClockScanline at end of scanline when
// BG or sprite rendering is enabled.

// IrqFiredLast returns true if the most recent ClockScanline call asserted IRQ.
// Used for scanline-tagged IRQ logging in the PPU.
func (m *mapper4) IrqFiredLast() bool { return m.lastClockFired }

// ClockScanline emulates MMC3 Rev B IRQ semantics (SMB3, Mega Man 3):
// reload the counter from the latch if it's zero (or $C001's pending-
// reload flag is set), otherwise decrement. After that, fire the IRQ
// if the counter ended at zero and IRQ is enabled. Rev A's
// "don't-fire-on-natural-reload" quirk (blargg test 6-MMC3_alt) is
// intentionally not modeled — the games I care about are Rev B.
func (m *mapper4) ClockScanline() {
	m.lastClockFired = false
	if m.irqCount == 0 || m.irqReload {
		m.irqCount = m.irqLatch
		m.irqReload = false
	} else {
		m.irqCount--
	}
	if m.irqCount == 0 && m.irqEnable {
		m.irqFlag = true
		m.irqClocks++
		m.lastClockFired = true
	}
	m.clockCount++
}

// Debug counters (not part of state).
var _ = struct{}{} // silence

func (m *mapper4) IRQPending() bool { return m.irqFlag }
