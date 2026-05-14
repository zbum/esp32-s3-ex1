TARGET     ?= esp32s3-generic
CHIP       ?= esp32s3
PORT       ?= /dev/cu.usbmodem2101
OUT        ?= build/firmware.bin
BAUD       ?= 460800
MON_BAUD   ?= 115200
RESET      ?= default

ESPFLASHER ?= $(shell go env GOPATH)/bin/espflasher

SSID       ?=
PASSWORD   ?=
SERVER_URL ?= http://192.168.0.10:8080/sensor
DEVICE_ID  ?= esp32-s3-ex1

LDFLAGS =
ifneq ($(strip $(SSID)),)
LDFLAGS = -ldflags="\
 -X main.ssid=$(SSID) \
 -X main.password=$(PASSWORD) \
 -X main.serverURL=$(SERVER_URL) \
 -X main.deviceID=$(DEVICE_ID)"
endif

.PHONY: build flash monitor all clean test help

help:
	@echo "Targets:"
	@echo "  build     - compile firmware to $(OUT)"
	@echo "  flash     - flash firmware to board on $(PORT)"
	@echo "  monitor   - open serial monitor on $(PORT)"
	@echo "  all       - build, flash, then monitor"
	@echo "  clean     - remove build artifacts"
	@echo "  test      - run host-side go tests"
	@echo ""
	@echo "Variables (override with VAR=value):"
	@echo "  TARGET=$(TARGET)   CHIP=$(CHIP)"
	@echo "  PORT=$(PORT)"
	@echo "  BAUD=$(BAUD)   MON_BAUD=$(MON_BAUD)   RESET=$(RESET)"
	@echo "  SSID, PASSWORD, SERVER_URL, DEVICE_ID"

build:
	@mkdir -p $(dir $(OUT))
	tinygo build -target $(TARGET) $(LDFLAGS) -o $(OUT) .

flash: build
	$(ESPFLASHER) -port $(PORT) -chip $(CHIP) -baud $(BAUD) -reset $(RESET) $(OUT)

monitor:
	tinygo monitor -port $(PORT) -baudrate $(MON_BAUD)

all: flash monitor

clean:
	rm -rf build

test:
	go test ./...
