# AHT10 결선 가이드

ASAIR **AHT10** 온도/습도 센서를 본 보드(ESP32-S3-N8R2 / N16R8 44핀 USB-C
DevKit)에 연결하는 방법과, TinyGo 드라이버(`internal/aht10`) 사용 절차를
정리한다. SGP40 과 같은 I²C0 버스에 병렬로 붙이는 것을 전제로 한다.

> 데이터시트: <http://www.aosong.com/userfiles/files/media/Data%20Sheet%20AHT10.pdf>

## 1. 칩 / 모듈 개요

| 항목 | 값 |
|------|-----|
| 인터페이스 | I²C, 최대 400 kHz |
| I²C 주소 | `0x38` (고정) |
| 동작 전압 | 1.8 V – 3.6 V (**3V3** 사용 권장) |
| 측정 출력 | 온도 ±0.3 °C, 습도 ±2 %RH |
| 권장 샘플링 | ≤ 1 Hz (자가 발열 방지) |

## 2. AHT10 vs AHT20

데이터/측정 트리거 포맷은 거의 같지만 다음이 다르다:

| 항목 | AHT10 | AHT20 |
|------|-------|-------|
| 초기화 명령 | `0xE1` | `0xBE` |
| 상태 읽기 | 명령 없이 1바이트 read | `0x71` 송신 후 read |
| 측정 응답 길이 | 6 바이트 | 7 바이트(마지막 CRC) |

TinyGo `tinygo.org/x/drivers/aht20` 는 AHT20 전용 포맷을 가정하므로, 본
프로젝트는 `internal/aht10` 에 AHT10 전용 드라이버를 자체 작성해 둔다.

## 3. 핀 매핑

I²C 는 `machine.I2C0` 을 사용한다. 본 프로젝트에서는 SGP40 과 **버스를
공유**하며, GPIO5/6 에 고정한다.

| AHT10 핀 | 보드 헤더 | GPIO | 비고 |
|----------|-----------|------|------|
| `VDD` (VCC) | 3V3 | — | **3.3 V**. 5V 직결 시 칩 파손. |
| `GND`       | GND | — | ESP32 GND 와 반드시 공유. |
| `SDA`       | 5   | GPIO5 | I²C0 데이터 (필요 시 4.7 kΩ pull-up to 3V3) |
| `SCL`       | 6   | GPIO6 | I²C0 클럭 (필요 시 4.7 kΩ pull-up to 3V3) |

브레이크아웃 보드 대부분은 풀업 저항이 내장돼 있다. SGP40 과 AHT10 모두
풀업을 들고 있다면 한 쪽 풀업을 떼는 게 안전. 자세한 매핑 규칙은
[docs/gpio-pin-mapping.md](gpio-pin-mapping.md) §4 참고.

### 결선 ASCII

```
   ESP32-S3 DevKit                AHT10 breakout
   ┌──────────────┐               ┌────────────┐
   │ 3V3 ─────────┼───────────────┤ VDD / VCC  │
   │ GND ─────────┼───────────────┤ GND        │
   │ GPIO5 (SDA) ─┼──── SDA ──────┤ SDA        │
   │ GPIO6 (SCL) ─┼──── SCL ──────┤ SCL        │
   └──────────────┘               └────────────┘
```

### SGP40 와 같은 버스에 함께 쓰기

I²C 멀티-드롭 그대로 사용. 주소가 다르므로 충돌 없다:

| 디바이스 | I²C 주소 |
|----------|----------|
| AHT10    | `0x38`   |
| SGP40    | `0x59`   |

## 4. TinyGo 코드 사용법

루트 `main.go` 가 AHT10 을 1초마다 측정하고, 측정값을 SGP40
`MeasureRawCompensated` 의 보정값으로 그대로 넘긴 다음 두 메트릭을 한
번의 Pushgateway POST 에 함께 담아 보낸다.

```go
import (
    "machine"
    "esp32-s3-ex1/internal/aht10"
)

machine.I2C0.Configure(machine.I2CConfig{
    Frequency: 400_000,
    SDA:       machine.GPIO5,
    SCL:       machine.GPIO6,
})

dev := aht10.New(machine.I2C0)
if err := dev.Configure(); err != nil {
    println("aht10 init err:", err.Error())
}

tempC, humPct, err := dev.Measure()
```

### 드라이버 API 요약 (`internal/aht10`)

| 함수 | 용도 | 주의 |
|------|------|------|
| `New(bus)` | 디바이스 핸들 생성 | I²C 설정은 caller 가 먼저 수행 |
| `Configure()` | soft reset → init/calibrate | `ErrNotCalibrated` 시 결선/전원 점검 |
| `Status()` | 1바이트 status 직접 읽기 | busy/calibrated 비트 확인용 |
| `Measure()` | 온도(°C) + 습도(%) 1회 측정 | 80 ms 블로킹 |

`Measure` 의 반환 타입은 `(tempC, humidityPct float32, err error)`.
온도 유효 범위 −40 ~ 85 °C, 습도 0 ~ 100 %.

## 5. Pushgateway 메트릭

`main.go::sampleAndMaybePush` 가 한 번의 POST body 에 다음 라인을 함께
실어 보낸다:

```
aht10_temperature_celsius 23.45
aht10_humidity_percent 47.12
sgp40_voc_raw 28664
```

Prometheus 측에서는 평소대로 Pushgateway scrape 만 하면 세 메트릭이 같은
`job` / `instance` 라벨로 노출된다. 부분 구성(AHT10 만, 또는 SGP40 만)도
지원 — 측정 실패한 라인은 body 에서 빠진다. 전체 데이터 흐름과 빌드 인자는
[docs/pushgateway.md](pushgateway.md) 참고.

## 6. 검증 절차

1. **빌드**: `make build` 가 에러 없이 끝나면 드라이버까지 컴파일된 것.
2. **플래시 / 모니터**: `make flash` → `make monitor` 로 시리얼 연결.
3. **부트 로그**:
   - `aht10: ready` — I²C init/calibrate 성공.
   - `temp: 23.5C  rh: 47.1%` — 1초마다 출력.
4. **이상 상황별 점검**:
   - `aht10 init err: aht10: not calibrated` — 보드 풀업/전원 점검. SGP40
     과 AHT10 양쪽에 강한 풀업이 동시 존재하면 버스가 죽는다. 한쪽 풀업
     제거 후 재시도.
   - `aht10 err: I2C ...` — 결선 오타, GND 미공유, 또는 보드 결함.
   - 측정값이 항상 0 — Calibration 실패. `Status()` 반환의 bit3 확인.
   - 온도가 +20 °C 안팎 실온에서 0 °C 근처 — 데이터 비트 시프트 오류
     가능성. 드라이버 식 점검(`MSB` / `LSB` 순서).
5. **반응 테스트**: 손가락으로 센서 패키지를 잠시 잡고 있으면 습도가 수
   초 안에 +5 ~ +10 % 정도 올라간다. 떼면 천천히 내려온다.

## 7. 체크리스트 (새 보드에 적용 시)

- [ ] VCC 가 **3.3 V** 인가? (5V 금지)
- [ ] GND 가 ESP32 GND 와 직결돼 있는가?
- [ ] SDA/SCL 이 GPIO5/6 인가? 변경했다면 `main.go` 상수와 본 문서 §3 동기화.
- [ ] SGP40 과 풀업 저항이 **버스 전체에 한 쌍만** 존재하는가?
- [ ] `make build` → `make flash` → `make monitor` 순서로 `aht10: ready`,
      `temp: ...` 출력 확인.
