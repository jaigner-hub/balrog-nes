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
	// nestest automation entry
	nes.CPU.PC = 0xC000
	nes.CPU.P = 0x24
	nes.CPU.S = 0xFD
	nes.CPU.cycles = 7
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
		disasm := disassemble(info, pc, b1, b2)
		bytesStr := fmt.Sprintf("%02X", op)
		switch instrLen(info.mode) {
		case 2:
			bytesStr = fmt.Sprintf("%02X %02X", op, b1)
		case 3:
			bytesStr = fmt.Sprintf("%02X %02X %02X", op, b1, b2)
		}
		fmt.Fprintf(w, "%04X  %-8s  %-32s A:%02X X:%02X Y:%02X P:%02X SP:%02X PPU:%3d,%3d CYC:%d\n",
			pc, bytesStr, disasm,
			nes.CPU.A, nes.CPU.X, nes.CPU.Y, nes.CPU.P, nes.CPU.S,
			nes.PPU.scanline, nes.PPU.cycle, nes.CPU.cycles)
		nes.CPU.Step()
		// Stop if we hit the nestest "done" vector or wrap to reset vec
		if nes.CPU.PC == 0xC66E || nes.CPU.PC == 0x0000 {
			break
		}
	}
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
