//go:build tinygo && esp32s3

package dht

import "machine"

func cpuFrequencyHz() uint32 {
	freq, err := machine.GetCPUFrequency()
	if err != nil || freq == 0 {
		return 240_000_000
	}
	return freq
}