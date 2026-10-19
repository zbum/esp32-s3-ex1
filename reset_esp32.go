package main

import "device/esp"

// espRestart requests a full digital-system reset through the ESP32-S3 RTC
// controller. TinyGo's ESP32-S3 target does not expose machine.CPUReset, and
// espradio does not link ESP-IDF's esp_restart implementation.
func espRestart() {
	esp.RTC_CNTL.SetOPTIONS0_SW_SYS_RST(1)
	for {
	}
}
