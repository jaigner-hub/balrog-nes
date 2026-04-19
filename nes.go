package main

type NES struct {
	Bus *NESBus
	CPU *CPU
	PPU *PPU
	APU *APU
}

func NewNES(cart *Cart, sampleRate float64) *NES {
	ppu := NewPPU(cart)
	apu := NewAPU(sampleRate)
	cpu := &CPU{}
	bus := &NESBus{Cart: cart, PPU: ppu, APU: apu, CPU: cpu}
	cpu.Bus = bus
	cpu.Reset()
	return &NES{Bus: bus, CPU: cpu, PPU: ppu, APU: apu}
}

// StepFrame runs CPU + PPU + APU until a frame completes.
// NTSC NES runs 29780.67 CPU cycles per frame (262 scanlines * 341 PPU / 3).
// We give each scanline a fractional share of that total so overall CPU
// cycles-per-frame match hardware — critical for audio sample rate to match
// the 44.1 kHz Ebiten output (otherwise the ring buffer drifts and drops).
const cpuCyclesPerFrame = 29780

func (n *NES) StepFrame() {
	frameStart := n.CPU.cycles
	scanline := 0
	for done := false; !done; {
		// Each scanline ends at a cumulative cycle count matching its fraction.
		target := frameStart + uint64(cpuCyclesPerFrame*(scanline+1))/262
		for n.CPU.cycles < target {
			before := n.CPU.cycles
			n.CPU.Step()
			delta := int(n.CPU.cycles - before)
			for i := 0; i < delta; i++ {
				n.APU.Step()
			}
			if n.PPU.NMIPending {
				n.PPU.NMIPending = false
				n.CPU.NMI()
			}
		}
		done = n.PPU.StepScanline()
		scanline++
	}
}
