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

// TestFluctuatingInputStaysFinite drives the algorithm with realistic
// jittery SGP40 readings — the original port had an operator-precedence
// bug in the MVE delta calculation (`sraw - mean/64` instead of
// `(sraw - mean)/64`) that was masked by the flat-input tests but blew
// the mean past ±1e30 within a couple of minutes on a real device. This
// test reproduces that scenario so the regression can't sneak back in.
func TestFluctuatingInputStaysFinite(t *testing.T) {
	a := New()
	for i := 0; i < 1800; i++ {
		// Pseudo-random walk: jitter ±20 ticks around a slow upward drift,
		// staying well inside SGP40's normal 25k–35k operating range.
		jitter := int32((i*13)%41) - 20
		drift := int32(i / 60)
		raw := 31000 + drift + jitter
		idx := a.Process(raw)
		if idx > 500 || idx < 0 {
			t.Fatalf("step %d: index out of [0,500]: got %d  raw=%d mean=%g",
				i, idx, raw, a.MoxMean())
		}
		// MoxMean is uninitialized (0) during the 45 s blackout — only
		// start checking baseline sanity once the algorithm has accepted
		// a real sample. Factor-of-2 oscillation, the bug's signature,
		// will trip this inside ~30 steps after the mean clears ±100.
		if i > 60 && (a.MoxMean() < 5000 || a.MoxMean() > 20000) {
			t.Fatalf("step %d: mean diverged: %g  raw=%d idx=%d",
				i, a.MoxMean(), raw, idx)
		}
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
