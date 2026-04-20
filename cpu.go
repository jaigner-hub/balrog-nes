package main

import "fmt"

// 6502 flags
const (
	flagC byte = 1 << 0
	flagZ byte = 1 << 1
	flagI byte = 1 << 2
	flagD byte = 1 << 3
	flagB byte = 1 << 4
	flagU byte = 1 << 5
	flagV byte = 1 << 6
	flagN byte = 1 << 7
)

type CPU struct {
	A, X, Y byte
	PC      uint16
	S       byte
	P       byte

	Bus Bus

	cycles    uint64
	stall     int
	nmiPend   bool
	irqPend   bool
	addrMode  amode
	operand   uint16
	pageCross bool

	// Called once per CPU cycle. Owner uses this to advance PPU/APU in
	// lockstep so that bus accesses appear at the correct PPU cycle within
	// each instruction. Each bus access (read or write) ticks once; opcodes
	// with internal cycles tick explicitly.
	tickFn func()

	// IRQ/NMI sampling — real 6502 samples the interrupt lines at phi2 of
	// the penultimate cycle (T-1) of each instruction, not at the final
	// cycle. If asserted at T-1, the interrupt is taken after the current
	// instruction. If asserted only at T (the last cycle), it's delayed by
	// one instruction. We model this by having tickFn set `rawIRQ`/`rawNMI`
	// each cycle, and `Step` snapshotting them into `irqPend`/`nmiPend`
	// only through the instruction's penultimate cycle.
	rawIRQ   bool
	rawNMI   bool
	irqLatch bool // staged IRQ line, 1 cycle pipeline
	nmiLatch bool
}

func (c *CPU) tick() {
	c.cycles++
	if c.tickFn != nil {
		c.tickFn()
	}
	// 6502 IRQ sampling: the interrupt line is sampled at phi2 of cycle
	// T-1, and the decision to take the interrupt is made before the last
	// cycle of the instruction. If IRQ is asserted at T-1, take after T.
	// If only asserted at T (last cycle), it's delayed one instruction.
	// We model this with a 1-cycle rolling latch: `irqPend` is set from
	// the previous cycle's raw state, so the first instruction boundary
	// after an IRQ assertion still sees it only if the line was already
	// low on the cycle before.
	c.irqPend = c.irqLatch
	c.irqLatch = c.rawIRQ
	c.nmiPend = c.nmiPend || c.nmiLatch
	c.nmiLatch = c.rawNMI
	c.rawNMI = false // edge-triggered: consume immediately after latching
}

func (c *CPU) read(addr uint16) byte {
	v := c.Bus.Read(addr)
	c.tick()
	return v
}

func (c *CPU) write(addr uint16, v byte) {
	c.Bus.Write(addr, v)
	c.tick()
}

type Bus interface {
	Read(addr uint16) byte
	Write(addr uint16, v byte)
}

type amode int

const (
	amIMP amode = iota
	amACC
	amIMM
	amZP
	amZPX
	amZPY
	amREL
	amABS
	amABSX
	amABSY
	amIND
	amIZX
	amIZY
)

type opInfo struct {
	name   string
	mode   amode
	cycles int
	fn     func(c *CPU)
}

var opTable [256]opInfo

func init() {
	// Fill with illegal/NOP default
	for i := range opTable {
		opTable[i] = opInfo{"???", amIMP, 2, (*CPU).opNOP}
	}
	type row struct {
		op     byte
		name   string
		mode   amode
		cycles int
		fn     func(c *CPU)
	}
	rows := []row{
		// ADC
		{0x69, "ADC", amIMM, 2, (*CPU).opADC}, {0x65, "ADC", amZP, 3, (*CPU).opADC},
		{0x75, "ADC", amZPX, 4, (*CPU).opADC}, {0x6D, "ADC", amABS, 4, (*CPU).opADC},
		{0x7D, "ADC", amABSX, 4, (*CPU).opADC}, {0x79, "ADC", amABSY, 4, (*CPU).opADC},
		{0x61, "ADC", amIZX, 6, (*CPU).opADC}, {0x71, "ADC", amIZY, 5, (*CPU).opADC},
		// AND
		{0x29, "AND", amIMM, 2, (*CPU).opAND}, {0x25, "AND", amZP, 3, (*CPU).opAND},
		{0x35, "AND", amZPX, 4, (*CPU).opAND}, {0x2D, "AND", amABS, 4, (*CPU).opAND},
		{0x3D, "AND", amABSX, 4, (*CPU).opAND}, {0x39, "AND", amABSY, 4, (*CPU).opAND},
		{0x21, "AND", amIZX, 6, (*CPU).opAND}, {0x31, "AND", amIZY, 5, (*CPU).opAND},
		// ASL
		{0x0A, "ASL", amACC, 2, (*CPU).opASL}, {0x06, "ASL", amZP, 5, (*CPU).opASL},
		{0x16, "ASL", amZPX, 6, (*CPU).opASL}, {0x0E, "ASL", amABS, 6, (*CPU).opASL},
		{0x1E, "ASL", amABSX, 7, (*CPU).opASL},
		// Branches
		{0x90, "BCC", amREL, 2, (*CPU).opBCC}, {0xB0, "BCS", amREL, 2, (*CPU).opBCS},
		{0xF0, "BEQ", amREL, 2, (*CPU).opBEQ}, {0x30, "BMI", amREL, 2, (*CPU).opBMI},
		{0xD0, "BNE", amREL, 2, (*CPU).opBNE}, {0x10, "BPL", amREL, 2, (*CPU).opBPL},
		{0x50, "BVC", amREL, 2, (*CPU).opBVC}, {0x70, "BVS", amREL, 2, (*CPU).opBVS},
		// BIT
		{0x24, "BIT", amZP, 3, (*CPU).opBIT}, {0x2C, "BIT", amABS, 4, (*CPU).opBIT},
		// BRK
		{0x00, "BRK", amIMP, 7, (*CPU).opBRK},
		// Flag ops
		{0x18, "CLC", amIMP, 2, (*CPU).opCLC}, {0xD8, "CLD", amIMP, 2, (*CPU).opCLD},
		{0x58, "CLI", amIMP, 2, (*CPU).opCLI}, {0xB8, "CLV", amIMP, 2, (*CPU).opCLV},
		{0x38, "SEC", amIMP, 2, (*CPU).opSEC}, {0xF8, "SED", amIMP, 2, (*CPU).opSED},
		{0x78, "SEI", amIMP, 2, (*CPU).opSEI},
		// CMP
		{0xC9, "CMP", amIMM, 2, (*CPU).opCMP}, {0xC5, "CMP", amZP, 3, (*CPU).opCMP},
		{0xD5, "CMP", amZPX, 4, (*CPU).opCMP}, {0xCD, "CMP", amABS, 4, (*CPU).opCMP},
		{0xDD, "CMP", amABSX, 4, (*CPU).opCMP}, {0xD9, "CMP", amABSY, 4, (*CPU).opCMP},
		{0xC1, "CMP", amIZX, 6, (*CPU).opCMP}, {0xD1, "CMP", amIZY, 5, (*CPU).opCMP},
		// CPX / CPY
		{0xE0, "CPX", amIMM, 2, (*CPU).opCPX}, {0xE4, "CPX", amZP, 3, (*CPU).opCPX},
		{0xEC, "CPX", amABS, 4, (*CPU).opCPX},
		{0xC0, "CPY", amIMM, 2, (*CPU).opCPY}, {0xC4, "CPY", amZP, 3, (*CPU).opCPY},
		{0xCC, "CPY", amABS, 4, (*CPU).opCPY},
		// DEC / DEX / DEY
		{0xC6, "DEC", amZP, 5, (*CPU).opDEC}, {0xD6, "DEC", amZPX, 6, (*CPU).opDEC},
		{0xCE, "DEC", amABS, 6, (*CPU).opDEC}, {0xDE, "DEC", amABSX, 7, (*CPU).opDEC},
		{0xCA, "DEX", amIMP, 2, (*CPU).opDEX}, {0x88, "DEY", amIMP, 2, (*CPU).opDEY},
		// EOR
		{0x49, "EOR", amIMM, 2, (*CPU).opEOR}, {0x45, "EOR", amZP, 3, (*CPU).opEOR},
		{0x55, "EOR", amZPX, 4, (*CPU).opEOR}, {0x4D, "EOR", amABS, 4, (*CPU).opEOR},
		{0x5D, "EOR", amABSX, 4, (*CPU).opEOR}, {0x59, "EOR", amABSY, 4, (*CPU).opEOR},
		{0x41, "EOR", amIZX, 6, (*CPU).opEOR}, {0x51, "EOR", amIZY, 5, (*CPU).opEOR},
		// INC / INX / INY
		{0xE6, "INC", amZP, 5, (*CPU).opINC}, {0xF6, "INC", amZPX, 6, (*CPU).opINC},
		{0xEE, "INC", amABS, 6, (*CPU).opINC}, {0xFE, "INC", amABSX, 7, (*CPU).opINC},
		{0xE8, "INX", amIMP, 2, (*CPU).opINX}, {0xC8, "INY", amIMP, 2, (*CPU).opINY},
		// JMP / JSR / RTS / RTI
		{0x4C, "JMP", amABS, 3, (*CPU).opJMP}, {0x6C, "JMP", amIND, 5, (*CPU).opJMP},
		{0x20, "JSR", amABS, 6, (*CPU).opJSR},
		{0x60, "RTS", amIMP, 6, (*CPU).opRTS}, {0x40, "RTI", amIMP, 6, (*CPU).opRTI},
		// LDA
		{0xA9, "LDA", amIMM, 2, (*CPU).opLDA}, {0xA5, "LDA", amZP, 3, (*CPU).opLDA},
		{0xB5, "LDA", amZPX, 4, (*CPU).opLDA}, {0xAD, "LDA", amABS, 4, (*CPU).opLDA},
		{0xBD, "LDA", amABSX, 4, (*CPU).opLDA}, {0xB9, "LDA", amABSY, 4, (*CPU).opLDA},
		{0xA1, "LDA", amIZX, 6, (*CPU).opLDA}, {0xB1, "LDA", amIZY, 5, (*CPU).opLDA},
		// LDX / LDY
		{0xA2, "LDX", amIMM, 2, (*CPU).opLDX}, {0xA6, "LDX", amZP, 3, (*CPU).opLDX},
		{0xB6, "LDX", amZPY, 4, (*CPU).opLDX}, {0xAE, "LDX", amABS, 4, (*CPU).opLDX},
		{0xBE, "LDX", amABSY, 4, (*CPU).opLDX},
		{0xA0, "LDY", amIMM, 2, (*CPU).opLDY}, {0xA4, "LDY", amZP, 3, (*CPU).opLDY},
		{0xB4, "LDY", amZPX, 4, (*CPU).opLDY}, {0xAC, "LDY", amABS, 4, (*CPU).opLDY},
		{0xBC, "LDY", amABSX, 4, (*CPU).opLDY},
		// LSR
		{0x4A, "LSR", amACC, 2, (*CPU).opLSR}, {0x46, "LSR", amZP, 5, (*CPU).opLSR},
		{0x56, "LSR", amZPX, 6, (*CPU).opLSR}, {0x4E, "LSR", amABS, 6, (*CPU).opLSR},
		{0x5E, "LSR", amABSX, 7, (*CPU).opLSR},
		// NOP
		{0xEA, "NOP", amIMP, 2, (*CPU).opNOP},
		// ORA
		{0x09, "ORA", amIMM, 2, (*CPU).opORA}, {0x05, "ORA", amZP, 3, (*CPU).opORA},
		{0x15, "ORA", amZPX, 4, (*CPU).opORA}, {0x0D, "ORA", amABS, 4, (*CPU).opORA},
		{0x1D, "ORA", amABSX, 4, (*CPU).opORA}, {0x19, "ORA", amABSY, 4, (*CPU).opORA},
		{0x01, "ORA", amIZX, 6, (*CPU).opORA}, {0x11, "ORA", amIZY, 5, (*CPU).opORA},
		// Stack
		{0x48, "PHA", amIMP, 3, (*CPU).opPHA}, {0x68, "PLA", amIMP, 4, (*CPU).opPLA},
		{0x08, "PHP", amIMP, 3, (*CPU).opPHP}, {0x28, "PLP", amIMP, 4, (*CPU).opPLP},
		// ROL / ROR
		{0x2A, "ROL", amACC, 2, (*CPU).opROL}, {0x26, "ROL", amZP, 5, (*CPU).opROL},
		{0x36, "ROL", amZPX, 6, (*CPU).opROL}, {0x2E, "ROL", amABS, 6, (*CPU).opROL},
		{0x3E, "ROL", amABSX, 7, (*CPU).opROL},
		{0x6A, "ROR", amACC, 2, (*CPU).opROR}, {0x66, "ROR", amZP, 5, (*CPU).opROR},
		{0x76, "ROR", amZPX, 6, (*CPU).opROR}, {0x6E, "ROR", amABS, 6, (*CPU).opROR},
		{0x7E, "ROR", amABSX, 7, (*CPU).opROR},
		// SBC
		{0xE9, "SBC", amIMM, 2, (*CPU).opSBC}, {0xE5, "SBC", amZP, 3, (*CPU).opSBC},
		{0xF5, "SBC", amZPX, 4, (*CPU).opSBC}, {0xED, "SBC", amABS, 4, (*CPU).opSBC},
		{0xFD, "SBC", amABSX, 4, (*CPU).opSBC}, {0xF9, "SBC", amABSY, 4, (*CPU).opSBC},
		{0xE1, "SBC", amIZX, 6, (*CPU).opSBC}, {0xF1, "SBC", amIZY, 5, (*CPU).opSBC},
		{0xEB, "SBC", amIMM, 2, (*CPU).opSBC}, // illegal duplicate
		// STA / STX / STY
		{0x85, "STA", amZP, 3, (*CPU).opSTA}, {0x95, "STA", amZPX, 4, (*CPU).opSTA},
		{0x8D, "STA", amABS, 4, (*CPU).opSTA}, {0x9D, "STA", amABSX, 5, (*CPU).opSTA},
		{0x99, "STA", amABSY, 5, (*CPU).opSTA},
		{0x81, "STA", amIZX, 6, (*CPU).opSTA}, {0x91, "STA", amIZY, 6, (*CPU).opSTA},
		{0x86, "STX", amZP, 3, (*CPU).opSTX}, {0x96, "STX", amZPY, 4, (*CPU).opSTX},
		{0x8E, "STX", amABS, 4, (*CPU).opSTX},
		{0x84, "STY", amZP, 3, (*CPU).opSTY}, {0x94, "STY", amZPX, 4, (*CPU).opSTY},
		{0x8C, "STY", amABS, 4, (*CPU).opSTY},
		// Transfers
		{0xAA, "TAX", amIMP, 2, (*CPU).opTAX}, {0xA8, "TAY", amIMP, 2, (*CPU).opTAY},
		{0xBA, "TSX", amIMP, 2, (*CPU).opTSX}, {0x8A, "TXA", amIMP, 2, (*CPU).opTXA},
		{0x9A, "TXS", amIMP, 2, (*CPU).opTXS}, {0x98, "TYA", amIMP, 2, (*CPU).opTYA},
		// Illegal NOPs
		{0x1A, "NOP", amIMP, 2, (*CPU).opNOP}, {0x3A, "NOP", amIMP, 2, (*CPU).opNOP},
		{0x5A, "NOP", amIMP, 2, (*CPU).opNOP}, {0x7A, "NOP", amIMP, 2, (*CPU).opNOP},
		{0xDA, "NOP", amIMP, 2, (*CPU).opNOP}, {0xFA, "NOP", amIMP, 2, (*CPU).opNOP},
		{0x80, "NOP", amIMM, 2, (*CPU).opNOP}, {0x82, "NOP", amIMM, 2, (*CPU).opNOP},
		{0x89, "NOP", amIMM, 2, (*CPU).opNOP}, {0xC2, "NOP", amIMM, 2, (*CPU).opNOP},
		{0xE2, "NOP", amIMM, 2, (*CPU).opNOP},
		{0x04, "NOP", amZP, 3, (*CPU).opNOP}, {0x44, "NOP", amZP, 3, (*CPU).opNOP},
		{0x64, "NOP", amZP, 3, (*CPU).opNOP},
		{0x14, "NOP", amZPX, 4, (*CPU).opNOP}, {0x34, "NOP", amZPX, 4, (*CPU).opNOP},
		{0x54, "NOP", amZPX, 4, (*CPU).opNOP}, {0x74, "NOP", amZPX, 4, (*CPU).opNOP},
		{0xD4, "NOP", amZPX, 4, (*CPU).opNOP}, {0xF4, "NOP", amZPX, 4, (*CPU).opNOP},
		{0x0C, "NOP", amABS, 4, (*CPU).opNOP},
		{0x1C, "NOP", amABSX, 4, (*CPU).opNOP}, {0x3C, "NOP", amABSX, 4, (*CPU).opNOP},
		{0x5C, "NOP", amABSX, 4, (*CPU).opNOP}, {0x7C, "NOP", amABSX, 4, (*CPU).opNOP},
		{0xDC, "NOP", amABSX, 4, (*CPU).opNOP}, {0xFC, "NOP", amABSX, 4, (*CPU).opNOP},
		// Illegal LAX/SAX/DCP/ISB/SLO/RLA/SRE/RRA — common ones nestest uses
		{0xA7, "LAX", amZP, 3, (*CPU).opLAX}, {0xB7, "LAX", amZPY, 4, (*CPU).opLAX},
		{0xAF, "LAX", amABS, 4, (*CPU).opLAX}, {0xBF, "LAX", amABSY, 4, (*CPU).opLAX},
		{0xA3, "LAX", amIZX, 6, (*CPU).opLAX}, {0xB3, "LAX", amIZY, 5, (*CPU).opLAX},
		{0x87, "SAX", amZP, 3, (*CPU).opSAX}, {0x97, "SAX", amZPY, 4, (*CPU).opSAX},
		{0x83, "SAX", amIZX, 6, (*CPU).opSAX}, {0x8F, "SAX", amABS, 4, (*CPU).opSAX},
		{0xC7, "DCP", amZP, 5, (*CPU).opDCP}, {0xD7, "DCP", amZPX, 6, (*CPU).opDCP},
		{0xCF, "DCP", amABS, 6, (*CPU).opDCP}, {0xDF, "DCP", amABSX, 7, (*CPU).opDCP},
		{0xDB, "DCP", amABSY, 7, (*CPU).opDCP},
		{0xC3, "DCP", amIZX, 8, (*CPU).opDCP}, {0xD3, "DCP", amIZY, 8, (*CPU).opDCP},
		{0xE7, "ISB", amZP, 5, (*CPU).opISB}, {0xF7, "ISB", amZPX, 6, (*CPU).opISB},
		{0xEF, "ISB", amABS, 6, (*CPU).opISB}, {0xFF, "ISB", amABSX, 7, (*CPU).opISB},
		{0xFB, "ISB", amABSY, 7, (*CPU).opISB},
		{0xE3, "ISB", amIZX, 8, (*CPU).opISB}, {0xF3, "ISB", amIZY, 8, (*CPU).opISB},
		{0x07, "SLO", amZP, 5, (*CPU).opSLO}, {0x17, "SLO", amZPX, 6, (*CPU).opSLO},
		{0x0F, "SLO", amABS, 6, (*CPU).opSLO}, {0x1F, "SLO", amABSX, 7, (*CPU).opSLO},
		{0x1B, "SLO", amABSY, 7, (*CPU).opSLO},
		{0x03, "SLO", amIZX, 8, (*CPU).opSLO}, {0x13, "SLO", amIZY, 8, (*CPU).opSLO},
		{0x27, "RLA", amZP, 5, (*CPU).opRLA}, {0x37, "RLA", amZPX, 6, (*CPU).opRLA},
		{0x2F, "RLA", amABS, 6, (*CPU).opRLA}, {0x3F, "RLA", amABSX, 7, (*CPU).opRLA},
		{0x3B, "RLA", amABSY, 7, (*CPU).opRLA},
		{0x23, "RLA", amIZX, 8, (*CPU).opRLA}, {0x33, "RLA", amIZY, 8, (*CPU).opRLA},
		{0x47, "SRE", amZP, 5, (*CPU).opSRE}, {0x57, "SRE", amZPX, 6, (*CPU).opSRE},
		{0x4F, "SRE", amABS, 6, (*CPU).opSRE}, {0x5F, "SRE", amABSX, 7, (*CPU).opSRE},
		{0x5B, "SRE", amABSY, 7, (*CPU).opSRE},
		{0x43, "SRE", amIZX, 8, (*CPU).opSRE}, {0x53, "SRE", amIZY, 8, (*CPU).opSRE},
		{0x67, "RRA", amZP, 5, (*CPU).opRRA}, {0x77, "RRA", amZPX, 6, (*CPU).opRRA},
		{0x6F, "RRA", amABS, 6, (*CPU).opRRA}, {0x7F, "RRA", amABSX, 7, (*CPU).opRRA},
		{0x7B, "RRA", amABSY, 7, (*CPU).opRRA},
		{0x63, "RRA", amIZX, 8, (*CPU).opRRA}, {0x73, "RRA", amIZY, 8, (*CPU).opRRA},
	}
	for _, r := range rows {
		opTable[r.op] = opInfo{r.name, r.mode, r.cycles, r.fn}
	}
}

func (c *CPU) Reset() {
	c.A, c.X, c.Y = 0, 0, 0
	c.S = 0xFD
	c.P = flagI | flagU
	// Read reset vector directly via Bus (no tick — PPU not yet running)
	lo := uint16(c.Bus.Read(0xFFFC))
	hi := uint16(c.Bus.Read(0xFFFD))
	c.PC = lo | (hi << 8)
	c.cycles = 7
}

func (c *CPU) setZN(v byte) {
	c.P &^= flagZ | flagN
	if v == 0 {
		c.P |= flagZ
	}
	if v&0x80 != 0 {
		c.P |= flagN
	}
}

func (c *CPU) read16(addr uint16) uint16 {
	lo := uint16(c.read(addr))
	hi := uint16(c.read(addr + 1))
	return lo | (hi << 8)
}

// 6502 indirect JMP bug + zero-page wrap helper
func (c *CPU) read16bug(addr uint16) uint16 {
	lo := uint16(c.read(addr))
	hi := uint16(c.read((addr & 0xFF00) | ((addr + 1) & 0x00FF)))
	return lo | (hi << 8)
}

func (c *CPU) push(v byte) {
	c.write(0x0100|uint16(c.S), v)
	c.S--
}

func (c *CPU) pop() byte {
	c.S++
	return c.read(0x0100 | uint16(c.S))
}

func (c *CPU) push16(v uint16) {
	c.push(byte(v >> 8))
	c.push(byte(v))
}

func (c *CPU) pop16() uint16 {
	lo := uint16(c.pop())
	hi := uint16(c.pop())
	return lo | (hi << 8)
}

func pagesDiffer(a, b uint16) bool { return a&0xFF00 != b&0xFF00 }

func (c *CPU) fetchOperand(mode amode) {
	c.pageCross = false
	switch mode {
	case amIMP, amACC:
		c.operand = 0
	case amIMM:
		c.operand = c.PC
		c.PC++
	case amZP:
		c.operand = uint16(c.read(c.PC))
		c.PC++
	case amZPX:
		// T2 read zp addr, T3 internal (add X)
		c.operand = uint16((c.read(c.PC) + c.X) & 0xFF)
		c.PC++
		c.tick() // T3 internal add
	case amZPY:
		c.operand = uint16((c.read(c.PC) + c.Y) & 0xFF)
		c.PC++
		c.tick() // T3 internal add
	case amREL:
		off := int8(c.read(c.PC))
		c.PC++
		c.operand = uint16(int32(c.PC) + int32(off))
	case amABS:
		c.operand = c.read16(c.PC)
		c.PC += 2
	case amABSX:
		base := c.read16(c.PC)
		c.PC += 2
		c.operand = base + uint16(c.X)
		c.pageCross = pagesDiffer(base, c.operand)
	case amABSY:
		base := c.read16(c.PC)
		c.PC += 2
		c.operand = base + uint16(c.Y)
		c.pageCross = pagesDiffer(base, c.operand)
	case amIND:
		ptr := c.read16(c.PC)
		c.PC += 2
		c.operand = c.read16bug(ptr)
	case amIZX:
		// T2 fetch zp ptr, T3 dummy read at ptr (internal add X), T4/T5 read addr
		zpRaw := c.read(c.PC)
		c.PC++
		c.tick() // T3 internal: add X to zp pointer
		zp := zpRaw + c.X
		lo := uint16(c.read(uint16(zp)))
		hi := uint16(c.read(uint16(zp + 1)))
		c.operand = lo | (hi << 8)
	case amIZY:
		zp := c.read(c.PC)
		c.PC++
		lo := uint16(c.read(uint16(zp)))
		hi := uint16(c.read(uint16(zp + 1)))
		base := lo | (hi << 8)
		c.operand = base + uint16(c.Y)
		c.pageCross = pagesDiffer(base, c.operand)
	}
}

func (c *CPU) NMI() { c.nmiPend = true }
func (c *CPU) IRQ() { c.irqPend = true }

func (c *CPU) doNMI() {
	c.tick() // T1 internal
	c.tick() // T2 internal
	c.push16(c.PC)
	c.push((c.P | flagU) &^ flagB)
	c.P |= flagI
	c.PC = c.read16(0xFFFA)
}

func (c *CPU) doIRQ() {
	c.tick() // T1 internal
	c.tick() // T2 internal
	c.push16(c.PC)
	c.push((c.P | flagU) &^ flagB)
	c.P |= flagI
	c.PC = c.read16(0xFFFE)
}

func (c *CPU) Step() int {
	startCycles := c.cycles
	if c.stall > 0 {
		c.stall--
		c.tick()
		return 1
	}
	if c.nmiPend {
		c.nmiPend = false
		c.doNMI()
		return int(c.cycles - startCycles)
	}
	if c.irqPend && c.P&flagI == 0 {
		c.irqPend = false
		c.doIRQ()
		return int(c.cycles - startCycles)
	}
	op := c.read(c.PC)
	c.PC++
	info := opTable[op]
	c.addrMode = info.mode
	c.fetchOperand(info.mode)
	info.fn(c)
	return int(c.cycles - startCycles)
}

// --- operand access helpers (for instructions) ---
func (c *CPU) loadOperand() byte {
	if c.addrMode == amACC {
		return c.A
	}
	return c.read(c.operand)
}
func (c *CPU) storeOperand(v byte) {
	if c.addrMode == amACC {
		c.A = v
	} else {
		c.write(c.operand, v)
	}
}

// --- opcodes ---
func (c *CPU) opADC() { c.loadDummy(); c.adc(c.read(c.operand)) }
func (c *CPU) opSBC() { c.loadDummy(); c.adc(^c.read(c.operand)) }
func (c *CPU) adc(v byte) {
	a := c.A
	carry := byte(0)
	if c.P&flagC != 0 {
		carry = 1
	}
	s := uint16(a) + uint16(v) + uint16(carry)
	c.P &^= flagC | flagV
	if s > 0xFF {
		c.P |= flagC
	}
	if (a^v)&0x80 == 0 && (a^byte(s))&0x80 != 0 {
		c.P |= flagV
	}
	c.A = byte(s)
	c.setZN(c.A)
}

func (c *CPU) opAND() { c.loadDummy(); c.A &= c.read(c.operand); c.setZN(c.A) }
func (c *CPU) opORA() { c.loadDummy(); c.A |= c.read(c.operand); c.setZN(c.A) }
func (c *CPU) opEOR() { c.loadDummy(); c.A ^= c.read(c.operand); c.setZN(c.A) }

// RMW (read-modify-write) on memory: read, dummy write old, write new.
// On accumulator (ACC mode), it's just T1 op + T2 internal.
// On indexed addressing modes, an extra dummy read at the uncorrected
// address happens before the real read (regardless of page-cross).
func (c *CPU) rmw(modify func(byte) byte) {
	if c.addrMode == amACC {
		c.tick()
		c.A = modify(c.A)
		c.setZN(c.A)
		return
	}
	switch c.addrMode {
	case amABSX, amABSY, amIZY:
		c.tick() // dummy read at uncorrected addr (indexed always pays this)
	}
	v := c.read(c.operand)
	c.write(c.operand, v) // dummy write of old value (RMW pipeline)
	v = modify(v)
	c.write(c.operand, v)
	c.setZN(v)
}

func (c *CPU) opASL() {
	c.rmw(func(v byte) byte {
		c.P &^= flagC
		if v&0x80 != 0 {
			c.P |= flagC
		}
		return v << 1
	})
}
func (c *CPU) opLSR() {
	c.rmw(func(v byte) byte {
		c.P &^= flagC
		if v&1 != 0 {
			c.P |= flagC
		}
		return v >> 1
	})
}
func (c *CPU) opROL() {
	c.rmw(func(v byte) byte {
		oldC := byte(0)
		if c.P&flagC != 0 {
			oldC = 1
		}
		c.P &^= flagC
		if v&0x80 != 0 {
			c.P |= flagC
		}
		return (v << 1) | oldC
	})
}
func (c *CPU) opROR() {
	c.rmw(func(v byte) byte {
		oldC := byte(0)
		if c.P&flagC != 0 {
			oldC = 0x80
		}
		c.P &^= flagC
		if v&1 != 0 {
			c.P |= flagC
		}
		return (v >> 1) | oldC
	})
}

func (c *CPU) branch(cond bool) {
	if cond {
		c.tick() // taken branch: dummy read at PC
		if pagesDiffer(c.PC, c.operand) {
			c.tick() // page cross: dummy read at uncorrected target
		}
		c.PC = c.operand
	}
}
func (c *CPU) opBCC() { c.branch(c.P&flagC == 0) }
func (c *CPU) opBCS() { c.branch(c.P&flagC != 0) }
func (c *CPU) opBEQ() { c.branch(c.P&flagZ != 0) }
func (c *CPU) opBNE() { c.branch(c.P&flagZ == 0) }
func (c *CPU) opBMI() { c.branch(c.P&flagN != 0) }
func (c *CPU) opBPL() { c.branch(c.P&flagN == 0) }
func (c *CPU) opBVC() { c.branch(c.P&flagV == 0) }
func (c *CPU) opBVS() { c.branch(c.P&flagV != 0) }

func (c *CPU) opBIT() {
	v := c.read(c.operand)
	c.P &^= flagZ | flagN | flagV
	if c.A&v == 0 {
		c.P |= flagZ
	}
	c.P |= v & (flagN | flagV)
}

// BRK: 7 cycles. T1 op, T2 dummy fetch (PC++), T3 push PCH, T4 push PCL,
// T5 push P (with B+U), T6 read low vector, T7 read high vector.
func (c *CPU) opBRK() {
	c.tick()  // T2 dummy fetch (PC was already incremented by Step)
	c.PC++    // BRK is treated as 2-byte: skip the byte after $00
	c.push16(c.PC)
	c.push(c.P | flagB | flagU)
	c.P |= flagI
	c.PC = c.read16(0xFFFE)
}

// Flag ops: 2 cycles total. T1 opcode (already ticked), T2 internal.
func (c *CPU) opCLC() { c.tick(); c.P &^= flagC }
func (c *CPU) opCLD() { c.tick(); c.P &^= flagD }
func (c *CPU) opCLI() { c.tick(); c.P &^= flagI }
func (c *CPU) opCLV() { c.tick(); c.P &^= flagV }
func (c *CPU) opSEC() { c.tick(); c.P |= flagC }
func (c *CPU) opSED() { c.tick(); c.P |= flagD }
func (c *CPU) opSEI() { c.tick(); c.P |= flagI }

func (c *CPU) compare(r byte) {
	c.loadDummy()
	v := c.read(c.operand)
	c.P &^= flagC | flagZ | flagN
	if r >= v {
		c.P |= flagC
	}
	c.setZN(r - v)
}
func (c *CPU) opCMP() { c.compare(c.A) }
func (c *CPU) opCPX() { c.compare(c.X) }
func (c *CPU) opCPY() { c.compare(c.Y) }

func (c *CPU) opDEC() { c.rmw(func(v byte) byte { return v - 1 }) }
func (c *CPU) opINC() { c.rmw(func(v byte) byte { return v + 1 }) }
func (c *CPU) opDEX() { c.tick(); c.X--; c.setZN(c.X) }
func (c *CPU) opDEY() { c.tick(); c.Y--; c.setZN(c.Y) }
func (c *CPU) opINX() { c.tick(); c.X++; c.setZN(c.X) }
func (c *CPU) opINY() { c.tick(); c.Y++; c.setZN(c.Y) }

func (c *CPU) opJMP() { c.PC = c.operand }

// JSR: T1 op (already done), T2 read addr lo (in fetchOperand amABS),
// T3 internal (S register juggle on real hw), T4 push PCH, T5 push PCL,
// T6 read addr hi (already done in fetchOperand). My fetchOperand reads
// both addr bytes up front (which absorbs T2 and T6 bus accesses), so
// here we model only the T3 internal and the two pushes (T4, T5).
func (c *CPU) opJSR() {
	c.tick() // T3 internal
	c.push16(c.PC - 1)
	c.PC = c.operand
}

// RTS: T1 op, T2 dummy read PC, T3 increment SP (internal), T4 pop PCL,
// T5 pop PCH, T6 increment PC (internal).
func (c *CPU) opRTS() {
	c.tick() // T2 dummy
	c.tick() // T3 internal
	c.PC = c.pop16()
	c.tick() // T6 internal
	c.PC++
}

// RTI: T1 op, T2 dummy read, T3 SP++ (internal), T4 pop P, T5 pop PCL,
// T6 pop PCH.
func (c *CPU) opRTI() {
	c.tick() // T2 dummy
	c.tick() // T3 internal
	c.P = (c.pop() &^ flagB) | flagU
	c.PC = c.pop16()
}

func (c *CPU) opLDA() { c.loadDummy(); c.A = c.read(c.operand); c.setZN(c.A) }
func (c *CPU) opLDX() { c.loadDummy(); c.X = c.read(c.operand); c.setZN(c.X) }
func (c *CPU) opLDY() { c.loadDummy(); c.Y = c.read(c.operand); c.setZN(c.Y) }

// STA on indexed/indirect modes always does an extra dummy read at the
// uncorrected address before the real write — so total cycles include the
// "page-cross" cycle even when there's no actual page cross.
func (c *CPU) storeDummy() {
	switch c.addrMode {
	case amABSX, amABSY, amIZY:
		c.tick()
	}
}

// Read-side page-cross dummy: for indexed addressing modes, real hardware
// does a dummy read at the uncorrected address only when a page boundary
// is crossed. Add 1 internal cycle to model that.
func (c *CPU) loadDummy() {
	if !c.pageCross {
		return
	}
	switch c.addrMode {
	case amABSX, amABSY, amIZY:
		c.tick()
	}
}
func (c *CPU) opSTA() { c.storeDummy(); c.write(c.operand, c.A) }
func (c *CPU) opSTX() { c.storeDummy(); c.write(c.operand, c.X) }
func (c *CPU) opSTY() { c.storeDummy(); c.write(c.operand, c.Y) }

func (c *CPU) opNOP() {
	// Implied/relative NOPs are 2 cy; addressed NOPs do their phantom read
	// (with optional page-cross dummy).
	switch c.addrMode {
	case amIMP, amACC:
		c.tick()
	case amIMM:
		// 2 cy total; opcode + operand read already done
	default:
		c.loadDummy()
		c.read(c.operand)
	}
}

// PHA/PHP: T1 op, T2 internal/dummy, T3 push.
func (c *CPU) opPHA() { c.tick(); c.push(c.A) }
func (c *CPU) opPHP() { c.tick(); c.push(c.P | flagB | flagU) }

// PLA/PLP: T1 op, T2 internal, T3 SP++ (internal), T4 pop.
func (c *CPU) opPLA() { c.tick(); c.tick(); c.A = c.pop(); c.setZN(c.A) }
func (c *CPU) opPLP() { c.tick(); c.tick(); c.P = (c.pop() &^ flagB) | flagU }

// Transfer ops: T1 op, T2 internal (ALU). My read of the opcode is the
// only bus access; pad 1 internal cycle here.
func (c *CPU) opTAX() { c.tick(); c.X = c.A; c.setZN(c.X) }
func (c *CPU) opTAY() { c.tick(); c.Y = c.A; c.setZN(c.Y) }
func (c *CPU) opTSX() { c.tick(); c.X = c.S; c.setZN(c.X) }
func (c *CPU) opTXA() { c.tick(); c.A = c.X; c.setZN(c.A) }
func (c *CPU) opTXS() { c.tick(); c.S = c.X }
func (c *CPU) opTYA() { c.tick(); c.A = c.Y; c.setZN(c.A) }

// Illegals
func (c *CPU) opLAX() {
	c.loadDummy()
	v := c.read(c.operand)
	c.A = v
	c.X = v
	c.setZN(v)
}
func (c *CPU) opSAX() { c.write(c.operand, c.A&c.X) }
// Illegal RMW: read, dummy write old, write modified, then ALU side-effect.
// Indexed modes pay the same uncorrected-dummy-read cycle as legal RMWs.
func (c *CPU) rmwSide(modify func(byte) byte, side func(byte)) {
	switch c.addrMode {
	case amABSX, amABSY, amIZY:
		c.tick()
	}
	v := c.read(c.operand)
	c.write(c.operand, v)
	v = modify(v)
	c.write(c.operand, v)
	side(v)
}

func (c *CPU) opDCP() {
	c.rmwSide(
		func(v byte) byte { return v - 1 },
		func(v byte) {
			c.P &^= flagC | flagZ | flagN
			if c.A >= v {
				c.P |= flagC
			}
			c.setZN(c.A - v)
		})
}
func (c *CPU) opISB() {
	c.rmwSide(
		func(v byte) byte { return v + 1 },
		func(v byte) { c.adc(^v) })
}
func (c *CPU) opSLO() {
	c.rmwSide(
		func(v byte) byte {
			c.P &^= flagC
			if v&0x80 != 0 {
				c.P |= flagC
			}
			return v << 1
		},
		func(v byte) { c.A |= v; c.setZN(c.A) })
}
func (c *CPU) opRLA() {
	c.rmwSide(
		func(v byte) byte {
			oldC := byte(0)
			if c.P&flagC != 0 {
				oldC = 1
			}
			c.P &^= flagC
			if v&0x80 != 0 {
				c.P |= flagC
			}
			return (v << 1) | oldC
		},
		func(v byte) { c.A &= v; c.setZN(c.A) })
}
func (c *CPU) opSRE() {
	c.rmwSide(
		func(v byte) byte {
			c.P &^= flagC
			if v&1 != 0 {
				c.P |= flagC
			}
			return v >> 1
		},
		func(v byte) { c.A ^= v; c.setZN(c.A) })
}
func (c *CPU) opRRA() {
	c.rmwSide(
		func(v byte) byte {
			oldC := byte(0)
			if c.P&flagC != 0 {
				oldC = 0x80
			}
			c.P &^= flagC
			if v&1 != 0 {
				c.P |= flagC
			}
			return (v >> 1) | oldC
		},
		func(v byte) { c.adc(v) })
}

// Debug format (nestest-ish)
func (c *CPU) String() string {
	return fmt.Sprintf("A:%02X X:%02X Y:%02X P:%02X SP:%02X CYC:%d PC:%04X",
		c.A, c.X, c.Y, c.P, c.S, c.cycles, c.PC)
}
