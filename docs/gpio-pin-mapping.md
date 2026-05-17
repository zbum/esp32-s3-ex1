# ESP32-S3 GPIO 핀 매핑 가이드

이 보드(ESP32-S3-N8R2 / N16R8 44핀 USB-C DevKit)에서 외부 페리페럴(ST7789,
DHT22, I2C 센서 등)에 GPIO 를 할당할 때 따르는 규칙을 정리한 문서. 한
번 결정한 핀 매핑은 `README.md` 의 보드 핀맵과 함께 두고두고 참고한다.

## 1. 절대 피해야 할 핀

| GPIO       | 이유 |
|------------|------|
| **GPIO0**  | 부트 스트래핑(low → ROM 부트로더). 풀업으로 띄워두면 입력은 가능하나 외부에서 0V 로 끌어내리면 부팅 실패. |
| **GPIO3**  | 스트래핑 (JTAG 신호 선택). 외부 회로가 강하게 0/1 로 묶으면 부팅 영향. |
| **GPIO19** | ESP32-S3 네이티브 USB D-. 우측 USB-C 로 플래시/모니터하면 일반 GPIO 로 사용 불가. |
| **GPIO20** | 네이티브 USB D+. GPIO19 와 동일. |
| **GPIO45** | 스트래핑(VDD_SPI 전압 선택). |
| **GPIO46** | 스트래핑(부트 메시지 인쇄 모드). |
| **GPIO48** | 보드 내장 WS2812 RGB LED. 단순 digital out 불가, WS2812 프로토콜 필요. |
| **GPIO35 / 36 / 37** | **N16R8** 모듈에서 옥탈 PSRAM 버스에 점유. 절대 사용 금지. **N8R2** 는 쿼드 PSRAM 이라 사용 가능하지만, 모듈 표기를 반드시 확인. |

## 2. 안전한 GPIO 풀

두 모듈(N8R2/N16R8) 공통으로 **부팅 직후부터 자유롭게 디지털 IO 로
사용 가능한 핀** 은 다음과 같다.

| 헤더 | GPIO | ADC | Touch | 비고 |
|------|------|-----|-------|------|
| 좌측 | 4    | ✓   | ✓     | DHT22 권장 핀(현재 사용) |
| 좌측 | 5    | ✓   | ✓     |  |
| 좌측 | 6    | ✓   | ✓     |  |
| 좌측 | 7    | ✓   | ✓     | ST7789 BL(현재 사용) |
| 좌측 | 8    | ✓   |       | ST7789 RST(현재 사용) |
| 좌측 | 9    | ✓   | ✓     | ST7789 CS(현재 사용) |
| 좌측 | 10   | ✓   | ✓     | ST7789 DC(현재 사용) |
| 좌측 | 11   | ✓   | ✓     | ST7789 MOSI(현재 사용) |
| 좌측 | 12   | ✓   | ✓     | ST7789 SCK(현재 사용) |
| 좌측 | 13   | ✓   | ✓     |  |
| 좌측 | 14   | ✓   | ✓     |  |
| 좌측 | 15   | ✓   |       |  |
| 좌측 | 16   | ✓   |       |  |
| 좌측 | 17   | ✓   |       |  |
| 좌측 | 18   | ✓   |       |  |
| 우측 | 1    | ✓   | ✓     | UART1 TX 보조용 가능 |
| 우측 | 2    | ✓   | ✓     |  |
| 우측 | 21   |     |       |  |
| 우측 | 38   |     |       |  |
| 우측 | 39 / 40 / 41 / 42 |  |  | JTAG (사용 시 디버거 단자) — 디버그 안 쓰면 일반 IO |
| 우측 | 43 / 44 |  |  | UART0 TX/RX. 시리얼 콘솔용. 일반 IO 로 빼려면 모니터링 포기. |
| 우측 | 47 |     |       |  |

> 표는 이 보드의 핀 라벨/회로 기준이다. **다른 ESP32-S3 모듈**(XIAO 등) 은
> 핀 라벨이 다르므로 그 보드의 핀맵 사진/문서를 우선 참고한다.

## 3. TinyGo 의 GPIO 매트릭스 — 페리페럴 → 핀 자유 매핑

ESP32 시리즈는 GPIO 매트릭스가 있어서 **SPI / I2C / UART 신호를 거의 모든
GPIO 에 라우팅** 할 수 있다. TinyGo 의 `machine` 패키지도 이걸 그대로
노출한다.

### SPI

ESP32-S3 에서 사용자 SPI 는 `machine.SPI0` (= 하드웨어 FSPI / SPI2). 클럭과
데이터 핀은 `SPIConfig` 에 원하는 GPIO 를 넣으면 매트릭스가 자동 연결한다.

```go
bus := machine.SPI0
bus.Configure(machine.SPIConfig{
    Frequency: 40_000_000,
    SCK:       machine.GPIO12, // 어떤 GPIO 라도 가능
    SDO:       machine.GPIO11, // MOSI
    SDI:       machine.GPIO13, // MISO (필요 없으면 생략)
    Mode:      0,              // ST7789 는 mode 0
})
```

CS / DC / RST / BL 같은 보조 라인은 SPI 자체가 아니라 별도 GPIO 디지털
출력이므로, 그냥 안전 풀에서 골라 쓰면 된다.

### I2C

`machine.I2C0` 가 사용자 I2C. SDA / SCL 도 임의 GPIO 매핑 가능.

```go
machine.I2C0.Configure(machine.I2CConfig{
    SDA: machine.GPIO5,
    SCL: machine.GPIO6,
})
```

### UART

`machine.UART1`, `machine.UART2` 가 사용자 UART. TX/RX 도 자유 매핑.

## 4. 페리페럴별 권장 매핑 (현재 프로젝트 기준)

| 페리페럴 | 신호 | GPIO | 비고 |
|----------|------|------|------|
| WS2812 RGB | DIN | 48 | 내장, 변경 불가 |
| DHT22      | DATA | 4 | 4.7k~10k 풀업 권장 |
| ST7789V3   | SCK | 12 | SPI0 클럭 |
|            | MOSI | 11 | SPI0 SDO |
|            | DC | 10 | data/command |
|            | CS | 9 | chip select |
|            | RST | 8 | active low |
|            | BL | 7 | 백라이트 (PWM 가능) |

추가로 센서/버튼을 붙일 때는 이 표에 행을 추가해 주변 핀 충돌이 없게 관리한다.

## 5. 핀을 바꾸려면 — 코드 수정 위치

ST7789 데모를 다른 핀으로 옮기는 예:

```go
// 루트 main.go 상단 상수만 바꾼다
const (
    pinSCK  = machine.GPIO14   // 12 → 14
    pinMOSI = machine.GPIO13   // 11 → 13
    pinDC   = machine.GPIO17
    pinCS   = machine.GPIO18
    pinRST  = machine.GPIO16
    pinBL   = machine.GPIO15
)
```

그 후 다시 빌드 / 플래시:

```bash
make build
make flash PORT=/dev/cu.usbmodemXXXX
```

WS2812 / DHT22 도 동일한 패턴으로 `main.go` / `main.go.backup` 상단 상수를
교체한다.

## 6. 검증 절차

1. **빌드 통과** — `make console build` 가 에러 없이 끝나야 한다.
2. **부팅 정상** — 보드에 플래시 후 RST 누르고 시리얼 모니터(`make monitor`) 에
   `cpu freq` 로그가 보이면 스트래핑 핀 충돌은 없는 것.
3. **신호 확인** — 화면이 안 나오거나 깜빡거리면:
   - 핀 잘못 잡지 않았는지: 위 안전 풀 / 금지 핀 표 다시 확인.
   - GND 공유 여부: ESP32 GND ↔ 패널 GND 가 직접 연결돼 있어야 한다.
   - SPI 주파수 낮춰보기: `Frequency: 10_000_000` 로 임시 변경.
   - 백라이트 점등 확인: BL 핀을 강제로 3V3 로 묶어보면 백라이트만 들어와야 한다.
4. **부팅 실패** — 보드가 계속 ROM 부트로더로 떨어진다면 GPIO0/3/45/46
   주변에 풀다운이 걸려 있을 가능성. 외부 회로 분리하고 확인.

## 7. 체크리스트 (새 페리페럴 추가 시)

- [ ] 사용할 GPIO 가 §1 의 금지 핀 목록에 없는가?
- [ ] N16R8 모듈인데 GPIO35/36/37 을 안 쓰는가?
- [ ] §4 표에 행 추가해 다른 페리페럴과 핀 충돌이 없는가?
- [ ] 코드 상수 정의를 한 곳(파일 상단)에 모았는가?
- [ ] `make build` / `make console` 가 통과하는가?
- [ ] 시리얼 모니터에 부트 로그가 정상으로 출력되는가?
