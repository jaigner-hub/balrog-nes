package main

// nestest automation trace. Loads the given NES ROM, forces PC=$C000
// (nestest automation entry), runs ~9000 instructions, and writes a
// trace in the format used by nestest.log — one line per instruction,
// showing opcode bytes, disassembly, register state, PPU position, and
// cumulative CPU cycles.

import (
	"bufio"
	"fmt"
	"log"
	"os"
)

func runNestestTrace(romPath, outPath string) {
	cart, err := LoadCart(romPath)
	if err != nil {
		log.Fatalf("load rom: %v", err)
	}
	fmt.Fprintf(os.Stderr, "cart: mapper=%d PRG=%d CHR=%d first bytes: %02X %02X %02X\n",
		cart.MapperID, len(cart.PRG), len(cart.CHR), cart.PRG[0], cart.PRG[1], cart.PRG[2])
	nes := NewNES(cart, 44100)
	// nestest automation entry. Reference nestest.log has CYC:7 + PPU at
	// (0, 21) at the first logged instruction ($C000). That represents
	// 7 CPU cycles of reset-like startup with the PPU already running
	// into visible scanline 0. Match that by clearing the PPU to scanline
	// 0 cycle 0 and hand-ticking 21 PPU cycles before starting the trace.
	nes.CPU.PC = 0xC000
	nes.CPU.P = 0x24
	nes.CPU.S = 0xFD
	nes.CPU.cycles = 7
	nes.PPU.scanline = 0
	nes.PPU.cycle = 0
	nes.PPU.frame = 0
	nes.PPU.odd = false
	for i := 0; i < 21; i++ {
		nes.PPU.Step()
	}
	// Disable tickFn's IRQ/NMI polling — nestest doesn't need them and
	// they'd interfere with the deterministic trace.
	nes.CPU.tickFn = func() {
		nes.APU.Step()
		nes.PPU.Step()
		nes.PPU.Step()
		nes.PPU.Step()
	}

	f, err := os.Create(outPath)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	for i := 0; i < 9000; i++ {
		pc := nes.CPU.PC
		op := nes.Bus.Read(pc)
		info := opTable[op]
		var b1, b2 byte
		if instrLen(info.mode) >= 2 {
			b1 = nes.Bus.Read(pc + 1)
		}
		if instrLen(info.mode) >= 3 {
			b2 = nes.Bus.Read(pc + 2)
		}
		disasm := disassembleWithValues(nes, info, op, pc, b1, b2)
		// Illegal / undocumented opcodes are prefixed with '*' in
		// nestest.log. The legal set is the 151 original 6502 opcodes;
		// everything else is flagged here.
		prefix := " "
		if isIllegalOpcode(op) {
			prefix = "*"
		}
		bytesStr := fmt.Sprintf("%02X", op)
		switch instrLen(info.mode) {
		case 2:
			bytesStr = fmt.Sprintf("%02X %02X", op, b1)
		case 3:
			bytesStr = fmt.Sprintf("%02X %02X %02X", op, b1, b2)
		}
		fmt.Fprintf(w, "%04X  %-8s %s%-31s A:%02X X:%02X Y:%02X P:%02X SP:%02X PPU:%3d,%3d CYC:%d\n",
			pc, bytesStr, prefix, disasm,
			nes.CPU.A, nes.CPU.X, nes.CPU.Y, nes.CPU.P, nes.CPU.S,
			nes.PPU.scanline, nes.PPU.cycle, nes.CPU.cycles)
		nes.CPU.Step()
		// Stop if we hit the nestest "done" vector or wrap to reset vec
		if nes.CPU.PC == 0xC66E || nes.CPU.PC == 0x0000 {
			break
		}
	}
}

// isIllegalOpcode returns true for the undocumented 6502 opcodes. Used
// to prefix the disassembly with '*' in nestest.log style.
func isIllegalOpcode(op byte) bool {
	switch op {
	// Multi-byte NOPs (DOP / TOP)
	case 0x04, 0x14, 0x34, 0x44, 0x54, 0x64, 0x74,
		0x80, 0x82, 0x89, 0xC2, 0xE2, 0xD4, 0xF4,
		0x0C, 0x1C, 0x3C, 0x5C, 0x7C, 0xDC, 0xFC:
		return true
	// Single-byte NOPs (unofficial)
	case 0x1A, 0x3A, 0x5A, 0x7A, 0xDA, 0xFA:
		return true
	// Undoc SBC
	case 0xEB:
		return true
	// LAX
	case 0xA3, 0xA7, 0xAF, 0xB3, 0xB7, 0xBF:
		return true
	// SAX
	case 0x83, 0x87, 0x8F, 0x97:
		return true
	// DCP
	case 0xC3, 0xC7, 0xCF, 0xD3, 0xD7, 0xDB, 0xDF:
		return true
	// ISB / ISC
	case 0xE3, 0xE7, 0xEF, 0xF3, 0xF7, 0xFB, 0xFF:
		return true
	// SLO
	case 0x03, 0x07, 0x0F, 0x13, 0x17, 0x1B, 0x1F:
		return true
	// RLA
	case 0x23, 0x27, 0x2F, 0x33, 0x37, 0x3B, 0x3F:
		return true
	// SRE
	case 0x43, 0x47, 0x4F, 0x53, 0x57, 0x5B, 0x5F:
		return true
	// RRA
	case 0x63, 0x67, 0x6F, 0x73, 0x77, 0x7B, 0x7F:
		return true
	}
	return false
}

func instrLen(mode amode) int {
	switch mode {
	case amIMP, amACC:
		return 1
	case amIMM, amZP, amZPX, amZPY, amIZX, amIZY, amREL:
		return 2
	case amABS, amABSX, amABSY, amIND:
		return 3
	}
	return 1
}

// disassembleWithValues formats an instruction in nestest.log's exact
// style, including the "= XX" memory-value annotations and the "@ XXXX
// = XX" indirect annotations.
func disassembleWithValues(nes *NES, info opInfo, op byte, pc uint16, b1, b2 byte) string {
	name := info.name
	// JMP/JSR don't show memory values for their target — they just jump.
	isJump := op == 0x4C || op == 0x20 // JMP abs, JSR abs
	isJmpInd := op == 0x6C             // JMP (ind) — has a special "@" form

	readAt := func(addr uint16) byte { return nes.Bus.Read(addr) }

	switch info.mode {
	case amIMP:
		return name
	case amACC:
		return name + " A"
	case amIMM:
		return fmt.Sprintf("%s #$%02X", name, b1)
	case amZP:
		if isJump {
			return fmt.Sprintf("%s $%02X", name, b1)
		}
		return fmt.Sprintf("%s $%02X = %02X", name, b1, readAt(uint16(b1)))
	case amZPX:
		ea := uint16((b1 + nes.CPU.X) & 0xFF)
		return fmt.Sprintf("%s $%02X,X @ %02X = %02X", name, b1, ea, readAt(ea))
	case amZPY:
		ea := uint16((b1 + nes.CPU.Y) & 0xFF)
		return fmt.Sprintf("%s $%02X,Y @ %02X = %02X", name, b1, ea, readAt(ea))
	case amREL:
		tgt := uint16(int32(pc+2) + int32(int8(b1)))
		return fmt.Sprintf("%s $%04X", name, tgt)
	case amABS:
		addr := uint16(b1) | (uint16(b2) << 8)
		if isJump {
			return fmt.Sprintf("%s $%04X", name, addr)
		}
		return fmt.Sprintf("%s $%04X = %02X", name, addr, readAt(addr))
	case amABSX:
		base := uint16(b1) | (uint16(b2) << 8)
		ea := base + uint16(nes.CPU.X)
		return fmt.Sprintf("%s $%04X,X @ %04X = %02X", name, base, ea, readAt(ea))
	case amABSY:
		base := uint16(b1) | (uint16(b2) << 8)
		ea := base + uint16(nes.CPU.Y)
		return fmt.Sprintf("%s $%04X,Y @ %04X = %02X", name, base, ea, readAt(ea))
	case amIND:
		ptr := uint16(b1) | (uint16(b2) << 8)
		// JMP (ind) emulates the 6502 indirect-JMP bug in its address
		// computation, but for trace purposes nestest just shows the raw
		// pointer and the real-hw-correct target.
		lo := uint16(readAt(ptr))
		hi := uint16(readAt((ptr & 0xFF00) | ((ptr + 1) & 0x00FF)))
		tgt := lo | (hi << 8)
		_ = isJmpInd
		return fmt.Sprintf("%s ($%04X) = %04X", name, ptr, tgt)
	case amIZX:
		zp := b1 + nes.CPU.X
		lo := uint16(readAt(uint16(zp)))
		hi := uint16(readAt(uint16(zp + 1)))
		ea := lo | (hi << 8)
		return fmt.Sprintf("%s ($%02X,X) @ %02X = %04X = %02X", name, b1, zp, ea, readAt(ea))
	case amIZY:
		lo := uint16(readAt(uint16(b1)))
		hi := uint16(readAt(uint16(b1 + 1)))
		base := lo | (hi << 8)
		ea := base + uint16(nes.CPU.Y)
		return fmt.Sprintf("%s ($%02X),Y = %04X @ %04X = %02X", name, b1, base, ea, readAt(ea))
	}
	return name
}

func disassemble(info opInfo, pc uint16, b1, b2 byte) string {
	name := info.name
	switch info.mode {
	case amIMP:
		return name
	case amACC:
		return name + " A"
	case amIMM:
		return fmt.Sprintf("%s #$%02X", name, b1)
	case amZP:
		return fmt.Sprintf("%s $%02X", name, b1)
	case amZPX:
		return fmt.Sprintf("%s $%02X,X", name, b1)
	case amZPY:
		return fmt.Sprintf("%s $%02X,Y", name, b1)
	case amREL:
		tgt := uint16(int32(pc+2) + int32(int8(b1)))
		return fmt.Sprintf("%s $%04X", name, tgt)
	case amABS:
		return fmt.Sprintf("%s $%02X%02X", name, b2, b1)
	case amABSX:
		return fmt.Sprintf("%s $%02X%02X,X", name, b2, b1)
	case amABSY:
		return fmt.Sprintf("%s $%02X%02X,Y", name, b2, b1)
	case amIND:
		return fmt.Sprintf("%s ($%02X%02X)", name, b2, b1)
	case amIZX:
		return fmt.Sprintf("%s ($%02X,X)", name, b1)
	case amIZY:
		return fmt.Sprintf("%s ($%02X),Y", name, b1)
	}
	return name
}
