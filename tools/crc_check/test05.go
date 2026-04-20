//go:build ignore

package main

import (
	"fmt"
	"hash/crc32"
)

func crc32Custom(data []byte) uint32 {
	// Blargg uses init=0, poly=EDB88320, no final XOR.
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

func crc32Go(data []byte) uint32 {
	// Go's IEEE CRC uses same polynomial but init=$FFFFFFFF and final XOR=$FFFFFFFF.
	table := crc32.MakeTable(crc32.IEEE)
	return crc32.Checksum(data, table)
}

func main() {
	data := []byte{
		0x00, 0x04, 0x01, 0x04, 0x02, 0x04,
		0x03, 0x03, 0x04, 0x03, 0x05, 0x03,
		0x06, 0x03, 0x07, 0x03, 0x08, 0x03,
		0x09, 0x02,
	}
	fmt.Printf("custom   : %08X  (expected A6CCB10A)\n", crc32Custom(data))
	fmt.Printf("go IEEE  : %08X\n", crc32Go(data))
	fmt.Printf("go IEEE^F: %08X\n", crc32Go(data)^0xFFFFFFFF)
}
