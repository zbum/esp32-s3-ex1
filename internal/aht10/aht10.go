// Package aht10 is a small TinyGo driver for the ASAIR AHT10 temperature
// + humidity sensor. It speaks the raw I2C protocol described in the AHT10
// datasheet:
//
//	http://www.aosong.com/userfiles/files/media/Data%20Sheet%20AHT10.pdf
//
// Compared to AHT20, AHT10 differs in three places:
//
//   - init command is 0xE1 (AHT20 uses 0xBE)
//   - status is read as the first byte of any read; there is no 0x71 command
//   - the measurement response is 6 bytes (AHT20 returns 7 including CRC)
//
// The tinygo aht20 driver assumes the AHT20 shape, so a dedicated AHT10
// driver lives here instead of trying to reuse it.
package aht10

import (
	"errors"
	"time"

	"tinygo.org/x/drivers"
)

// Address is the fixed 7-bit I2C address of the AHT10.
const Address = 0x38

var (
	// ErrBusy is returned when the status byte's busy bit is still set after
	// the worst-case measurement wait. Caller should retry.
	ErrBusy = errors.New("aht10: sensor busy")
	// ErrNotCalibrated is returned when the calibrated bit in the status
	// byte stays clear after Configure — usually a wiring or power problem.
	ErrNotCalibrated = errors.New("aht10: not calibrated")
)

const (
	cmdInit      = 0xE1
	cmdMeasure   = 0xAC
	cmdSoftReset = 0xBA

	initArg1    = 0x08
	initArg2    = 0x00
	measureArg1 = 0x33
	measureArg2 = 0x00

	statusBusy  = 1 << 7
	statusCalOK = 1 << 3

	// Datasheet timings with a small safety margin.
	resetDelay   = 20 * time.Millisecond
	initDelay    = 10 * time.Millisecond
	measureDelay = 80 * time.Millisecond
)

// Device is a single AHT10 sensor on an I2C bus.
type Device struct {
	bus     drivers.I2C
	cmdBuf  [3]byte
	respBuf [6]byte
}

// New creates an unconfigured Device. The caller must configure the I2C bus
// first; Configure must then be called before any Measure.
func New(bus drivers.I2C) *Device {
	return &Device{bus: bus}
}

// Configure issues a soft reset and then the init/calibrate command so the
// chip loads its calibration registers. Returns ErrNotCalibrated when the
// status read-back never reports the calibrated bit.
func (d *Device) Configure() error {
	d.cmdBuf[0] = cmdSoftReset
	if err := d.bus.Tx(Address, d.cmdBuf[:1], nil); err != nil {
		return err
	}
	time.Sleep(resetDelay)

	d.cmdBuf[0] = cmdInit
	d.cmdBuf[1] = initArg1
	d.cmdBuf[2] = initArg2
	if err := d.bus.Tx(Address, d.cmdBuf[:3], nil); err != nil {
		return err
	}
	time.Sleep(initDelay)

	status, err := d.Status()
	if err != nil {
		return err
	}
	if status&statusCalOK == 0 {
		return ErrNotCalibrated
	}
	return nil
}

// Status reads the 1-byte status register. AHT10 returns the status byte as
// the first byte of any read, so this is a single-byte I2C read with no
// command prefix.
func (d *Device) Status() (byte, error) {
	resp := d.respBuf[:1]
	if err := d.bus.Tx(Address, nil, resp); err != nil {
		return 0, err
	}
	return resp[0], nil
}

// Measure triggers a single temperature + humidity measurement and returns
// (tempC, humidityPct). The datasheet specifies 75 ms measurement time; the
// driver waits 80 ms and then reads the 6-byte response.
func (d *Device) Measure() (tempC, humidityPct float32, err error) {
	d.cmdBuf[0] = cmdMeasure
	d.cmdBuf[1] = measureArg1
	d.cmdBuf[2] = measureArg2
	if err = d.bus.Tx(Address, d.cmdBuf[:3], nil); err != nil {
		return 0, 0, err
	}
	time.Sleep(measureDelay)

	resp := d.respBuf[:6]
	if err = d.bus.Tx(Address, nil, resp); err != nil {
		return 0, 0, err
	}
	if resp[0]&statusBusy != 0 {
		return 0, 0, ErrBusy
	}

	// Humidity occupies the upper 20 bits of resp[1..3]; the high nibble of
	// resp[3] belongs to humidity, the low nibble to temperature.
	rawH := uint32(resp[1])<<12 | uint32(resp[2])<<4 | uint32(resp[3])>>4
	rawT := (uint32(resp[3])&0x0F)<<16 | uint32(resp[4])<<8 | uint32(resp[5])

	const scale = float32(1 << 20)
	humidityPct = float32(rawH) * 100.0 / scale
	tempC = float32(rawT)*200.0/scale - 50.0
	return tempC, humidityPct, nil
}
