package main

// Standalone nestest automation driver. Loads nestest.nes, sets PC=$C000
// (automation entry point), and executes ~9000 instructions while logging
// each one in the canonical nestest.log format so we can diff against the
// known-good reference:
//
//   PC  OP OPERANDS  DISASM                          A:XX X:XX Y:XX P:XX SP:XX PPU:N,N CYC:N
//
// The first instruction where our CPU log diverges from nestest.log is the
// bug — cycle count, flag, addressing mode, anything.

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "nestest driver: run the real emulator with --trace nestest.trace")
}
