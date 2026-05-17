// Default sketch: cycle the on-board WS2812 through a palette, poll the
// Sensirion SGP40 VOC sensor over I2C, mirror every status line to both the
// USB serial console and a 1.69" 240x280 ST7789V3 TFT, and (when WiFi
// credentials are supplied at build time) push each SGP40 raw tick to a
// Prometheus Pushgateway.
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
// Wiring for the SGP40 breakout (see docs/sgp40-wiring.md):
//
//	SDA -> GPIO5   I2C0 data
//	SCL -> GPIO6   I2C0 clock
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
	pusher *pushgateway.Pusher
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

func main() {
	time.Sleep(2 * time.Second)

	setupDisplay()
	say("ST7789V3 + WS2812 + SGP40")
	say("-------------------------")
	setupSGP40()
	setupWiFi()
	setupPusher()

	if freq, err := machine.GetCPUFrequency(); err != nil {
		say("cpu freq err: " + err.Error())
	} else {
		say("cpu freq: " + strconv.FormatUint(uint64(freq), 10))
	}

	ledPin.Configure(machine.PinConfig{Mode: machine.PinOutput})

	strip := ws2812.NewWS2812(ledPin)
	strip.SetBrightness(brightness)

	pixels := make([]color.RGBA, numPixels)
	palette := []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
		{R: 255, G: 255, B: 0, A: 255},
		{R: 0, G: 255, B: 255, A: 255},
		{R: 255, G: 0, B: 255, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
	}
	names := []string{"red", "green", "blue", "yellow", "cyan", "magenta", "white"}

	for i := 0; ; i++ {
		idx := i % len(palette)
		c := palette[idx]
		for j := range pixels {
			pixels[j] = c
		}
		if err := strip.WriteColors(pixels); err != nil {
			say("write err: " + err.Error())
		} else {
			say(strconv.Itoa(i) + ": " + names[idx])
		}
		// Poll the SGP40 once per second (every other palette tick) — the
		// sensor's gas-index calibration assumes 1 Hz sampling.
		if voc != nil && i%2 == 0 {
			if raw, err := voc.MeasureRaw(); err != nil {
				say("voc err: " + err.Error())
			} else {
				rawStr := strconv.FormatUint(uint64(raw), 10)
				say("voc raw: " + rawStr)
				if pusher != nil {
					if err := pusher.Push("sgp40_voc_raw " + rawStr); err != nil {
						say("push err: " + err.Error())
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}
