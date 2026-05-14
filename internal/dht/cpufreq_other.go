//go:build tinygo && !esp32s3

package dht

import "machine"

func cpuFrequencyHz() uint32 {
	return machine.CPUFrequency()
}
