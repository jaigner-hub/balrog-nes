package main

// Optional interfaces a mapper can implement to participate in scanline-based
// IRQ counting (used by MMC3 and its cousins). We type-assert at runtime, so
// older mappers that don't need this don't have to implement it.
type scanlineCounter interface {
	ClockScanline()
}
type irqMapper interface {
	IRQPending() bool
}

type NES struct {
	Bus *NESBus
	CPU *CPU
	PPU *PPU
	APU *APU

	irqMapper irqMapper
}

func NewNES(cart *Cart, sampleRate float64) *NES {
	ppu := NewPPU(cart)
	apu := NewAPU(sampleRate)
	cpu := &CPU{}
	bus := &NESBus{Cart: cart, PPU: ppu, APU: apu, CPU: cpu}
	cpu.Bus = bus
	apu.bus = bus
	apu.cpu = cpu
	cpu.Reset()
	n := &NES{Bus: bus, CPU: cpu, PPU: ppu, APU: apu}
	if irq, ok := cart.mapper.(irqMapper); ok {
		n.irqMapper = irq
	}
	return n
}

// StepFrame runs CPU + PPU + APU until a full frame has been rendered.
// Every CPU cycle ticks the PPU 3 times (that's the real 3:1 PPU:CPU ratio).
// All MMC3 IRQ clocking happens inside the PPU via A12 rising edges on CHR
// fetches — no scanline heuristics up here.
func (n *NES) StepFrame() {
	// Catch the pre-frame state: we're about to start a new frame whenever
	// the PPU is at (0, 0) — it'll be there initially on the very first call
	// and after every FrameDone from the previous StepFrame.
	// Run until we cross into a new frame and then back to (0, 0).
	started := false
	for {
		// Step CPU one instruction (or one stall cycle)
		before := n.CPU.cycles
		n.CPU.Step()
		delta := int(n.CPU.cycles - before)
		for i := 0; i < delta; i++ {
			n.APU.Step()
			n.PPU.Step()
			n.PPU.Step()
			n.PPU.Step()
		}
		if n.PPU.NMIPending {
			n.PPU.NMIPending = false
			n.CPU.NMI()
		}
		if n.APU.DMC.irqPending {
			n.CPU.IRQ()
		}
		if n.irqMapper != nil && n.irqMapper.IRQPending() {
			n.CPU.IRQ()
		}
		// Frame boundary: we've seen PPU move out of (0,0) and back into it
		if !started {
			if n.PPU.scanline != 0 || n.PPU.cycle != 0 {
				started = true
			}
		} else if n.PPU.scanline == 0 && n.PPU.cycle == 0 {
			return
		}
	}
}
