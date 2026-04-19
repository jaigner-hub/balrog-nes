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
	lo := uint16(c.Bus.Read(addr))
	hi := uint16(c.Bus.Read(addr + 1))
	return lo | (hi << 8)
}

// 6502 indirect JMP bug + zero-page wrap helper
func (c *CPU) read16bug(addr uint16) uint16 {
	lo := uint16(c.Bus.Read(addr))
	hi := uint16(c.Bus.Read((addr & 0xFF00) | ((addr + 1) & 0x00FF)))
	return lo | (hi << 8)
}

func (c *CPU) push(v byte) {
	c.Bus.Write(0x0100|uint16(c.S), v)
	c.S--
}

func (c *CPU) pop() byte {
	c.S++
	return c.Bus.Read(0x0100 | uint16(c.S))
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
		c.operand = uint16(c.Bus.Read(c.PC))
		c.PC++
	case amZPX:
		c.operand = uint16((c.Bus.Read(c.PC) + c.X) & 0xFF)
		c.PC++
	case amZPY:
		c.operand = uint16((c.Bus.Read(c.PC) + c.Y) & 0xFF)
		c.PC++
	case amREL:
		off := int8(c.Bus.Read(c.PC))
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
		zp := c.Bus.Read(c.PC) + c.X
		c.PC++
		lo := uint16(c.Bus.Read(uint16(zp)))
		hi := uint16(c.Bus.Read(uint16(zp + 1)))
		c.operand = lo | (hi << 8)
	case amIZY:
		zp := c.Bus.Read(c.PC)
		c.PC++
		lo := uint16(c.Bus.Read(uint16(zp)))
		hi := uint16(c.Bus.Read(uint16(zp + 1)))
		base := lo | (hi << 8)
		c.operand = base + uint16(c.Y)
		c.pageCross = pagesDiffer(base, c.operand)
	}
}

func (c *CPU) NMI() { c.nmiPend = true }
func (c *CPU) IRQ() { c.irqPend = true }

func (c *CPU) doNMI() {
	c.push16(c.PC)
	c.push((c.P | flagU) &^ flagB)
	c.P |= flagI
	c.PC = c.read16(0xFFFA)
	c.cycles += 7
}

func (c *CPU) doIRQ() {
	c.push16(c.PC)
	c.push((c.P | flagU) &^ flagB)
	c.P |= flagI
	c.PC = c.read16(0xFFFE)
	c.cycles += 7
}

func (c *CPU) Step() int {
	startCycles := c.cycles
	if c.stall > 0 {
		c.stall--
		c.cycles++
		return 1
	}
	if c.nmiPend {
		c.nmiPend = false
		c.doNMI()
		return 7
	}
	if c.irqPend && c.P&flagI == 0 {
		c.irqPend = false
		c.doIRQ()
		return 7
	}
	op := c.Bus.Read(c.PC)
	c.PC++
	info := opTable[op]
	c.addrMode = info.mode
	c.fetchOperand(info.mode)
	info.fn(c)
	c.cycles += uint64(info.cycles)
	// Page-cross penalty for reads
	if c.pageCross {
		switch op {
		case 0x7D, 0x79, 0x71, 0x3D, 0x39, 0x31,
			0xDD, 0xD9, 0xD1, 0x5D, 0x59, 0x51,
			0xBD, 0xB9, 0xB1, 0xBE, 0xBC, 0xBF,
			0x1D, 0x19, 0x11, 0xFD, 0xF9, 0xF1,
			0x1C, 0x3C, 0x5C, 0x7C, 0xDC, 0xFC:
			c.cycles++
		}
	}
	return int(c.cycles - startCycles)
}

// --- operand access helpers (for instructions) ---
func (c *CPU) loadOperand() byte {
	if c.addrMode == amACC {
		return c.A
	}
	return c.Bus.Read(c.operand)
}
func (c *CPU) storeOperand(v byte) {
	if c.addrMode == amACC {
		c.A = v
	} else {
		c.Bus.Write(c.operand, v)
	}
}

// --- opcodes ---
func (c *CPU) opADC() { c.adc(c.Bus.Read(c.operand)) }
func (c *CPU) opSBC() { c.adc(^c.Bus.Read(c.operand)) }
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

func (c *CPU) opAND() { c.A &= c.Bus.Read(c.operand); c.setZN(c.A) }
func (c *CPU) opORA() { c.A |= c.Bus.Read(c.operand); c.setZN(c.A) }
func (c *CPU) opEOR() { c.A ^= c.Bus.Read(c.operand); c.setZN(c.A) }

func (c *CPU) opASL() {
	v := c.loadOperand()
	c.P &^= flagC
	if v&0x80 != 0 {
		c.P |= flagC
	}
	v <<= 1
	c.storeOperand(v)
	c.setZN(v)
}
func (c *CPU) opLSR() {
	v := c.loadOperand()
	c.P &^= flagC
	if v&1 != 0 {
		c.P |= flagC
	}
	v >>= 1
	c.storeOperand(v)
	c.setZN(v)
}
func (c *CPU) opROL() {
	v := c.loadOperand()
	oldC := byte(0)
	if c.P&flagC != 0 {
		oldC = 1
	}
	c.P &^= flagC
	if v&0x80 != 0 {
		c.P |= flagC
	}
	v = (v << 1) | oldC
	c.storeOperand(v)
	c.setZN(v)
}
func (c *CPU) opROR() {
	v := c.loadOperand()
	oldC := byte(0)
	if c.P&flagC != 0 {
		oldC = 0x80
	}
	c.P &^= flagC
	if v&1 != 0 {
		c.P |= flagC
	}
	v = (v >> 1) | oldC
	c.storeOperand(v)
	c.setZN(v)
}

func (c *CPU) branch(cond bool) {
	if cond {
		c.cycles++
		if pagesDiffer(c.PC, c.operand) {
			c.cycles++
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
	v := c.Bus.Read(c.operand)
	c.P &^= flagZ | flagN | flagV
	if c.A&v == 0 {
		c.P |= flagZ
	}
	c.P |= v & (flagN | flagV)
}

func (c *CPU) opBRK() {
	c.PC++
	c.push16(c.PC)
	c.push(c.P | flagB | flagU)
	c.P |= flagI
	c.PC = c.read16(0xFFFE)
}

func (c *CPU) opCLC() { c.P &^= flagC }
func (c *CPU) opCLD() { c.P &^= flagD }
func (c *CPU) opCLI() { c.P &^= flagI }
func (c *CPU) opCLV() { c.P &^= flagV }
func (c *CPU) opSEC() { c.P |= flagC }
func (c *CPU) opSED() { c.P |= flagD }
func (c *CPU) opSEI() { c.P |= flagI }

func (c *CPU) compare(r byte) {
	v := c.Bus.Read(c.operand)
	c.P &^= flagC | flagZ | flagN
	if r >= v {
		c.P |= flagC
	}
	c.setZN(r - v)
	// setZN cleared Z/N already above inside; just keep C as set
}
func (c *CPU) opCMP() { c.compare(c.A) }
func (c *CPU) opCPX() { c.compare(c.X) }
func (c *CPU) opCPY() { c.compare(c.Y) }

func (c *CPU) opDEC() {
	v := c.Bus.Read(c.operand) - 1
	c.Bus.Write(c.operand, v)
	c.setZN(v)
}
func (c *CPU) opDEX() { c.X--; c.setZN(c.X) }
func (c *CPU) opDEY() { c.Y--; c.setZN(c.Y) }
func (c *CPU) opINC() {
	v := c.Bus.Read(c.operand) + 1
	c.Bus.Write(c.operand, v)
	c.setZN(v)
}
func (c *CPU) opINX() { c.X++; c.setZN(c.X) }
func (c *CPU) opINY() { c.Y++; c.setZN(c.Y) }

func (c *CPU) opJMP() { c.PC = c.operand }
func (c *CPU) opJSR() { c.push16(c.PC - 1); c.PC = c.operand }
func (c *CPU) opRTS() { c.PC = c.pop16() + 1 }
func (c *CPU) opRTI() {
	c.P = (c.pop() &^ flagB) | flagU
	c.PC = c.pop16()
}

func (c *CPU) opLDA() { c.A = c.Bus.Read(c.operand); c.setZN(c.A) }
func (c *CPU) opLDX() { c.X = c.Bus.Read(c.operand); c.setZN(c.X) }
func (c *CPU) opLDY() { c.Y = c.Bus.Read(c.operand); c.setZN(c.Y) }
func (c *CPU) opSTA() { c.Bus.Write(c.operand, c.A) }
func (c *CPU) opSTX() { c.Bus.Write(c.operand, c.X) }
func (c *CPU) opSTY() { c.Bus.Write(c.operand, c.Y) }

func (c *CPU) opNOP() {}

func (c *CPU) opPHA() { c.push(c.A) }
func (c *CPU) opPLA() { c.A = c.pop(); c.setZN(c.A) }
func (c *CPU) opPHP() { c.push(c.P | flagB | flagU) }
func (c *CPU) opPLP() { c.P = (c.pop() &^ flagB) | flagU }

func (c *CPU) opTAX() { c.X = c.A; c.setZN(c.X) }
func (c *CPU) opTAY() { c.Y = c.A; c.setZN(c.Y) }
func (c *CPU) opTSX() { c.X = c.S; c.setZN(c.X) }
func (c *CPU) opTXA() { c.A = c.X; c.setZN(c.A) }
func (c *CPU) opTXS() { c.S = c.X }
func (c *CPU) opTYA() { c.A = c.Y; c.setZN(c.A) }

// Illegals
func (c *CPU) opLAX() {
	v := c.Bus.Read(c.operand)
	c.A = v
	c.X = v
	c.setZN(v)
}
func (c *CPU) opSAX() { c.Bus.Write(c.operand, c.A&c.X) }
func (c *CPU) opDCP() {
	v := c.Bus.Read(c.operand) - 1
	c.Bus.Write(c.operand, v)
	c.P &^= flagC | flagZ | flagN
	if c.A >= v {
		c.P |= flagC
	}
	c.setZN(c.A - v)
}
func (c *CPU) opISB() {
	v := c.Bus.Read(c.operand) + 1
	c.Bus.Write(c.operand, v)
	c.adc(^v)
}
func (c *CPU) opSLO() {
	v := c.Bus.Read(c.operand)
	c.P &^= flagC
	if v&0x80 != 0 {
		c.P |= flagC
	}
	v <<= 1
	c.Bus.Write(c.operand, v)
	c.A |= v
	c.setZN(c.A)
}
func (c *CPU) opRLA() {
	v := c.Bus.Read(c.operand)
	oldC := byte(0)
	if c.P&flagC != 0 {
		oldC = 1
	}
	c.P &^= flagC
	if v&0x80 != 0 {
		c.P |= flagC
	}
	v = (v << 1) | oldC
	c.Bus.Write(c.operand, v)
	c.A &= v
	c.setZN(c.A)
}
func (c *CPU) opSRE() {
	v := c.Bus.Read(c.operand)
	c.P &^= flagC
	if v&1 != 0 {
		c.P |= flagC
	}
	v >>= 1
	c.Bus.Write(c.operand, v)
	c.A ^= v
	c.setZN(c.A)
}
func (c *CPU) opRRA() {
	v := c.Bus.Read(c.operand)
	oldC := byte(0)
	if c.P&flagC != 0 {
		oldC = 0x80
	}
	c.P &^= flagC
	if v&1 != 0 {
		c.P |= flagC
	}
	v = (v >> 1) | oldC
	c.Bus.Write(c.operand, v)
	c.adc(v)
}

// Debug format (nestest-ish)
func (c *CPU) String() string {
	return fmt.Sprintf("A:%02X X:%02X Y:%02X P:%02X SP:%02X CYC:%d PC:%04X",
		c.A, c.X, c.Y, c.P, c.S, c.cycles, c.PC)
}
