package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
)

// Save state format. Bumped if the layout changes incompatibly.
const stateVersion = 2 // bumped when DMC was added

type SaveState struct {
	Version int

	// CPU
	A, X, Y, S, P byte
	PC            uint16
	CPUCycles     uint64
	Stall         int
	NMIPend       bool
	IRQPend       bool

	// PPU registers + internal state
	PPUCtrl, PPUMask, PPUStatus, PPUOAMAddr byte
	PPUBusLat, PPUDataBuf                   byte
	PPUv, PPUt                              uint16
	PPUx, PPUw                              byte
	PPUNT                                   [2][1024]byte
	PPUPalette                              [32]byte
	PPUOAM                                  [256]byte
	PPUScanline                             int
	PPUCycle                                int
	PPUFrame                                uint64
	PPUOdd                                  bool
	PPUNMIPending                           bool

	// Bus
	RAM       [2048]byte
	CtrlState [2]ControllerState

	// Cart mutable state
	CHR       []byte // CHR-RAM contents (only meaningful when HasCHRRAM)
	HasCHRRAM bool
	MapperID  byte
	MapperBlob []byte // mapper-specific binary state (PRG-RAM, registers, etc.)

	// APU
	APUBlob []byte
}

type ControllerState struct {
	Buttons byte
	Shift   byte
	Strobe  bool
}

// Mapper interface extension: serialize private state.
// (Defined here so we don't pollute the interface declaration with i/o concerns.)
type stateful interface {
	stateBlob() []byte
	restoreBlob([]byte) error
}

func (n *NES) Snapshot() *SaveState {
	cpu, ppu, bus := n.CPU, n.PPU, n.Bus
	s := &SaveState{
		Version: stateVersion,
		// CPU
		A: cpu.A, X: cpu.X, Y: cpu.Y, S: cpu.S, P: cpu.P, PC: cpu.PC,
		CPUCycles: cpu.cycles, Stall: cpu.stall,
		NMIPend: cpu.nmiPend, IRQPend: cpu.irqPend,
		// PPU
		PPUCtrl: ppu.ctrl, PPUMask: ppu.mask, PPUStatus: ppu.status,
		PPUOAMAddr: ppu.oamAddr, PPUBusLat: ppu.busLat, PPUDataBuf: ppu.dataBuf,
		PPUv: ppu.v, PPUt: ppu.t, PPUx: ppu.x, PPUw: ppu.w,
		PPUNT: ppu.nt, PPUPalette: ppu.palette, PPUOAM: ppu.oam,
		PPUScanline: ppu.scanline, PPUCycle: ppu.cycle,
		PPUFrame: ppu.frame, PPUOdd: ppu.odd, PPUNMIPending: ppu.NMIPending,
		// Bus
		RAM: bus.RAM,
		CtrlState: [2]ControllerState{
			{Buttons: bus.Ctrl[0].Buttons, Shift: bus.Ctrl[0].shift, Strobe: bus.Ctrl[0].strobe},
			{Buttons: bus.Ctrl[1].Buttons, Shift: bus.Ctrl[1].shift, Strobe: bus.Ctrl[1].strobe},
		},
		// Cart
		HasCHRRAM: bus.Cart.HasCHRRAM,
		MapperID:  bus.Cart.MapperID,
	}
	if bus.Cart.HasCHRRAM {
		s.CHR = append([]byte(nil), bus.Cart.CHR...)
	}
	if m, ok := bus.Cart.mapper.(stateful); ok {
		s.MapperBlob = m.stateBlob()
	}
	s.APUBlob = n.APU.snapshot()
	return s
}

func (n *NES) Restore(s *SaveState) error {
	// Older state files decode cleanly into newer structs — gob just zeros
	// the fields that didn't exist yet. Block only *newer* states from
	// loading into an older binary.
	if s.Version > stateVersion {
		return fmt.Errorf("state was saved by a newer build (v%d) than this one (v%d)",
			s.Version, stateVersion)
	}
	if s.MapperID != n.Bus.Cart.MapperID {
		return fmt.Errorf("state was saved with mapper %d, current ROM uses mapper %d",
			s.MapperID, n.Bus.Cart.MapperID)
	}
	cpu, ppu, bus := n.CPU, n.PPU, n.Bus
	cpu.A, cpu.X, cpu.Y, cpu.S, cpu.P, cpu.PC = s.A, s.X, s.Y, s.S, s.P, s.PC
	cpu.cycles, cpu.stall = s.CPUCycles, s.Stall
	cpu.nmiPend, cpu.irqPend = s.NMIPend, s.IRQPend

	ppu.ctrl, ppu.mask, ppu.status = s.PPUCtrl, s.PPUMask, s.PPUStatus
	ppu.oamAddr, ppu.busLat, ppu.dataBuf = s.PPUOAMAddr, s.PPUBusLat, s.PPUDataBuf
	ppu.v, ppu.t, ppu.x, ppu.w = s.PPUv, s.PPUt, s.PPUx, s.PPUw
	ppu.nt, ppu.palette, ppu.oam = s.PPUNT, s.PPUPalette, s.PPUOAM
	ppu.scanline, ppu.cycle = s.PPUScanline, s.PPUCycle
	ppu.frame, ppu.odd, ppu.NMIPending = s.PPUFrame, s.PPUOdd, s.PPUNMIPending

	bus.RAM = s.RAM
	bus.Ctrl[0].Buttons, bus.Ctrl[0].shift, bus.Ctrl[0].strobe =
		s.CtrlState[0].Buttons, s.CtrlState[0].Shift, s.CtrlState[0].Strobe
	bus.Ctrl[1].Buttons, bus.Ctrl[1].shift, bus.Ctrl[1].strobe =
		s.CtrlState[1].Buttons, s.CtrlState[1].Shift, s.CtrlState[1].Strobe

	if s.HasCHRRAM && len(s.CHR) == len(bus.Cart.CHR) {
		copy(bus.Cart.CHR, s.CHR)
	}
	if m, ok := bus.Cart.mapper.(stateful); ok && len(s.MapperBlob) > 0 {
		if err := m.restoreBlob(s.MapperBlob); err != nil {
			return fmt.Errorf("mapper restore: %w", err)
		}
	}
	if err := n.APU.restore(s.APUBlob); err != nil {
		return fmt.Errorf("apu restore: %w", err)
	}
	return nil
}

func WriteStateFile(path string, s *SaveState) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func ReadStateFile(path string) (*SaveState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s SaveState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// --- Mapper state blobs ---

func (m *mapper0) stateBlob() []byte         { return nil }
func (m *mapper0) restoreBlob([]byte) error  { return nil }

type mapper1Blob struct {
	Shift, Count, Ctrl, Chr0, Chr1, Prg byte
	PRGRAM                              []byte
}

func (m *mapper1) stateBlob() []byte {
	b := mapper1Blob{
		Shift: m.shift, Count: m.count, Ctrl: m.ctrl,
		Chr0: m.chr0, Chr1: m.chr1, Prg: m.prg,
		PRGRAM: append([]byte(nil), m.prgRAM[:]...),
	}
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(b)
	return buf.Bytes()
}

func (m *mapper1) restoreBlob(data []byte) error {
	var b mapper1Blob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	m.shift, m.count, m.ctrl = b.Shift, b.Count, b.Ctrl
	m.chr0, m.chr1, m.prg = b.Chr0, b.Chr1, b.Prg
	if len(b.PRGRAM) == len(m.prgRAM) {
		copy(m.prgRAM[:], b.PRGRAM)
	}
	return nil
}

// mapper2 (UxROM): just a PRG bank register.
type mapper2Blob struct{ PrgBank int }

func (m *mapper2) stateBlob() []byte {
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(mapper2Blob{PrgBank: m.prgBank})
	return buf.Bytes()
}
func (m *mapper2) restoreBlob(data []byte) error {
	var b mapper2Blob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	m.prgBank = b.PrgBank
	return nil
}

// mapper7 (AxROM): PRG bank + mirror-select (bit 4).
type mapper7Blob struct {
	Bank   byte
	Mirror int
}

func (m *mapper7) stateBlob() []byte {
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(mapper7Blob{Bank: m.bank, Mirror: int(m.mirror)})
	return buf.Bytes()
}
func (m *mapper7) restoreBlob(data []byte) error {
	var b mapper7Blob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	m.bank = b.Bank
	m.mirror = Mirroring(b.Mirror)
	return nil
}

// mapper3 (CNROM): CHR bank register.
type mapper3Blob struct{ ChrBank int }

func (m *mapper3) stateBlob() []byte {
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(mapper3Blob{ChrBank: m.chrBank})
	return buf.Bytes()
}
func (m *mapper3) restoreBlob(data []byte) error {
	var b mapper3Blob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	m.chrBank = b.ChrBank
	return nil
}

// mapper4 (MMC3): bank registers, mode bits, mirroring, IRQ counter state,
// and PRG-RAM.
type mapper4Blob struct {
	Banks      [8]byte
	BankSelect byte
	PrgMode    byte
	ChrMode    byte
	Mirror     int
	PRGRAM     []byte
	IRQLatch   byte
	IRQCount   byte
	IRQReload  bool
	IRQEnable  bool
	IRQFlag    bool
}

func (m *mapper4) stateBlob() []byte {
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(mapper4Blob{
		Banks: m.banks, BankSelect: m.bankSelect,
		PrgMode: m.prgMode, ChrMode: m.chrMode,
		Mirror: int(m.mirror),
		PRGRAM: append([]byte(nil), m.prgRAM[:]...),
		IRQLatch: m.irqLatch, IRQCount: m.irqCount,
		IRQReload: m.irqReload, IRQEnable: m.irqEnable, IRQFlag: m.irqFlag,
	})
	return buf.Bytes()
}
func (m *mapper4) restoreBlob(data []byte) error {
	var b mapper4Blob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	m.banks, m.bankSelect = b.Banks, b.BankSelect
	m.prgMode, m.chrMode = b.PrgMode, b.ChrMode
	m.mirror = Mirroring(b.Mirror)
	if len(b.PRGRAM) == len(m.prgRAM) {
		copy(m.prgRAM[:], b.PRGRAM)
	}
	m.irqLatch, m.irqCount = b.IRQLatch, b.IRQCount
	m.irqReload, m.irqEnable, m.irqFlag = b.IRQReload, b.IRQEnable, b.IRQFlag
	return nil
}

// --- APU state blob ---

type apuBlob struct {
	Pulse1, Pulse2 pulseBlob
	Triangle       triangleBlob
	Noise          noiseBlob
	DMC            dmcBlob
	Cycle          uint64
	FrameStep      int
	FrameMode      byte
	InhibitIRQ     bool
}

type dmcBlob struct {
	Enabled, IrqEn, Loop                 bool
	RateIdx, Output                      byte
	SampleAddr, SampleLen                uint16
	Timer                                uint16
	CurrentAddr                          uint16
	BytesLeft                            uint16
	Shifter, BitsRemaining, SampleBuffer byte
	Silence, BufferLoaded, IrqPending    bool
}

type pulseBlob struct {
	Enabled                                                 bool
	Channel, Duty, DutyPos                                  byte
	Timer, TimerLoad                                        uint16
	Length                                                  byte
	LengthHalt                                              bool
	EnvStart, EnvLoop, EnvConstant                          bool
	EnvVolume, EnvDivider, EnvDecayLevel                    byte
	SweepEnabled                                            bool
	SweepPeriod, SweepShift                                 byte
	SweepNegate, SweepReload                                bool
	SweepDivider                                            byte
}

type triangleBlob struct {
	Enabled                                            bool
	Timer, TimerLoad                                   uint16
	Length, LinearReload, LinearCounter                byte
	LengthHalt, ReloadFlag                             bool
	SeqPos                                             byte
}

type noiseBlob struct {
	Enabled                              bool
	Timer, TimerLoad                     uint16
	Length                               byte
	LengthHalt, Mode                     bool
	EnvStart, EnvLoop, EnvConstant       bool
	EnvVolume, EnvDivider, EnvDecayLevel byte
	Shift                                uint16
}

func (a *APU) snapshot() []byte {
	b := apuBlob{
		Pulse1:     pulseToBlob(&a.Pulse1),
		Pulse2:     pulseToBlob(&a.Pulse2),
		Triangle:   triangleToBlob(&a.Triangle),
		Noise:      noiseToBlob(&a.Noise),
		DMC:        dmcToBlob(&a.DMC),
		Cycle:      a.cycle,
		FrameStep:  a.frameStep,
		FrameMode:  a.frameMode,
		InhibitIRQ: a.inhibitIRQ,
	}
	var buf bytes.Buffer
	gob.NewEncoder(&buf).Encode(b)
	return buf.Bytes()
}

func (a *APU) restore(data []byte) error {
	var b apuBlob
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&b); err != nil {
		return err
	}
	pulseFromBlob(&a.Pulse1, b.Pulse1)
	pulseFromBlob(&a.Pulse2, b.Pulse2)
	triangleFromBlob(&a.Triangle, b.Triangle)
	noiseFromBlob(&a.Noise, b.Noise)
	dmcFromBlob(&a.DMC, b.DMC)
	a.cycle, a.frameStep, a.frameMode, a.inhibitIRQ =
		b.Cycle, b.FrameStep, b.FrameMode, b.InhibitIRQ
	return nil
}

func dmcToBlob(d *dmcCh) dmcBlob {
	return dmcBlob{
		Enabled: d.enabled, IrqEn: d.irqEn, Loop: d.loop,
		RateIdx: d.rateIdx, Output: d.output,
		SampleAddr: d.sampleAddr, SampleLen: d.sampleLen,
		Timer: d.timer, CurrentAddr: d.currentAddr, BytesLeft: d.bytesLeft,
		Shifter: d.shifter, BitsRemaining: d.bitsRemaining, SampleBuffer: d.sampleBuffer,
		Silence: d.silence, BufferLoaded: d.bufferLoaded, IrqPending: d.irqPending,
	}
}
func dmcFromBlob(d *dmcCh, b dmcBlob) {
	d.enabled, d.irqEn, d.loop = b.Enabled, b.IrqEn, b.Loop
	d.rateIdx, d.output = b.RateIdx, b.Output
	d.sampleAddr, d.sampleLen = b.SampleAddr, b.SampleLen
	d.timer, d.currentAddr, d.bytesLeft = b.Timer, b.CurrentAddr, b.BytesLeft
	d.shifter, d.bitsRemaining, d.sampleBuffer = b.Shifter, b.BitsRemaining, b.SampleBuffer
	d.silence, d.bufferLoaded, d.irqPending = b.Silence, b.BufferLoaded, b.IrqPending
}

func pulseToBlob(p *pulseCh) pulseBlob {
	return pulseBlob{
		Enabled: p.enabled, Channel: p.channel, Duty: p.duty, DutyPos: p.dutyPos,
		Timer: p.timer, TimerLoad: p.timerLoad,
		Length: p.length, LengthHalt: p.lengthHalt,
		EnvStart: p.env.start, EnvLoop: p.env.loop, EnvConstant: p.env.constant,
		EnvVolume: p.env.volume, EnvDivider: p.env.divider, EnvDecayLevel: p.env.decayLevel,
		SweepEnabled: p.sweepEnabled, SweepPeriod: p.sweepPeriod, SweepShift: p.sweepShift,
		SweepNegate: p.sweepNegate, SweepReload: p.sweepReload, SweepDivider: p.sweepDivider,
	}
}
func pulseFromBlob(p *pulseCh, b pulseBlob) {
	p.enabled, p.channel, p.duty, p.dutyPos = b.Enabled, b.Channel, b.Duty, b.DutyPos
	p.timer, p.timerLoad = b.Timer, b.TimerLoad
	p.length, p.lengthHalt = b.Length, b.LengthHalt
	p.env.start, p.env.loop, p.env.constant = b.EnvStart, b.EnvLoop, b.EnvConstant
	p.env.volume, p.env.divider, p.env.decayLevel = b.EnvVolume, b.EnvDivider, b.EnvDecayLevel
	p.sweepEnabled, p.sweepPeriod, p.sweepShift = b.SweepEnabled, b.SweepPeriod, b.SweepShift
	p.sweepNegate, p.sweepReload, p.sweepDivider = b.SweepNegate, b.SweepReload, b.SweepDivider
}

func triangleToBlob(t *triangleCh) triangleBlob {
	return triangleBlob{
		Enabled: t.enabled, Timer: t.timer, TimerLoad: t.timerLoad,
		Length: t.length, LinearReload: t.linearReload, LinearCounter: t.linearCounter,
		LengthHalt: t.lengthHalt, ReloadFlag: t.reloadFlag, SeqPos: t.seqPos,
	}
}
func triangleFromBlob(t *triangleCh, b triangleBlob) {
	t.enabled, t.timer, t.timerLoad = b.Enabled, b.Timer, b.TimerLoad
	t.length, t.linearReload, t.linearCounter = b.Length, b.LinearReload, b.LinearCounter
	t.lengthHalt, t.reloadFlag, t.seqPos = b.LengthHalt, b.ReloadFlag, b.SeqPos
}

func noiseToBlob(n *noiseCh) noiseBlob {
	return noiseBlob{
		Enabled: n.enabled, Timer: n.timer, TimerLoad: n.timerLoad,
		Length: n.length, LengthHalt: n.lengthHalt, Mode: n.mode,
		EnvStart: n.env.start, EnvLoop: n.env.loop, EnvConstant: n.env.constant,
		EnvVolume: n.env.volume, EnvDivider: n.env.divider, EnvDecayLevel: n.env.decayLevel,
		Shift: n.shift,
	}
}
func noiseFromBlob(n *noiseCh, b noiseBlob) {
	n.enabled, n.timer, n.timerLoad = b.Enabled, b.Timer, b.TimerLoad
	n.length, n.lengthHalt, n.mode = b.Length, b.LengthHalt, b.Mode
	n.env.start, n.env.loop, n.env.constant = b.EnvStart, b.EnvLoop, b.EnvConstant
	n.env.volume, n.env.divider, n.env.decayLevel = b.EnvVolume, b.EnvDivider, b.EnvDecayLevel
	n.shift = b.Shift
}
