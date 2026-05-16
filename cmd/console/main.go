// Demo: render scrolling text on a 1.69" 240x280 ST7789V3 panel.
//
// Wiring (override the constants below for your own pin map):
//
//	SCK  -> GPIO12   (SPI clock)
//	MOSI -> GPIO11   (SDO / data out)
//	DC   -> GPIO10   (data/command)
//	CS   -> GPIO9    (chip select)
//	RST  -> GPIO8    (reset, active low)
//	BL   -> GPIO7    (backlight)
//	VCC  -> 3V3
//	GND  -> GND
package main

import (
	"image/color"
	"machine"
	"strconv"
	"time"

	"esp32-s3-ex1/internal/console"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont/freemono"
)

const (
	pinSCK  = machine.GPIO12
	pinMOSI = machine.GPIO11
	pinDC   = machine.GPIO10
	pinCS   = machine.GPIO9
	pinRST  = machine.GPIO8
	pinBL   = machine.GPIO7

	spiFreqHz = 40_000_000

	// 1.69" Waveshare-style ST7789V3 panel.
	panelWidth     = 240
	panelHeight    = 280
	panelRowOffset = 20

	// FreeMono 9pt7b metrics: glyph cell 11x15, baseline 11 from cell top.
	lineHeight  = 15
	baselineY   = 11
	charAdvance = 11
)

func main() {
	time.Sleep(2 * time.Second)

	bus := machine.SPI0 // ESP32-S3 FSPI (hardware SPI2)
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
		Width:     panelWidth,
		Height:    panelHeight,
		RowOffset: panelRowOffset,
	})

	term := console.New(&disp, &freemono.Regular9pt7b, console.Config{
		Width:       panelWidth,
		Height:      panelHeight,
		LineHeight:  lineHeight,
		Baseline:    baselineY,
		CharAdvance: charAdvance,
		Foreground:  color.RGBA{R: 220, G: 220, B: 220, A: 255},
		Background:  color.RGBA{R: 0, G: 0, B: 0, A: 255},
	})
	if term == nil {
		println("console.New returned nil — bad Config")
		return
	}
	term.Init()

	term.Println("ST7789V3 console demo")
	term.Println("---------------------")
	term.Println("rows=" + strconv.Itoa(term.Rows()) + " cols=" + strconv.Itoa(term.Cols()))
	term.Println("scrolling on overflow:")

	for i := 0; ; i++ {
		line := "line " + strconv.Itoa(i)
		term.Println(line)
		println(line)
		time.Sleep(400 * time.Millisecond)
	}
}
