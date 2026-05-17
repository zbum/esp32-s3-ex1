// Default sketch: poll the Sensirion SGP40 VOC sensor and the ASAIR AHT10
// temperature + humidity sensor over a shared I2C bus, mirror every status
// line to both the USB serial console and a 1.69" 240x280 ST7789V3 TFT, and
// (when WiFi credentials are supplied at build time) push the latest sample
// of all three signals to a Prometheus Pushgateway in one POST.
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
// Wiring for the SGP40 + AHT10 breakouts (see docs/sgp40-wiring.md and
// docs/aht10-wiring.md). Both sensors live on the same I2C0 bus — distinct
// addresses (SGP40=0x59, AHT10=0x38) keep them from colliding.
//
//	SDA -> GPIO5   I2C0 data  (shared)
//	SCL -> GPIO6   I2C0 clock (shared)
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

	"esp32-s3-ex1/internal/aht10"
	"esp32-s3-ex1/internal/console"
	"esp32-s3-ex1/internal/pushgateway"
	"esp32-s3-ex1/internal/sgp40"
	"esp32-s3-ex1/internal/ws2812"

	"tinygo.org/x/drivers/netdev"
	nl "tinygo.org/x/drivers/netlink"
	espnl "tinygo.org/x/espradio/netlink"

	"tinygo.org/x/drivers/st7789"
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

	pinSDA = machine.GPIO5
	pinSCL = machine.GPIO6

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
	pushInstance = "sgp40"
)

var (
	term   *console.Console
	voc    *sgp40.Device
	rh     *aht10.Device
	pusher *pushgateway.Pusher

	ledStrip   *ws2812.Device
	ledPixels  []color.RGBA
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

	disp := st7789.New(bus, pinRST, pinDC, pinCS, pinBL)
	// Tell the driver the controller's full GRAM size; the console handles
	// the panel rowstart itself because the driver zeroes it at Rotation0.
	disp.Configure(st7789.Config{
		Width:  gramWidth,
		Height: gramHeight,
	})

	t := console.New(&disp, &freemono.Regular9pt7b, console.Config{
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

// setupSGP40 also brings up the shared I2C0 bus (GPIO5/6). AHT10 sits on
// the same bus at a different address, so setupAHT10 can run afterwards
// without touching the bus config.
func setupSGP40() {
	if err := machine.I2C0.Configure(machine.I2CConfig{
		Frequency: i2cFreqHz,
		SDA:       pinSDA,
		SCL:       pinSCL,
	}); err != nil {
		say("i2c configure err: " + err.Error())
		return
	}

	dev := sgp40.New(machine.I2C0)
	if err := dev.Configure(); err != nil {
		say("sgp40 init err: " + err.Error())
		return
	}
	voc = dev

	if sn, err := dev.SerialNumber(); err != nil {
		say("sgp40 serial err: " + err.Error())
	} else {
		say("sgp40 serial: " + strconv.FormatUint(sn, 16))
	}
}

// setupAHT10 attaches the ASAIR AHT10 temperature/humidity sensor on the
// same I2C0 bus already brought up by setupSGP40. A nil rh disables the
// AHT10 readout downstream so SGP40-only builds keep working.
func setupAHT10() {
	dev := aht10.New(machine.I2C0)
	if err := dev.Configure(); err != nil {
		say("aht10 init err: " + err.Error())
		return
	}
	rh = dev
	say("aht10: ready")
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

// sampleAndMaybePush runs one measurement cycle: AHT10 first (so its T/RH
// can compensate the SGP40 measurement), then SGP40. Console + TFT always
// see the latest values. On every pushIntervalTicks-th invocation the
// collected metrics are concatenated into a single Pushgateway POST body,
// which keeps the lneto TCP socket pool from being hammered.
//
// Partial-sensor configurations stay valid: if either rh or voc is nil the
// corresponding metric lines are simply omitted from the body, and SGP40
// falls back to the datasheet's default 50%RH / 25°C compensation.
func sampleAndMaybePush(i int) {
	var (
		haveTH        bool
		tempC, humPct float32
		haveVOC       bool
		vocRaw        uint16
	)

	if rh != nil {
		t, h, err := rh.Measure()
		if err != nil {
			say("aht10 err: " + err.Error())
		} else {
			tempC, humPct, haveTH = t, h, true
			say("temp: " + fmtF(tempC, 1) + "C  rh: " + fmtF(humPct, 1) + "%")
		}
	}

	if voc != nil {
		compT, compRH := float32(25), float32(50)
		if haveTH {
			compT, compRH = tempC, humPct
		}
		raw, err := voc.MeasureRawCompensated(compRH, compT)
		if err != nil {
			say("voc err: " + err.Error())
		} else {
			vocRaw, haveVOC = raw, true
			say("voc raw: " + strconv.FormatUint(uint64(raw), 10))
		}
	}

	if pusher == nil || i%pushIntervalTicks != 0 {
		return
	}
	body := ""
	if haveTH {
		body += "aht10_temperature_celsius " + fmtF(tempC, 2) + "\n"
		body += "aht10_humidity_percent " + fmtF(humPct, 2) + "\n"
	}
	if haveVOC {
		body += "sgp40_voc_raw " + strconv.FormatUint(uint64(vocRaw), 10) + "\n"
	}
	if body == "" {
		return
	}
	if err := pusher.Push(body); err != nil {
		sayColor("push err: "+err.Error(), colorPushErr)
	} else {
		sayColor("pushed @ i="+strconv.Itoa(i), colorPushOK)
	}
}

func main() {
	time.Sleep(2 * time.Second)

	setupDisplay()
	say("ST7789V3 + WS2812 + SGP40 + AHT10")
	say("---------------------------------")
	setupSGP40()
	setupAHT10()
	setupLED()
	setupWiFi()
	setupPusher()

	if freq, err := machine.GetCPUFrequency(); err != nil {
		say("cpu freq err: " + err.Error())
	} else {
		say("cpu freq: " + strconv.FormatUint(uint64(freq), 10))
	}

	for i := 0; ; i++ {
		cycleLED(i)
		// Poll both sensors once per second (every other palette tick) — the
		// SGP40 gas-index calibration assumes 1 Hz sampling and AHT10 is
		// happy at the same cadence.
		//
		// Pushgateway POSTs are gated to once per pushIntervalTicks. The
		// lneto net stack has a small TCP socket pool and each connection
		// sits in TIME_WAIT for tens of seconds, so a 1 Hz push exhausts the
		// pool ("resource exhausted") within a handful of iterations.
		if i%2 == 0 {
			sampleAndMaybePush(i)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
