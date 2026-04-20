// Computes the CRC used by blargg's test ROMs (mirrors crc.s).
// Init = 0, no final XOR, polynomial = $EDB88320 (reversed CRC-32).
package main

import (
	"fmt"
	"os"
)

func crc32Blargg(data []byte) uint32 {
	var crc uint32 = 0
	for _, b := range data {
		crc ^= uint32(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "-hex" {
		// Accept space-separated hex bytes on remaining args
		out := make([]byte, 0)
		for _, arg := range os.Args[2:] {
			var b byte
			_, err := fmt.Sscanf(arg, "%x", &b)
			if err != nil {
				fmt.Fprintf(os.Stderr, "bad hex: %q\n", arg)
				os.Exit(1)
			}
			out = append(out, b)
		}
		fmt.Printf("%08X\n", crc32Blargg(out))
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: crc_check <string-or-hex-bytes>")
		fmt.Fprintln(os.Stderr, "  hex-bytes mode: use \\xNN escapes for each byte")
		fmt.Fprintln(os.Stderr, "  or: crc_check -hex <b1> <b2> ...")
		os.Exit(1)
	}
	s := os.Args[1]
	// Accept \xNN hex escapes for raw bytes (print_hex/print_dec CRC the
	// numeric byte, not the displayed digits). Everything else passes
	// through literally except \n for newline and \\ for backslash.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				out = append(out, 10)
				i++
				continue
			case '\\':
				out = append(out, '\\')
				i++
				continue
			case 'x':
				if i+3 < len(s) {
					var b byte
					_, err := fmt.Sscanf(s[i+2:i+4], "%02x", &b)
					if err == nil {
						out = append(out, b)
						i += 3
						continue
					}
				}
			}
		}
		out = append(out, s[i])
	}
	fmt.Printf("%08X\n", crc32Blargg(out))
}
