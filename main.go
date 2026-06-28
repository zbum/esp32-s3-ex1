// Default sketch: poll the Sensirion SGP40 VOC sensor and the ASAIR AHT20
// temperature + humidity sensor over a shared I²C0 bus, mirror every status
// line to both the USB serial console and a 1.69" 240x280 ST7789V3 TFT, and
// (when WiFi credentials are supplied at build time) push the latest sample
// of all three signals to a Prometheus Pushgateway in one POST.
//
// The T/RH module on the bench is labeled "AHT10" but its silicon
// answers the AHT20 init command (0xBE), not the AHT10 one (0xE1) — so
// the firmware uses tinygo.org/x/drivers/aht20.
//
// The on-board WS2812 RGB LED is wired up but left dark by default — its
// bit-banged protocol fights with WiFi IRQs and tends to latch onto a
// random color (green in this project). Call enableLED() if you want the
// palette demo back; see ledEnabled and cycleLED.
//
// Wiring for the ST7789V3 panel (see docs/gpio-pin-mapping.md for the rules
// behind these choices):
//
//	BLK -> GPIO7   backlight
//	RES -> GPIO8   reset, active low
//	CS  -> GPIO9   chip select
//	DC  -> GPIO10  data / command
//	SDA -> GPIO11  SPI MOSI
//	SCL -> GPIO12  SPI clock
//	VCC -> 3V3
//	GND -> GND
//
// Wiring for the SGP40 + AHT20 breakouts (shared I²C0 bus, distinct
// addresses 0x59 and 0x38; see docs/sgp40-wiring.md and docs/aht10-wiring.md
// — the AHT10 doc covers the AHT20-on-AHT10-board case too):
//
//	SDA -> GPIO5   I²C0 data  (shared)
//	SCL -> GPIO6   I²C0 clock (shared)
//	VCC -> 3V3
//	GND -> GND
//
// Credentials and Pushgateway target are injected at link time so they
// never land in source control. Build with:
//
//	make flash SSID=... PASSWORD=... PUSH_URL=http://host:9091 \
//	  PUSH_JOB=esp32-s3-ex1 PUSH_INSTANCE=sgp40
//
// Without SSID the WiFi step is skipped and the loop runs locally only.
// See docs/pushgateway.md for the full pipeline.
package main

import (
	"image/color"
	"machine"
	"strconv"
	"time"

	"esp32-s3-ex1/internal/console"
	"esp32-s3-ex1/internal/pushgateway"
	"esp32-s3-ex1/internal/sgp40"
	"esp32-s3-ex1/internal/sgp40/vocindex"
	"esp32-s3-ex1/internal/ws2812"

	"tinygo.org/x/drivers/aht20"

	"tinygo.org/x/drivers/netdev"
	nl "tinygo.org/x/drivers/netlink"
	espnl "tinygo.org/x/espradio/netlink"

	"tinygo.org/x/drivers/pixel"
	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freemono"
)

const (
	ledPin     = machine.GPIO48
	numPixels  = 1
	brightness = 32

	pinSCK  = machine.GPIO12
	pinMOSI = machine.GPIO11
	pinDC   = machine.GPIO10
	pinCS   = machine.GPIO9
	pinRST  = machine.GPIO8
	pinBL   = machine.GPIO7

	pinSDA = machine.GPIO5 // I²C0 SDA — SGP40 + AHT20
	pinSCL = machine.GPIO6 // I²C0 SCL — SGP40 + AHT20

	spiFreqHz = 40_000_000
	i2cFreqHz = 400_000

	// Panel: 1.69" Waveshare ST7789V3.
	// Visible area is 240x280 starting at GRAM row 20 inside a 240x320 GRAM.
	panelWidth     = 240
	panelHeight    = 280
	panelRowOffset = 20
	gramWidth      = 240
	gramHeight     = 320

	lineHeight  = 15
	baselineY   = 11
	charAdvance = 11

	// Loop sleeps 500 ms per tick. pushIntervalTicks*500 ms = push period.
	// 120 ticks = 60 s. Tried 30 ticks (15 s) first but the second push was
	// still failing with lneto.ErrExhausted — the socket from the prior
	// connection had not yet finished TIME_WAIT inside lneto's small TCP
	// pool. 60 s gives the previous socket time to fully release.
	pushIntervalTicks = 120
)

// ldflags-injected credentials and Pushgateway target. Defaults make local
// builds work without WiFi — pushURL stays empty unless overridden so the
// pusher is treated as disabled.
var (
	ssid         string
	password     string
	pushURL      string
	pushJob      = "esp32-s3-ex1"
	pushInstance = "esp32-s3"
)

var (
	term   *console.Console
	disp   *st7789.Device // exposed for direct dashboard rendering
	rh     *aht20.Device
	voc    *sgp40.Device
	vocIdx *vocindex.Algorithm
	pusher *pushgateway.Pusher

	ledStrip  *ws2812.Device
	ledPixels []color.RGBA
	// The on-board WS2812 is bit-banged with sub-microsecond pulse widths;
	// WiFi IRQs corrupt those frames and the LED latches onto the last
	// garbled value (in this project, green). Default to off; flip via
	// enableLED() if you really want the palette demo back.
	ledEnabled = false
)

// ledPalette / ledNames drive the WS2812 demo cycle. Kept at package scope so
// cycleLED stays a pure function over the loop counter.
var (
	ledPalette = []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
		{R: 255, G: 255, B: 0, A: 255},
		{R: 0, G: 255, B: 255, A: 255},
		{R: 255, G: 0, B: 255, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
	}
	ledNames = []string{"red", "green", "blue", "yellow", "cyan", "magenta", "white"}
)

// say prints to USB serial via println and mirrors the same string to the
// ST7789 console when the display has been initialized. A panel failure must
// not silence serial output, so the println call always runs first.
func say(s string) {
	println(s)
	if term != nil {
		term.Println(s)
	}
}

// sayColor is like say but renders the on-screen copy in a non-default
// foreground color. Serial output ignores the color since it has none.
// Used to flag push successes (green) and push errors (red) on the TFT.
func sayColor(s string, fg color.RGBA) {
	println(s)
	if term != nil {
		term.PrintlnColor(s, fg)
	}
}

var (
	colorPushOK  = color.RGBA{R: 80, G: 220, B: 80, A: 255}
	colorPushErr = color.RGBA{R: 220, G: 80, B: 80, A: 255}
)

func setupDisplay() {
	bus := machine.SPI0
	if err := bus.Configure(machine.SPIConfig{
		Frequency: spiFreqHz,
		SCK:       pinSCK,
		SDO:       pinMOSI,
		Mode:      0,
	}); err != nil {
		println("spi configure err:", err.Error())
		return
	}

	d := st7789.New(bus, pinRST, pinDC, pinCS, pinBL)
	// Tell the driver the controller's full GRAM size; the console handles
	// the panel rowstart itself because the driver zeroes it at Rotation0.
	d.Configure(st7789.Config{
		Width:  gramWidth,
		Height: gramHeight,
	})
	disp = &d

	t := console.New(disp, &freemono.Regular9pt7b, console.Config{
		Width:       panelWidth,
		Height:      panelHeight,
		RowOffset:   panelRowOffset,
		LineHeight:  lineHeight,
		Baseline:    baselineY,
		CharAdvance: charAdvance,
		Foreground:  color.RGBA{R: 220, G: 220, B: 220, A: 255},
		Background:  color.RGBA{R: 0, G: 0, B: 0, A: 255},
	})
	if t == nil {
		println("console.New returned nil")
		return
	}
	t.Init()
	term = t
}

// Dashboard rendering — once the loop starts, the TFT shows a static
// four-line readout (T / H / V / I) updated in place instead of the scrolling
// boot console. Four rows × dashLineH = 160 px; panelHeight = 280 px;
// dashY0 = 95 places the block with ~75 px top and ~45 px bottom margin.
var (
	dashFont  = &freemono.Bold18pt7b
	dashBG    = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	dashLineH = int16(40)
	dashY0    = int16(95)
	dashX     = int16(10)

	colorTemp = color.RGBA{R: 255, G: 160, B: 60, A: 255}  // warm orange
	colorHum  = color.RGBA{R: 80, G: 180, B: 255, A: 255}  // cyan/blue
	colorVOC1 = color.RGBA{R: 80, G: 220, B: 80, A: 255}   // good
	colorVOC2 = color.RGBA{R: 240, G: 220, B: 60, A: 255}  // moderate
	colorVOC3 = color.RGBA{R: 240, G: 80, B: 80, A: 255}   // bad
	colorNA   = color.RGBA{R: 140, G: 140, B: 140, A: 255} // dim grey when data missing
)

// vocColor maps SGP40 raw ticks to a status color. Lower raw = higher VOC
// load (resistance drops). Thresholds are rough heuristics, not VOC Index.
func vocColor(raw uint16) color.RGBA {
	switch {
	case raw >= 28000:
		return colorVOC1
	case raw >= 25000:
		return colorVOC2
	default:
		return colorVOC3
	}
}

// vocIndexColor maps the Sensirion VOC Index (0..500, 100 = baseline) to a
// 3-tier color. Sensirion's own guidance starts suggesting ventilation
// around 200, so the FAIR band sits just above that to give a small
// hysteresis margin before the row turns red.
func vocIndexColor(idx int32) color.RGBA {
	switch {
	case idx <= 150:
		return colorVOC1
	case idx <= 250:
		return colorVOC2
	default:
		return colorVOC3
	}
}

// vocIndexLabel returns the short ASCII status tag shown next to the index.
// The dashboard font (freemono Bold18pt7b) is ASCII-only, so Korean glyphs
// would need a font asset we don't ship — keep these to 3–4 chars to fit
// alongside the numeric value inside the 240 px row.
func vocIndexLabel(idx int32) string {
	switch {
	case idx <= 150:
		return "GOOD"
	case idx <= 250:
		return "FAIR"
	default:
		return "BAD"
	}
}

// rowBuf is the single off-screen row image shared by all four dashboard
// rows. We render each row's background + glyphs into this RAM buffer and
// then ship it to the panel in one DrawBitmap SPI burst, which removes the
// visible "FillRectangle → empty black → glyphs slowly drawn" flash the
// previous code produced once per row update.
//
// Memory cost: panelWidth(240) × dashLineH(40) × 2 bytes (RGB565) ≈ 19 KB.
// Plenty of headroom on the ESP32-S3's internal SRAM.
var (
	rowBuf      pixel.Image[pixel.RGB565BE]
	rowBufReady bool

	// lastRowText caches each row's last-rendered string. drawDashboard
	// skips the DrawBitmap entirely when the row text is unchanged — so
	// at 0.1 Hz refresh on relatively stable readings (e.g. T didn't move
	// 0.1 °C between ticks) the corresponding row stays untouched.
	lastRowText [4]string
)

// rowCanvas adapts the RAM-resident rowBuf to the tinyfont.Displayer
// interface. WriteLine writes glyph pixels via SetPixel into our buffer
// instead of the live panel; the buffer is then blitted in one shot.
type rowCanvas struct{}

func (rowCanvas) Size() (int16, int16) { return panelWidth, dashLineH }
func (rowCanvas) SetPixel(x, y int16, c color.RGBA) {
	if x < 0 || y < 0 || x >= panelWidth || y >= dashLineH {
		return
	}
	rowBuf.Set(int(x), int(y), pixel.NewColor[pixel.RGB565BE](c.R, c.G, c.B))
}
func (rowCanvas) Display() error { return nil }

// ensureRowBuf lazy-allocates the shared row image. It runs once on the
// first dashboard refresh — putting it in switchToDashboard would force
// the allocation even when the panel never came up.
func ensureRowBuf() {
	if rowBufReady {
		return
	}
	rowBuf = pixel.NewImage[pixel.RGB565BE](int(panelWidth), int(dashLineH))
	rowBufReady = true
}

// switchToDashboard clears the visible panel and detaches the console so
// say() / sayColor() only emit to the serial port from this point on.
func switchToDashboard() {
	if disp == nil {
		return
	}
	disp.FillRectangle(0, panelRowOffset, panelWidth, panelHeight, dashBG)
	// Force every row to redraw on the next drawDashboard — last-render
	// cache must not falsely report "already drawn" for a freshly black
	// panel.
	for i := range lastRowText {
		lastRowText[i] = ""
	}
	term = nil
}

// drawDashboard renders the current readings in place. Each row is cleared
// to background first so old digits don't bleed through when a value
// shortens (e.g. 30000 → 9999).
func drawDashboard(tempC, humPct float32, vocRaw uint16, vocVal int32, haveTH, haveVOC, haveIdx bool) {
	if disp == nil {
		return
	}
	ensureRowBuf()
	// Precompute the RGB565 representation of the row background so
	// FillSolidColor can run as a single tight loop over the underlying
	// byte buffer instead of per-pixel SetPixel.
	bgPx := pixel.NewColor[pixel.RGB565BE](dashBG.R, dashBG.G, dashBG.B)
	// Glyph baseline lives inside the row buffer at this y. Mirrors the
	// previous on-panel layout (baseY-dashLineH+8 was the rect top), so
	// row 0's baseline maps to dashLineH-8 inside the buffer.
	baseInBuf := dashLineH - 8
	row := func(idx int16, text string, fg color.RGBA) {
		if lastRowText[idx] == text {
			return // unchanged — skip the SPI burst entirely
		}
		lastRowText[idx] = text
		rowBuf.FillSolidColor(bgPx)
		tinyfont.WriteLine(rowCanvas{}, dashFont, dashX, baseInBuf, text, fg)
		// Ship the whole row in one transaction so the panel never shows
		// the intermediate "all-black" state — the row content is replaced
		// in a single SPI sweep.
		topY := panelRowOffset + dashY0 - dashLineH + 8 + idx*dashLineH
		_ = disp.DrawBitmap(0, topY, rowBuf)
	}
	tStr, hStr, vStr, iStr := "--.- C", "--.- %", "-----", "--- WARM"
	tFG, hFG, vFG, iFG := colorNA, colorNA, colorNA, colorNA
	if haveTH {
		tStr = fmtF(tempC, 1) + " C"
		hStr = fmtF(humPct, 1) + " %"
		tFG, hFG = colorTemp, colorHum
	}
	if haveVOC {
		vStr = strconv.FormatUint(uint64(vocRaw), 10)
		vFG = vocColor(vocRaw)
	}
	if haveIdx {
		iStr = strconv.FormatInt(int64(vocVal), 10) + " " + vocIndexLabel(vocVal)
		iFG = vocIndexColor(vocVal)
	}
	row(0, "T "+tStr, tFG)
	row(1, "H "+hStr, hFG)
	row(2, "V "+vStr, vFG)
	row(3, "I "+iStr, iFG)
}

// setupAHT20 attaches the ASAIR AHT20 on I²C0 using the TinyGo stock
// driver (init command 0xBE; AHT10's 0xE1 does not work on the silicon
// in our "AHT10"-labeled module). A nil rh disables the downstream
// readout so the firmware still boots cleanly if the chip never
// responds.
func setupAHT20() {
	if err := machine.I2C0.Configure(machine.I2CConfig{
		Frequency: i2cFreqHz,
		SDA:       pinSDA,
		SCL:       pinSCL,
	}); err != nil {
		say("i2c configure err: " + err.Error())
		return
	}
	time.Sleep(100 * time.Millisecond)

	dev := aht20.New(machine.I2C0)
	dev.Configure()

	// Confirm with a probe Read — the aht20 driver swallows Tx errors
	// silently in Configure, so the only honest way to check it actually
	// talked to a chip is to try a measurement.
	if err := dev.Read(); err != nil {
		say("aht20 read err: " + err.Error())
		return
	}
	rh = &dev
	say("aht20: ready  temp=" + fmtF(dev.Celsius(), 1) + "C rh=" + fmtF(dev.RelHumidity(), 1) + "%")
}

// setupSGP40 attaches the SGP40 on the I²C0 bus already brought up by
// setupAHT20. Order matters: AHT20 must finish init first (the on-bench
// module otherwise interferes with SGP40's first transaction). Empty voc
// disables the readout downstream so SGP40-less builds keep working.
func setupSGP40() {
	dev := sgp40.New(machine.I2C0)
	if err := dev.Configure(); err != nil {
		say("sgp40 init err: " + err.Error())
		return
	}
	voc = dev
	// Sensirion's Gas Index Algorithm assumes 1 Hz input — the main loop
	// already polls at that cadence (500 ms tick, every other tick).
	vocIdx = vocindex.New()

	if sn, err := dev.SerialNumber(); err != nil {
		say("sgp40 serial err: " + err.Error())
	} else {
		say("sgp40 serial: " + strconv.FormatUint(sn, 16))
	}
}

// setupLED initializes the on-board WS2812 and drives several all-zero
// frames so the LED is dark regardless of whatever the previous firmware
// (or a soft reset) left latched in the controller. One frame is often
// enough, but a soft-reset path keeps the previous color on the chip's
// internal register, and a single bit-bang occasionally loses sync with
// the chip's >50 us reset gap on the first try — repeating with a clear
// gap between attempts is much more reliable. ledEnabled defaults to
// false, so once we've forced black nothing else writes to the strip.
func setupLED() {
	ledPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	ledPin.Low()
	time.Sleep(1 * time.Millisecond)

	strip := ws2812.NewWS2812(ledPin)
	strip.SetBrightness(brightness)
	ledStrip = &strip
	ledPixels = make([]color.RGBA, numPixels)

	for k := 0; k < 5; k++ {
		_ = ledStrip.WriteColors(ledPixels)
		time.Sleep(1 * time.Millisecond)
	}
	say("led: forced off (" + strconv.Itoa(5) + " black frames)")
}

// cycleLED writes the i'th palette color to the strip and logs the name.
// No-op when the LED is disabled or never initialized — keeps the main loop
// branch-free.
func cycleLED(i int) {
	if !ledEnabled || ledStrip == nil {
		return
	}
	idx := i % len(ledPalette)
	c := ledPalette[idx]
	for j := range ledPixels {
		ledPixels[j] = c
	}
	if err := ledStrip.WriteColors(ledPixels); err != nil {
		say("write err: " + err.Error())
		return
	}
	say(strconv.Itoa(i) + ": " + ledNames[idx])
}

// enableLED re-enables the cycle. The next cycleLED tick paints a fresh
// color, so no explicit refresh is needed here.
func enableLED() {
	ledEnabled = true
}

// disableLED stops the cycle and blanks the strip. WS2812 is a bit-banged
// protocol with sub-microsecond timing; once the WiFi radio is active the
// bus interrupts corrupt frames and the LED tends to latch on whatever
// garbled value happened to clock through last (in this project, green).
// Disabling after WiFi comes up keeps the indication consistent.
func disableLED() {
	ledEnabled = false
	if ledStrip == nil {
		return
	}
	for j := range ledPixels {
		ledPixels[j] = color.RGBA{}
	}
	_ = ledStrip.WriteColors(ledPixels)
}

// setupWiFi brings up the ESP32-S3 native radio via espradio's netlink
// implementation and binds it as the default netdev for net/http. Skipped
// when no SSID was injected at build time so the board still works as a
// standalone display + sensor.
func setupWiFi() {
	if ssid == "" {
		say("wifi: skipped (no SSID at build time)")
		return
	}
	link := &espnl.Esplink{}
	netdev.UseNetdev(link)

	say("wifi: connecting to " + ssid)
	if err := link.NetConnect(&nl.ConnectParams{
		Ssid:       ssid,
		Passphrase: password,
	}); err != nil {
		say("wifi err: " + err.Error())
		return
	}
	if addr, err := link.GetHardwareAddr(); err == nil {
		say("wifi mac: " + addr.String())
	}
	say("wifi: connected")
}

// setupPusher builds the full Pushgateway URL once and keeps a Pusher in
// the package var when WiFi + pushURL are usable. A nil pusher disables
// every Push call downstream so the rest of the loop stays unconditional.
func setupPusher() {
	if pushURL == "" {
		say("push: disabled (no PUSH_URL)")
		return
	}
	full := pushURL + "/metrics/job/" + pushJob + "/instance/" + pushInstance
	pusher = pushgateway.New(full)
	say("push: " + full)
}

// fmtF is a small helper for formatting a float32 with a fixed precision —
// strconv.FormatFloat with these args otherwise gets repeated several times
// in the metric body.
func fmtF(v float32, prec int) string {
	return strconv.FormatFloat(float64(v), 'f', prec, 32)
}

// sgp40Compensation returns the humidity / temperature pair that should be
// fed into MeasureRawCompensated. When the T/RH sensor has not produced a
// valid reading yet, it falls back to the SGP40 default 50 %RH / 25 °C.
// When values are present, they are clamped to the datasheet range so we
// never hand the driver an out-of-range compensation value.
func sgp40Compensation(tempC, humPct float32, haveTH bool) (float32, float32) {
	if !haveTH {
		return 25.0, 50.0
	}
	if tempC < -45.0 {
		tempC = -45.0
	} else if tempC > 130.0 {
		tempC = 130.0
	}
	if humPct < 0.0 {
		humPct = 0.0
	} else if humPct > 100.0 {
		humPct = 100.0
	}
	return tempC, humPct
}

// sampleAndMaybePush runs one measurement cycle: AHT20 first (so its T/RH
// can compensate the SGP40 measurement), then SGP40. Console + TFT always
// see the latest values. On every pushIntervalTicks-th invocation the
// collected metrics are concatenated into a single Pushgateway POST body
// to keep the lneto TCP socket pool from being hammered. Either sensor
// being nil only drops its own lines from the body.
func sampleAndMaybePush(i int) {
	var (
		haveTH        bool
		tempC, humPct float32
		haveVOC       bool
		vocRaw        uint16
		// The VOC Index is computed on every sample so the estimator sees the
		// uniform 1 Hz cadence its dynamics assume; haveIdx flips on once the
		// 45 s initial blackout has elapsed so we don't push a misleading 0.
		haveIdx bool
		vocVal  int32
	)

	if rh != nil {
		if err := rh.Read(); err != nil {
			say("aht20 err: " + err.Error())
		} else {
			tempC, humPct, haveTH = rh.Celsius(), rh.RelHumidity(), true
			say("temp: " + fmtF(tempC, 1) + "C  rh: " + fmtF(humPct, 1) + "%")
		}
	}

	if voc != nil {
		compT, compRH := sgp40Compensation(tempC, humPct, haveTH)
		raw, err := voc.MeasureRawCompensated(compRH, compT)
		if err != nil {
			say("voc err: " + err.Error())
		} else {
			vocRaw, haveVOC = raw, true
			// Capture the blackout flag BEFORE Process advances the internal
			// uptime — otherwise the sample that exits blackout returns 0 yet
			// InBlackout() already reads false, which would mis-tag it as oob.
			wasBlackout := vocIdx != nil && vocIdx.InBlackout()
			if vocIdx != nil {
				vocVal = vocIdx.Process(int32(raw))
			}
			rawStr := strconv.FormatUint(uint64(raw), 10)
			switch {
			case vocIdx == nil:
				say("voc raw: " + rawStr + "  voc index: (disabled)")
			case wasBlackout:
				// Sensirion blackout: 45 calls return 0. Show samples
				// remaining so the user sees the countdown, not a frozen log.
				left := 45 - int32(vocIdx.Uptime())
				if left < 0 {
					left = 0
				}
				say("voc raw: " + rawStr +
					"  voc index: warming (" + strconv.FormatInt(int64(left), 10) + "s left)")
			case vocVal > 0:
				haveIdx = true
				say("voc raw: " + rawStr +
					"  voc index: " + strconv.FormatInt(int64(vocVal), 10))
			default:
				// Past blackout but Process still returned 0 — in this
				// port the only branch that does that is the out-of-band
				// sraw guard, so flag the sensor as the likely culprit.
				say("voc raw: " + rawStr + "  voc index: 0 (sraw oob?)")
			}
		}
	}

	// Sampling cadence is 1 Hz (Sensirion algorithm dynamics) and the
	// dashboard rides the same cadence now that the row backbuffer
	// eliminates the per-row flash — DrawBitmap replaces the row in a
	// single SPI sweep, and unchanged rows are skipped entirely by the
	// lastRowText cache, so 1 Hz refresh costs nothing visually.
	drawDashboard(tempC, humPct, vocRaw, vocVal, haveTH, haveVOC, haveIdx)

	// vocIdx.Process must run on every sample (1 Hz) to keep the Sensirion
	// algorithm's learned dynamics intact; the network push, by contrast, is
	// rate-limited by the small lneto TCP socket pool — so the two cadences
	// are split: sampling runs every call, pushing only every pushIntervalTicks.
	if pusher == nil || i%pushIntervalTicks != 0 {
		return
	}
	body := ""
	if haveTH {
		body += "aht20_temperature_celsius " + fmtF(tempC, 2) + "\n"
		body += "aht20_humidity_percent " + fmtF(humPct, 2) + "\n"
	}
	if haveVOC {
		body += "sgp40_voc_raw " + strconv.FormatUint(uint64(vocRaw), 10) + "\n"
	}
	if haveIdx {
		body += "sgp40_voc_index " + strconv.FormatInt(int64(vocVal), 10) + "\n"
	}
	if body == "" {
		return
	}
	if err := pusher.Push(body); err != nil {
		sayColor("push err: "+err.Error(), colorPushErr)
		return
	}
	msg := "pushed @ i=" + strconv.Itoa(i)
	if haveTH {
		msg += " T=" + fmtF(tempC, 1) + "C RH=" + fmtF(humPct, 1) + "%"
	}
	if haveVOC {
		msg += " voc=" + strconv.FormatUint(uint64(vocRaw), 10)
	}
	sayColor(msg, colorPushOK)
}

func main() {
	time.Sleep(2 * time.Second)

	setupDisplay()
	say("ST7789V3 + SGP40 + AHT20")
	say("---------------------------------")
	// AHT20 first: bus init + sensor handshake completes before SGP40
	// touches I²C0. Reversing the order leaves AHT20's first transaction
	// racing SGP40's and is what tripped the earlier debug session.
	setupAHT20()
	setupSGP40()
	setupLED()
	setupWiFi()
	setupPusher()

	if freq, err := machine.GetCPUFrequency(); err != nil {
		say("cpu freq err: " + err.Error())
	} else {
		say("cpu freq: " + strconv.FormatUint(uint64(freq), 10))
	}

	// Drop the scrolling boot console; the TFT becomes a static dashboard
	// from here on. Serial keeps receiving every say() call.
	switchToDashboard()
	drawDashboard(0, 0, 0, 0, false, false, false)

	for i := 0; ; i++ {
		cycleLED(i)
		// Sampling at 1 Hz (every other 500 ms tick) is required by the
		// Sensirion Gas Index Algorithm to keep its learned baseline /
		// variance estimator's dynamics correct. The pushgateway POST is
		// rate-limited separately inside sampleAndMaybePush so the small
		// lneto TCP socket pool doesn't get exhausted.
		if i%2 == 0 {
			sampleAndMaybePush(i)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
