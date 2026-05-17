// Package sgp40 is a small TinyGo driver for the Sensirion SGP40 indoor-air
// VOC sensor. It speaks the raw I2C protocol described in the SGP40 datasheet:
//
//	https://sensirion.com/media/documents/296373BB/6203C5DF/Sensirion_Gas_Sensors_Datasheet_SGP40.pdf
//
// The sensor exposes a single raw VOC tick value (uint16) read with the
// measure_raw_signal command (0x260F). To translate ticks into the Sensirion
// "VOC Index" (0-500), the host must additionally run the Sensirion Gas Index
// Algorithm — that algorithm is not implemented here; only the raw value and
// the housekeeping commands (self-test, serial number, heater off) are.
package sgp40

import (
	"errors"
	"time"

	"tinygo.org/x/drivers"
)

// Address is the fixed 7-bit I2C address of the SGP40.
const Address = 0x59

var (
	// ErrCRC is returned when the CRC byte on a sensor response does not
	// match the data bytes — usually a wiring or bus-noise problem.
	ErrCRC = errors.New("sgp40: invalid CRC")
	// ErrSelfTest is returned when the on-chip self-test reports failure.
	ErrSelfTest = errors.New("sgp40: self-test failed")
)

const (
	cmdMeasureRaw  = 0x260F
	cmdMeasureTest = 0x280E
	cmdHeaterOff   = 0x3615
	cmdSerial      = 0x3682

	// Maximum execution times from the datasheet, with a small safety margin.
	measureRawDelay  = 30 * time.Millisecond
	measureTestDelay = 320 * time.Millisecond
	shortCmdDelay    = 1 * time.Millisecond
)

// Device is a single SGP40 sensor on an I2C bus.
type Device struct {
	bus     drivers.I2C
	cmdBuf  [8]byte
	respBuf [9]byte
}

// New creates an unconfigured Device. Call Configure before reading.
func New(bus drivers.I2C) *Device {
	return &Device{bus: bus}
}

// Configure waits out the sensor's 0.6 ms power-up time and turns the
// hotplate off so the sensor is in a known idle state until the first
// measurement. It does not run a self-test — call SelfTest separately if
// you want one.
func (d *Device) Configure() error {
	time.Sleep(1 * time.Millisecond)
	return d.HeaterOff()
}

// MeasureRaw issues a measurement with the default 50 %RH / 25 °C
// compensation and returns the raw 16-bit VOC tick.
func (d *Device) MeasureRaw() (uint16, error) {
	return d.MeasureRawCompensated(50.0, 25.0)
}

// MeasureRawCompensated issues a measurement using the supplied relative
// humidity (%) and temperature (°C) compensation values and returns the raw
// 16-bit VOC tick. Compensation is the sensor's recommended way to reduce
// humidity / temperature cross-sensitivity; if you do not have those
// measurements use MeasureRaw.
func (d *Device) MeasureRawCompensated(rh, t float32) (uint16, error) {
	rhTicks := uint16(rh / 100 * 65535)
	tTicks := uint16((t + 45) / 175 * 65535)

	d.cmdBuf[0] = byte(cmdMeasureRaw >> 8)
	d.cmdBuf[1] = byte(cmdMeasureRaw & 0xff)
	d.cmdBuf[2] = byte(rhTicks >> 8)
	d.cmdBuf[3] = byte(rhTicks & 0xff)
	d.cmdBuf[4] = crc8(d.cmdBuf[2:4])
	d.cmdBuf[5] = byte(tTicks >> 8)
	d.cmdBuf[6] = byte(tTicks & 0xff)
	d.cmdBuf[7] = crc8(d.cmdBuf[5:7])

	if err := d.bus.Tx(Address, d.cmdBuf[:8], nil); err != nil {
		return 0, err
	}
	time.Sleep(measureRawDelay)

	resp := d.respBuf[:3]
	if err := d.bus.Tx(Address, nil, resp); err != nil {
		return 0, err
	}
	if crc8(resp[:2]) != resp[2] {
		return 0, ErrCRC
	}
	return uint16(resp[0])<<8 | uint16(resp[1]), nil
}

// HeaterOff stops the hotplate and puts the sensor into idle. Useful for
// reducing power when measurements are not needed.
func (d *Device) HeaterOff() error {
	d.cmdBuf[0] = byte(cmdHeaterOff >> 8)
	d.cmdBuf[1] = byte(cmdHeaterOff & 0xff)
	if err := d.bus.Tx(Address, d.cmdBuf[:2], nil); err != nil {
		return err
	}
	time.Sleep(shortCmdDelay)
	return nil
}

// SelfTest runs the on-chip self test (~250 ms) and returns ErrSelfTest if
// the sensor reports a failure.
func (d *Device) SelfTest() error {
	d.cmdBuf[0] = byte(cmdMeasureTest >> 8)
	d.cmdBuf[1] = byte(cmdMeasureTest & 0xff)
	if err := d.bus.Tx(Address, d.cmdBuf[:2], nil); err != nil {
		return err
	}
	time.Sleep(measureTestDelay)

	resp := d.respBuf[:3]
	if err := d.bus.Tx(Address, nil, resp); err != nil {
		return err
	}
	if crc8(resp[:2]) != resp[2] {
		return ErrCRC
	}
	val := uint16(resp[0])<<8 | uint16(resp[1])
	if val != 0xD400 {
		return ErrSelfTest
	}
	return nil
}

// SerialNumber reads the 48-bit chip serial number.
func (d *Device) SerialNumber() (uint64, error) {
	d.cmdBuf[0] = byte(cmdSerial >> 8)
	d.cmdBuf[1] = byte(cmdSerial & 0xff)
	if err := d.bus.Tx(Address, d.cmdBuf[:2], nil); err != nil {
		return 0, err
	}
	time.Sleep(shortCmdDelay)

	resp := d.respBuf[:9]
	if err := d.bus.Tx(Address, nil, resp); err != nil {
		return 0, err
	}
	var sn uint64
	for word := 0; word < 3; word++ {
		off := word * 3
		if crc8(resp[off:off+2]) != resp[off+2] {
			return 0, ErrCRC
		}
		sn = sn<<16 | uint64(resp[off])<<8 | uint64(resp[off+1])
	}
	return sn, nil
}

// crc8 is the Sensirion CRC-8 (poly 0x31, init 0xff) used on every data word.
func crc8(data []byte) byte {
	crc := byte(0xff)
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x31
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
