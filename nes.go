package main

import "fmt"

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

// StepFrame runs CPU + PPU + APU until PPU.frame ticks over. Every CPU
// cycle ticks the PPU 3 times (real 3:1 PPU:CPU ratio). All MMC3 IRQ
// clocking happens inside the PPU via A12 rising edges on CHR fetches —
// no scanline heuristics up here.
var debugMapper4 = false

func (n *NES) StepFrame() {
	startFrame := n.PPU.frame
	// Snapshot MMC3 counters for diagnostic
	var clockBefore, irqBefore int
	if debugMapper4 {
		if m, ok := n.Bus.Cart.mapper.(*mapper4); ok {
			clockBefore = m.clockCount
			irqBefore = m.irqClocks
		}
	}
	for n.PPU.frame == startFrame {
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
	}
	if debugMapper4 {
		if m, ok := n.Bus.Cart.mapper.(*mapper4); ok {
			clockDelta := m.clockCount - clockBefore
			irqDelta := m.irqClocks - irqBefore
			fmt.Printf("frame %d: %d clocks, %d IRQs (latch=%d enabled=%v)\n",
				n.PPU.frame, clockDelta, irqDelta, m.irqLatch, m.irqEnable)
		}
	}
}
