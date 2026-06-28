# esp32-s3-ex1

ESP32-S3-N8R2 / N16R8 (44핀 USB-C DevKit, 8MB PSRAM) + TinyGo 예제 프로젝트. 현재 동작 모드: ST7789V3 콘솔 + SGP40(VOC) + AHT10/AHT20(온/습도) 1Hz 측정, WiFi 빌드 시 Prometheus Pushgateway 로 세 메트릭 동시 push.

## Board Overview

![ESP32-S3 보드](docs/esp32s3-board.webp)

- 모듈: **ESP32-S3-N8R2** (8MB flash + 2MB PSRAM) 또는 **N16R8** (16MB flash + 8MB PSRAM)
- 44핀 DIP 헤더 (양쪽 22핀)
- USB-C 두 개 — 좌측은 USB-to-UART 브리지, 우측은 ESP32-S3 네이티브 USB(USB-Serial/JTAG)
- BOOT / RST 푸시버튼
- 내장 **WS2812 RGB LED** (GPIO48, 보드 다이어그램의 "RGB" 표시)
- 5V 핀 방향은 솔더 점퍼로 전환 (기본은 5V 입력; 확장보드 급전이 필요하면 점퍼 단락)

## Board PIN Definitions

![ESP32-S3 Pinout](docs/xiao-esp32s3-pinout.webp)

TinyGo 빌드 타겟은 `esp32s3-generic`. 이 타겟에는 `D0`, `LED` 같은 별칭이 정의돼 있지 않으니 코드에서는 항상 `machine.GPIO<N>` 으로 직접 참조한다.

### 좌측 헤더 (모듈 안테나 기준 위→아래)

| 라벨 | GPIO | 비고 |
|------|------|------|
| 3V3  | —    | 3.3V 출력 |
| 3V3  | —    | 3.3V 출력 |
| RST  | —    | 외부 리셋 입력 (active low) |
| 4    | GPIO4  | ADC, Touch |
| 5    | GPIO5  | ADC, Touch |
| 6    | GPIO6  | ADC, Touch |
| 7    | GPIO7  | ADC, Touch |
| 15   | GPIO15 | ADC |
| 16   | GPIO16 | ADC |
| 17   | GPIO17 | ADC |
| 18   | GPIO18 | ADC |
| 8    | GPIO8  | ADC, Touch |
| 3    | GPIO3  | ADC, Touch, 스트래핑 |
| 46   | GPIO46 | 스트래핑 |
| 9    | GPIO9  | ADC, Touch |
| 10   | GPIO10 | ADC, Touch |
| 11   | GPIO11 | ADC, Touch |
| 12   | GPIO12 | ADC, Touch |
| 13   | GPIO13 | ADC, Touch |
| 14   | GPIO14 | ADC, Touch |
| 5V   | —    | USB 5V (기본: 입력) |
| GND  | —    | |

### 우측 헤더 (모듈 안테나 기준 위→아래)

| 라벨 | GPIO | 비고 |
|------|------|------|
| GND  | —    | |
| TX   | GPIO43 | UART0 TX (기본 콘솔) |
| RX   | GPIO44 | UART0 RX (기본 콘솔) |
| 1    | GPIO1  | ADC, Touch |
| 2    | GPIO2  | ADC, Touch |
| 42   | GPIO42 | JTAG TMS |
| 41   | GPIO41 | JTAG TDI |
| 40   | GPIO40 | JTAG TDO |
| 39   | GPIO39 | JTAG TCK |
| 38   | GPIO38 | |
| 37   | GPIO37 | N16R8 모듈에서 옥탈 PSRAM에 점유 — 사용 금지 |
| 36   | GPIO36 | N16R8 모듈에서 옥탈 PSRAM에 점유 — 사용 금지 |
| 35   | GPIO35 | N16R8 모듈에서 옥탈 PSRAM에 점유 — 사용 금지 |
| 0    | GPIO0  | BOOT 스트래핑 (low 시 부트로더 진입) |
| 45   | GPIO45 | 스트래핑 |
| 48   | GPIO48 | **WS2812 RGB LED** (보드 내장) |
| 47   | GPIO47 | |
| 21   | GPIO21 | |
| 20   | GPIO20 | USB D+ (네이티브 USB 사용 시 GPIO로 쓰면 안 됨) |
| 19   | GPIO19 | USB D- (네이티브 USB 사용 시 GPIO로 쓰면 안 됨) |
| GND  | —    | |
| GND  | —    | |

### 사용 주의

- **GPIO19 / GPIO20** — ESP32-S3 네이티브 USB D-/D+. 우측 USB-C로 플래시/모니터 시 일반 GPIO로 사용 불가.
- **GPIO35 / 36 / 37** — N16R8(옥탈 PSRAM) 모듈은 내부 PSRAM 버스에 연결. N8R2(쿼드 PSRAM)는 사용 가능. 모듈 표기 확인 필수.
- **GPIO0** — 부트 스트래핑. 부팅 시 풀-다운하면 ROM 부트로더로 진입. 일반 입력으로 사용 가능하나 부팅 직후 저항/외부 입력에 주의.
- **GPIO48** — 보드 내장 WS2812 RGB LED. 단순 digital toggle로는 동작하지 않으며 WS2812 프로토콜이 필요.

### AHT10 권장 결선 (I²C0, 주소 `0x38`)

| AHT10 핀 | 헤더 라벨 | GPIO |
|----------|-----------|------|
| VDD / VCC | 3V3 | — |
| SDA       | 5   | GPIO5 |
| SCL       | 6   | GPIO6 |
| GND       | GND | — |

> 3.3 V 전용. SGP40(`0x59`) 과 같은 I²C0 버스(GPIO5/6) 공유 가능 — 주소가
> 다르므로 충돌 없음. 풀업 저항은 **버스 전체에 한 쌍만** (브레이크아웃 보드
> 둘 다 풀업 내장이면 한 쪽 제거). 자세한 결선은 [docs/aht10-wiring.md](docs/aht10-wiring.md) 참고.

### AHT10 용도와 기능

- I²C 로 온도와 습도를 읽는 센서다.
- SGP40 과 같은 버스에 붙여 쓰기 좋고, 현재 메인 펌웨어의 온습도 소스가
  이 계열이다.
- 읽은 온도와 습도는 SGP40 `MeasureRawCompensated()` 보정값으로 사용해
  VOC raw tick 의 습도/온도 민감도를 줄인다.
- 동시에 Pushgateway 로 올라가는 온습도 메트릭의 원천이 된다.
- 실제 코드에서는 AHT20 호환 모듈을 사용하지만, 문서상으로는 AHT10/AHT20
  계열로 묶어 설명한다.

### SGP40 권장 결선 (I²C0, 주소 `0x59`)

| SGP40 핀 | 헤더 라벨 | GPIO |
|----------|-----------|------|
| VDD / VCC | 3V3 | — |
| SDA | 5 | GPIO5 |
| SCL | 6 | GPIO6 |
| GND | GND | — |

> 3.3 V 전용이다. AHT10/AHT20 과 같은 I²C0 버스(GPIO5/6)를 공유하며, 주소가
> 달라 충돌하지 않는다. 브레이크아웃 보드에 풀업 저항이 이미 있으면 보통 추가
> 저항은 필요 없다.

### SGP40 용도와 기능

- 실내 공기 중 VOC 변화를 감지하는 MOX 기반 가스 센서다.
- `MeasureRawCompensated()` 로 읽으면 AHT10/AHT20 온도와 습도 값을 함께 넣어
  온습도 보정을 적용할 수 있다.
- 이 프로젝트의 메인 코드에서는 SGP40 raw tick 을 시리얼과 TFT에 표시하고,
  필요하면 VOC Index 계산과 Pushgateway 전송에도 사용한다.
- 센서가 내보내는 값은 절대 농도(ppm)가 아니라 raw tick 이므로, 숫자 자체보다
  상대 변화와 추세를 보는 용도에 적합하다.

## Build / Flash / Monitor

전제: TinyGo ≥ 0.41, `espflasher` 설치 (`go install tinygo.org/x/espflasher@latest`).

```bash
make build                                       # build/firmware.bin 생성
make flash PORT=/dev/cu.usbmodemXXXX             # espflasher로 적재
make monitor PORT=/dev/cu.usbmodemXXXX           # 시리얼 모니터
```

기본 빌드 타겟은 `esp32s3-generic`. 다른 보드면 `make build TARGET=...` 으로 재정의.

기본 `main.go` 는 WS2812 팔레트 사이클과 1.69" 240x280 ST7789V3 콘솔
출력을 함께 동작시킨다. `println` 으로 찍는 로그가 USB 시리얼과 ST7789
화면 양쪽에 동시에 표시된다. 결선·API 는 [docs/st7789-console.md](docs/st7789-console.md)
참고.

새 페리페럴에 GPIO 를 할당하기 전에는 [docs/gpio-pin-mapping.md](docs/gpio-pin-mapping.md)
의 금지 핀 / 안전 핀 목록과 매핑 절차를 먼저 확인한다.
