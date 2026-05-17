// Default sketch: cycle the on-board WS2812 through a palette and mirror every
// status line to both the USB serial console and a 1.69" 240x280 ST7789V3 TFT.
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
package main

import (
	"image/color"
	"machine"
	"strconv"
	"time"

	"esp32-s3-ex1/internal/console"
	"esp32-s3-ex1/internal/ws2812"

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

	spiFreqHz = 40_000_000

	panelWidth   = 240
	panelHeight  = 280
	lineHeight   = 15
	baselineY    = 11
	charAdvance  = 11
)

var term *console.Console

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
	disp.Configure(st7789.Config{
		Width:  panelWidth,
		Height: panelHeight,
	})

	t := console.New(&disp, &freemono.Regular9pt7b, console.Config{
		Width:       panelWidth,
		Height:      panelHeight,
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

func main() {
	time.Sleep(2 * time.Second)

	setupDisplay()
	say("ST7789V3 console + WS2812 demo")
	say("------------------------------")

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
		time.Sleep(500 * time.Millisecond)
	}
}
