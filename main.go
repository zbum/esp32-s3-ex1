package main

import (
	"image/color"
	"machine"
	"time"

	"esp32-s3-ex1/internal/ws2812"
)

const (
	ledPin     = machine.GPIO48
	numPixels  = 1
	brightness = 32
)

func main() {
	time.Sleep(2 * time.Second)

	if freq, err := machine.GetCPUFrequency(); err != nil {
		println("cpu freq err:", err.Error())
	} else {
		println("cpu freq:", freq)
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

	for i := 0; ; i++ {
		c := palette[i%len(palette)]
		for j := range pixels {
			pixels[j] = c
		}
		if err := strip.WriteColors(pixels); err != nil {
			println("write err:", err.Error())
		} else {
			println("wrote color idx:", i%len(palette))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
