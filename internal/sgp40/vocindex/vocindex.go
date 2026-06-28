// Package vocindex computes Sensirion's "VOC Index" (0–500, neutral ~100)
// from raw SGP40 ticks. It is a TinyGo-friendly float32 port of the BSD-3
// licensed C reference at
//
//	https://github.com/Sensirion/gas-index-algorithm
//	commit: VOC algorithm v3.2, file sensirion_gas_index_algorithm.c
//
// Only the VOC branch is included (SGP40 has no NOx output). The fixed-point
// fix16_t intermediates of the upstream code are replaced with float32 —
// the ESP32-S3 has a hardware FPU and the original code already treats the
// state as logical floats; the scaling constants (GAMMA_SCALING etc.) are
// preserved so the dynamics match the reference exactly.
//
// Usage:
//
//	a := vocindex.New()        // call once
//	for {                      // call exactly once per sampling interval (1 s)
//	    raw, _ := sgp40.MeasureRawCompensated(rh, t)
//	    idx := a.Process(int32(raw))
//	    // idx is 0 during the first ~45 s (initial blackout), then 0..500.
//	}
//
// The estimator needs roughly 12 h of continuous samples to fully learn the
// environmental baseline; before that it returns useful but transient values.
package vocindex

import "math"

// VOC algorithm v3.2 constants, copied verbatim from
// sensirion_gas_index_algorithm.h so the dynamics stay identical to upstream.
const (
	samplingInterval                       = 1.0
	initialBlackout                        = 45.0
	indexGain                              = 230.0
	srawStdInitial                         = 50.0
	srawStdBonusVoc                        = 220.0
	tauMeanHoursVoc                        = 12.0
	tauVarianceHoursVoc                    = 12.0
	tauInitialMeanVoc                      = 20.0
	initDurationMeanVoc                    = 3600.0 * 0.75
	initTransitionMean                     = 0.01
	tauInitialVariance                     = 2500.0
	initDurationVarianceVoc                = 3600.0 * 1.45
	initTransitionVariance                 = 0.01
	gatingThresholdVoc                     = 340.0
	gatingThresholdInitial                 = 510.0
	gatingThresholdTransition              = 0.09
	gatingMaxDurationMinutesVoc            = 60.0 * 3.0
	gatingMaxRatio                         = 0.3
	sigmoidL                               = 500.0
	sigmoidKVoc                            = -0.0065
	sigmoidX0Voc                           = 213.0
	vocIndexOffsetDefault                  = 100.0
	lpTauFast                              = 20.0
	lpTauSlow                              = 500.0
	lpAlpha                                = -0.2
	persistenceUptimeGamma                 = 3.0 * 3600.0
	mveGammaScaling                        = 64.0
	mveAdditionalGammaMeanScaling          = 8.0
	mveFix16Max                            = 32767.0
)

// Algorithm holds the state of one running VOC Index estimator. Not safe for
// concurrent use; the host is expected to drive it from a single 1 Hz loop.
type Algorithm struct {
	// Configuration (defaults applied by New, exposed via setters in case
	// the caller wants to tune offset/gain like the reference C API does).
	indexOffset          float32
	indexGain            float32
	tauMeanHours         float32
	tauVarianceHours     float32
	gatingMaxDurationMin float32
	srawStdInit          float32

	// Per-loop bookkeeping.
	uptime   float32
	sraw     float32
	vocIndex float32

	// Mean-variance estimator (adaptive baseline + std for sraw).
	mveInitialized           bool
	mveMean                  float32
	mveSrawOffset            float32
	mveStd                   float32
	mveGamma                 float32 // base gamma for steady-state mean update
	mveGammaVar              float32 // base gamma for steady-state variance update
	mveGammaInitMean         float32
	mveGammaInitVariance     float32
	mveGammaMean             float32 // currently-effective mean gamma (blend init/steady)
	mveGammaVariance         float32
	mveUptimeGamma           float32
	mveUptimeGating          float32
	mveGatingDurationMinutes float32
	mveSigmoidK              float32 // gating sigmoid
	mveSigmoidX0             float32

	// MOX model: parameterized by current mean/std.
	moxSrawStd  float32
	moxSrawMean float32

	// Output sigmoid (raw delta -> 0..500 index).
	sigK      float32
	sigX0     float32
	sigOffset float32

	// Adaptive lowpass on the final index.
	lpA1          float32
	lpA2          float32
	lpInitialized bool
	lpX1          float32
	lpX2          float32
	lpX3          float32
}

// New returns a fresh Algorithm configured with the VOC defaults from the
// Sensirion reference. The estimator is ready to call Process immediately.
func New() *Algorithm {
	a := &Algorithm{
		indexOffset:          vocIndexOffsetDefault,
		indexGain:            indexGain,
		tauMeanHours:         tauMeanHoursVoc,
		tauVarianceHours:     tauVarianceHoursVoc,
		gatingMaxDurationMin: gatingMaxDurationMinutesVoc,
		srawStdInit:          srawStdInitial,
	}
	a.reset()
	return a
}

// Reset discards all learned state and returns the algorithm to its
// power-on condition (45 s blackout, neutral baseline).
func (a *Algorithm) Reset() { a.reset() }

// Uptime returns the number of samples Process has consumed since the last
// Reset, expressed in seconds (samplingInterval is 1 s). The caller can
// compare against the 45 s initial blackout to tell "warming up" from
// "received an out-of-band sraw and held last output."
func (a *Algorithm) Uptime() float32 { return a.uptime }

// InBlackout reports whether Process is still in the initial 45 s blackout
// window. Useful for log lines that want to distinguish "warming, this is
// expected" from "still 0 after warmup, something is off."
func (a *Algorithm) InBlackout() bool { return a.uptime <= initialBlackout }

// Index returns the most recent internal floating-point index value before
// rounding. Exposing the unrounded state lets the caller distinguish
// "algorithm is producing a real value but rounding bit it" from
// "algorithm output really is ~0".
func (a *Algorithm) Index() float32 { return a.vocIndex }

// MoxMean returns the learned baseline sraw (post -20000 shift). Exposed
// only for debug logs — the algorithm advances on its own.
func (a *Algorithm) MoxMean() float32 { return a.moxSrawMean }

func (a *Algorithm) reset() {
	a.uptime = 0
	a.sraw = 0
	a.vocIndex = 0
	a.initMVE()
	a.setMVEParameters(a.srawStdInit, a.tauMeanHours, a.tauVarianceHours)
	a.initMox()
	a.setMoxParameters(a.mveStd, a.mveMean+a.mveSrawOffset)
	a.initSigmoid()
	a.setSigmoidParameters(sigmoidKVoc, sigmoidX0Voc, a.indexOffset)
	a.initLowpass()
	a.setLowpassParameters()
}

// Process consumes one raw SGP40 tick and returns the corresponding VOC Index
// (0 during initial blackout, otherwise typically 0..500 with 100 being the
// long-term average for the current environment). Call exactly once per
// sampling interval; cadence drift breaks the learned dynamics.
//
// The accepted raw input range is (0, 65000); values outside that band are
// treated as missing and the previous output is returned unchanged. The
// reference algorithm shifts sraw by -20000 so the working range sits near
// zero — Process applies that shift internally; callers pass the unshifted
// 16-bit tick from MeasureRaw.
func (a *Algorithm) Process(sraw int32) int32 {
	if a.uptime <= initialBlackout {
		a.uptime += samplingInterval
		return 0
	}
	if sraw <= 0 || sraw >= 65000 {
		// Out-of-band sample: keep last output, don't poison the baseline.
		return roundUp(a.vocIndex)
	}
	if sraw < 20001 {
		sraw = 20001
	} else if sraw > 52767 {
		sraw = 52767
	}
	srawF := float32(sraw - 20000)

	a.sraw = srawF
	a.vocIndex = a.moxProcess(srawF)
	a.vocIndex = a.sigmoidProcess(a.vocIndex)
	a.vocIndex = a.lowpassProcess(a.vocIndex)
	// Combined floor + NaN scrub. The comparison `v < 0.5` is false when
	// v is NaN (IEEE 754 ordered compares all return false against NaN),
	// so the previous `if v < 0.5` floor would silently let NaN through —
	// `int32(NaN)` then collapses to 0 and the reading appears stuck. Use
	// a positive-equality test so NaN trips the corrective branch.
	if !(a.vocIndex >= 0.5) {
		a.vocIndex = 0.5
	}

	a.mveProcess(srawF, a.vocIndex)
	a.setMoxParameters(a.mveStd, a.mveMean+a.mveSrawOffset)
	return roundUp(a.vocIndex)
}

// roundUp converts a non-negative float32 to int32 with round-half-away-
// from-zero. Replaces math.Round in the Process hot path because the
// stdlib Round goes through float64 conversion and a TinyGo libm shim;
// for our non-negative output (the 0.5 floor above guarantees v ≥ 0.5)
// a single `int32(v + 0.5)` matches the upstream C reference exactly and
// has no library-version sensitivity.
func roundUp(v float32) int32 {
	if v <= 0 {
		return 0
	}
	return int32(v + 0.5)
}

// ---------------- Mean / Variance Estimator -----------------------------

func (a *Algorithm) initMVE() {
	a.mveInitialized = false
	a.mveMean = 0
	a.mveSrawOffset = 0
	a.mveStd = 0
	a.mveGamma = 0
	a.mveGammaVar = 0
	a.mveGammaInitMean = 0
	a.mveGammaInitVariance = 0
	a.mveGammaMean = 0
	a.mveGammaVariance = 0
	a.mveUptimeGamma = 0
	a.mveUptimeGating = 0
	a.mveGatingDurationMinutes = 0
	a.mveSigmoidK = 0
	a.mveSigmoidX0 = 0
}

func (a *Algorithm) setMVEParameters(stdInitial, tauMeanH, tauVarH float32) {
	a.tauMeanHours = tauMeanH
	a.tauVarianceHours = tauVarH
	a.mveInitialized = false
	a.mveMean = 0
	a.mveSrawOffset = 0
	a.mveStd = stdInitial
	a.mveGammaMean = ((mveAdditionalGammaMeanScaling * mveGammaScaling * samplingInterval / 3600.0) /
		(tauMeanH + samplingInterval/3600.0))
	a.mveGammaVariance = (mveGammaScaling * samplingInterval / 3600.0) /
		(tauVarH + samplingInterval/3600.0)
	a.mveGammaInitMean = (mveAdditionalGammaMeanScaling * mveGammaScaling * samplingInterval) /
		(tauInitialMeanVoc + samplingInterval)
	a.mveGammaInitVariance = (mveGammaScaling * samplingInterval) /
		(tauInitialVariance + samplingInterval)
	a.mveGamma = a.mveGammaMean
	a.mveGammaVar = a.mveGammaVariance
	a.mveUptimeGamma = 0
	a.mveUptimeGating = 0
	a.mveGatingDurationMinutes = 0
	a.mveSigmoidK = gatingThresholdTransition
	a.mveSigmoidX0 = gatingThresholdVoc
}

func (a *Algorithm) mveProcess(sraw, vocIndex float32) {
	if !a.mveInitialized {
		a.mveInitialized = true
		a.mveSrawOffset = sraw
		a.mveMean = 0
	} else {
		if a.mveMean >= 100.0 || a.mveMean <= -100.0 {
			a.mveSrawOffset += a.mveMean
			a.mveMean = 0
		}
		sraw -= a.mveSrawOffset

		// Update gamma blend between "init" (fast learning) and steady state.
		gammaMean := a.calcGammaMean()
		gammaVariance := a.calcGammaVariance()

		// Gating sigmoid: when current vocIndex is large (away from 100), reduce
		// learning rate so a transient spike doesn't shift the baseline.
		sigmoid := mveSigmoid(vocIndex, a.mveSigmoidK, a.mveSigmoidX0)
		// Gating max-duration cap: prevent permanent gating during long events.
		if vocIndex < a.mveSigmoidX0 {
			a.mveGatingDurationMinutes += (samplingInterval / 60.0) * ((1.0 - sigmoid) * (1.0 + gatingMaxRatio))
		} else {
			a.mveGatingDurationMinutes -= (samplingInterval / 60.0) * (sigmoid * gatingMaxRatio)
		}
		if a.mveGatingDurationMinutes < 0 {
			a.mveGatingDurationMinutes = 0
		}
		if a.mveGatingDurationMinutes > a.gatingMaxDurationMin {
			a.mveUptimeGating = 0
		}

		// Apply gating to learning rate.
		gammaMean *= sigmoid
		gammaVariance *= sigmoid

		// Sensirion reference: delta_sgp = (sraw - mean) / GAMMA_SCALING.
		// The previous port wrote `sraw - a.mveMean/mveGammaScaling`, which
		// Go's operator precedence parses as `sraw - (mean/64)` — only the
		// mean got the /64, leaving the effective update gain 64× too high.
		// With constant input that still decays to zero so unit tests pass,
		// but with a real fluctuating SGP40 stream the mean blows past the
		// ±100 absorb threshold in one step and the offset starts
		// oscillating with a factor of ~−2 per step until the float
		// overflows to ±Inf and the algorithm collapses to NaN.
		delta := (sraw - a.mveMean) / mveGammaScaling
		var deltaVariance float32
		if delta < 0 {
			deltaVariance = -delta
		} else {
			deltaVariance = delta
		}

		// Variance update: EMA of |delta| as a proxy for std.
		std := a.mveStd
		std = sqrtF((std*std*(mveGammaScaling-gammaVariance) + gammaVariance*deltaVariance*deltaVariance) / mveGammaScaling)
		a.mveStd = std

		// Mean update.
		a.mveMean += gammaMean * delta / mveAdditionalGammaMeanScaling
	}
	a.mveUptimeGamma += samplingInterval
	a.mveUptimeGating += samplingInterval
}

// calcGammaMean blends the "initial" (fast) and "steady-state" mean-update
// gammas around INIT_DURATION_MEAN_VOC using a smooth INIT_TRANSITION_MEAN
// sigmoid.
func (a *Algorithm) calcGammaMean() float32 {
	sig := sigmoidUnit(a.mveUptimeGamma, initTransitionMean*initDurationMeanVoc, initDurationMeanVoc)
	return a.mveGammaInitMean + sig*(a.mveGammaMean-a.mveGammaInitMean)
}

func (a *Algorithm) calcGammaVariance() float32 {
	sig := sigmoidUnit(a.mveUptimeGamma, initTransitionVariance*initDurationVarianceVoc, initDurationVarianceVoc)
	return a.mveGammaInitVariance + sig*(a.mveGammaVariance-a.mveGammaInitVariance)
}

// ---------------- MOX Model --------------------------------------------

func (a *Algorithm) initMox() {
	a.moxSrawStd = 1
	a.moxSrawMean = 0
}

func (a *Algorithm) setMoxParameters(std, mean float32) {
	a.moxSrawStd = std
	a.moxSrawMean = mean
}

// moxProcess converts a raw value into a sigmoid-input "delta": how far the
// sample sits from the learned baseline, in units of (std + bonus).
func (a *Algorithm) moxProcess(sraw float32) float32 {
	return (sraw - a.moxSrawMean) / -(a.moxSrawStd + srawStdBonusVoc) * a.indexGain
}

// ---------------- Output sigmoid ---------------------------------------

func (a *Algorithm) initSigmoid() {
	a.sigK = sigmoidKVoc
	a.sigX0 = sigmoidX0Voc
	a.sigOffset = vocIndexOffsetDefault
}

func (a *Algorithm) setSigmoidParameters(k, x0, offset float32) {
	a.sigK = k
	a.sigX0 = x0
	a.sigOffset = offset
}

// sigmoidProcess maps the MOX delta to the final 0..500 index, with the
// learned baseline mapping to the configured offset (default 100).
func (a *Algorithm) sigmoidProcess(sample float32) float32 {
	x := a.sigK * (sample - a.sigX0)
	if x < -50.0 {
		return sigmoidL - a.sigOffset
	}
	if x > 50.0 {
		return 0
	}
	shift := (sigmoidL - 5.0*a.sigOffset) / 4.0
	return (sigmoidL+shift)/(1.0+expF(x)) - shift
}

// ---------------- Adaptive lowpass -------------------------------------

func (a *Algorithm) initLowpass() {
	a.lpA1 = samplingInterval / (lpTauFast + samplingInterval)
	a.lpA2 = samplingInterval / (lpTauSlow + samplingInterval)
	a.lpInitialized = false
	a.lpX1 = 0
	a.lpX2 = 0
	a.lpX3 = 0
}

func (a *Algorithm) setLowpassParameters() {
	a.lpA1 = samplingInterval / (lpTauFast + samplingInterval)
	a.lpA2 = samplingInterval / (lpTauSlow + samplingInterval)
}

// lowpassProcess runs two EMAs (fast & slow) and blends them by the magnitude
// of their disagreement: when the signal is moving fast, trust the fast EMA;
// when it's quiet, trust the slow one. This is the same trick the reference
// uses to keep step-response snappy without amplifying steady-state noise.
func (a *Algorithm) lowpassProcess(sample float32) float32 {
	if !a.lpInitialized {
		a.lpX1 = sample
		a.lpX2 = sample
		a.lpX3 = sample
		a.lpInitialized = true
		return sample
	}
	a.lpX1 += a.lpA1 * (sample - a.lpX1)
	a.lpX2 += a.lpA2 * (sample - a.lpX2)
	abs := a.lpX1 - a.lpX2
	if abs < 0 {
		abs = -abs
	}
	f1 := expF(lpAlpha * abs)
	tau := (lpTauSlow-lpTauFast)*f1 + lpTauFast
	a3 := samplingInterval / (tau + samplingInterval)
	a.lpX3 += a3 * (sample - a.lpX3)
	return a.lpX3
}

// ---------------- Small math helpers (TinyGo math/math32 friendly) -----

// sigmoidUnit is the smooth init/steady-state transition used inside the
// mean/variance estimator: 0 while uptime << center, ~1 once uptime >> center.
func sigmoidUnit(uptime, k, x0 float32) float32 {
	v := k * (uptime - x0)
	if v < -50.0 {
		return 0
	}
	if v > 50.0 {
		return 1
	}
	return 1.0 / (1.0 + expF(v))
}

// mveSigmoid is the gating sigmoid: 1 when vocIndex is near the offset
// (normal air -> learn fast), 0 when far from it (gas event -> freeze).
func mveSigmoid(sample, k, x0 float32) float32 {
	v := k * (sample - x0)
	if v < -50.0 {
		return 1
	}
	if v > 50.0 {
		return 0
	}
	return 1.0 / (1.0 + expF(v))
}

func expF(x float32) float32   { return float32(math.Exp(float64(x))) }
func sqrtF(x float32) float32  { return float32(math.Sqrt(float64(x))) }
func roundF(x float32) float32 { return float32(math.Round(float64(x))) }
