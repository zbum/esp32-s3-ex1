package vocindex

import "testing"

// TestInitialBlackout verifies the spec'd ~45 s output=0 startup window.
// The reference algorithm gates the first 45 samples (at 1 Hz) so a freshly
// powered board doesn't paint a wild value before the baseline has settled.
func TestInitialBlackout(t *testing.T) {
	a := New()
	for i := 0; i < 45; i++ {
		if got := a.Process(30000); got != 0 {
			t.Fatalf("sample %d during blackout: got %d, want 0", i, got)
		}
	}
}

// TestSteadyStateConvergesNear100 feeds a flat raw value past the blackout
// and a few hundred samples of the fast-learning init phase. The reference
// algorithm should pull the output toward the configured offset (100) — we
// don't need exact equality (the EMA is still moving), but anything outside
// 0..150 means the dynamics are badly broken.
func TestSteadyStateConvergesNear100(t *testing.T) {
	a := New()
	var last int32
	for i := 0; i < 1200; i++ {
		last = a.Process(30000)
	}
	if last < 0 || last > 150 {
		t.Fatalf("flat input after 1200 s should sit near 100, got %d", last)
	}
}

// TestOutOfBandSampleHoldsOutput simulates an I2C read error returning 0 (or
// out-of-range 65000) after the algorithm has been running. Process should
// not blow up the baseline or move the output dramatically.
func TestOutOfBandSampleHoldsOutput(t *testing.T) {
	a := New()
	for i := 0; i < 200; i++ {
		a.Process(30000)
	}
	good := a.Process(30000)
	bad := a.Process(0)
	if bad != good {
		t.Fatalf("zero sample should hold output: good=%d bad=%d", good, bad)
	}
	bad2 := a.Process(65001)
	if bad2 != good {
		t.Fatalf("oversize sample should hold output: good=%d bad2=%d", good, bad2)
	}
}

// TestSpikeDropsBelowBaseline confirms the sign of the response: SGP40 ticks
// fall as VOC load rises (the sensor's resistance drops), so a downward
// raw-tick step should pull the index above the offset, not below it.
func TestSpikeDropsBelowBaseline(t *testing.T) {
	a := New()
	for i := 0; i < 600; i++ {
		a.Process(30000)
	}
	baseline := a.Process(30000)
	var spiked int32
	for i := 0; i < 5; i++ {
		spiked = a.Process(25000) // lower raw = more VOC -> higher index
	}
	if spiked <= baseline {
		t.Fatalf("falling raw should raise index: baseline=%d spiked=%d", baseline, spiked)
	}
}
