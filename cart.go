package main

import (
	"errors"
	"fmt"
	"os"
)

type Mirroring int

const (
	MirrorHorizontal Mirroring = iota
	MirrorVertical
	MirrorSingle0
	MirrorSingle1
	MirrorFourScreen
)

type Mapper interface {
	ReadPRG(addr uint16) byte
	WritePRG(addr uint16, v byte)
	ReadCHR(addr uint16) byte
	WriteCHR(addr uint16, v byte)
	Mirror() Mirroring
}

type Cart struct {
	PRG        []byte
	CHR        []byte
	MapperID   byte
	initMirror Mirroring
	HasCHRRAM  bool
	mapper     Mapper
}

func LoadCart(path string) (*Cart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 16 || string(data[0:4]) != "NES\x1A" {
		return nil, errors.New("not an iNES file")
	}
	prgSize := int(data[4]) * 16 * 1024
	chrSize := int(data[5]) * 8 * 1024
	flags6 := data[6]
	flags7 := data[7]
	mapperID := (flags6 >> 4) | (flags7 & 0xF0)

	var mirror Mirroring
	switch {
	case flags6&0x08 != 0:
		mirror = MirrorFourScreen
	case flags6&0x01 != 0:
		mirror = MirrorVertical
	default:
		mirror = MirrorHorizontal
	}

	offset := 16
	if flags6&0x04 != 0 {
		offset += 512
	}
	if offset+prgSize > len(data) {
		return nil, fmt.Errorf("PRG truncated: need %d have %d", offset+prgSize, len(data))
	}
	prg := make([]byte, prgSize)
	copy(prg, data[offset:offset+prgSize])

	var chr []byte
	hasCHRRAM := false
	if chrSize == 0 {
		chr = make([]byte, 8*1024)
		hasCHRRAM = true
	} else {
		if offset+prgSize+chrSize > len(data) {
			return nil, fmt.Errorf("CHR truncated")
		}
		chr = make([]byte, chrSize)
		copy(chr, data[offset+prgSize:offset+prgSize+chrSize])
	}

	c := &Cart{
		PRG: prg, CHR: chr,
		MapperID: mapperID, initMirror: mirror, HasCHRRAM: hasCHRRAM,
	}
	switch mapperID {
	case 0:
		c.mapper = newMapper0(c)
	case 1:
		c.mapper = newMapper1(c)
	default:
		return nil, fmt.Errorf("unsupported mapper %d", mapperID)
	}
	return c, nil
}

func (c *Cart) ReadPRG(addr uint16) byte      { return c.mapper.ReadPRG(addr) }
func (c *Cart) WritePRG(addr uint16, v byte)  { c.mapper.WritePRG(addr, v) }
func (c *Cart) ReadCHR(addr uint16) byte      { return c.mapper.ReadCHR(addr) }
func (c *Cart) WriteCHR(addr uint16, v byte)  { c.mapper.WriteCHR(addr, v) }
func (c *Cart) MirrorMode() Mirroring         { return c.mapper.Mirror() }
