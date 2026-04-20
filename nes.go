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
	// Wire CPU tick → step APU once and PPU 3x. Each CPU bus access (and
	// internal cycles) triggers one tick, so register writes land at the
	// correct PPU cycle within each instruction.
	cpu.tickFn = func() {
		n.APU.Step()
		n.PPU.Step()
		n.PPU.Step()
		n.PPU.Step()
		// Sample interrupt lines this cycle. NMI is edge-triggered so we
		// latch it on the rising edge. IRQ is level-triggered — track the
		// live line state each cycle; `Step` decides when to actually take
		// the interrupt based on instruction-cycle position (T-1 phi2).
		if n.PPU.NMIPending {
			n.PPU.NMIPending = false
			n.CPU.rawNMI = true
		}
		irq := false
		if n.APU.DMC.irqPending {
			irq = true
		}
		if n.irqMapper != nil && n.irqMapper.IRQPending() {
			irq = true
		}
		n.CPU.rawIRQ = irq
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
		// CPU.Step internally ticks PPU/APU per cycle via tickFn (set in
		// NewNES). NMI/IRQ pending flags are sampled inside tickFn each
		// cycle, so a pending interrupt is taken at the next instruction
		// boundary without another loop check here.
		n.CPU.Step()
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
