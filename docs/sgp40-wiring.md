# SGP40 결선 가이드

Sensirion **SGP40** 실내 공기질(VOC) 센서를 본 보드(ESP32-S3-N8R2 / N16R8
44핀 USB-C DevKit)에 연결하는 방법과, TinyGo 드라이버
(`internal/sgp40`) 사용 절차를 정리한다.

> 데이터시트: <https://sensirion.com/media/documents/296373BB/6203C5DF/Sensirion_Gas_Sensors_Datasheet_SGP40.pdf>

## 1. 칩 / 모듈 개요

| 항목 | 값 |
|------|-----|
| 인터페이스 | I²C, 최대 400 kHz |
| I²C 주소 | `0x59` (고정) |
| 동작 전압 | 1.7 V – 3.6 V (**3V3** 사용 권장) |
| 전력 (가열 ON) | 평균 ~2.6 mA, 피크 ~48 mA |
| 측정 출력 | `measure_raw_signal`(0x260F) → 16-bit raw tick |
| 권장 샘플링 | 1 Hz (Sensirion VOC Index 알고리즘 가정) |

브레이크아웃 보드(예: Adafruit #4829, DFRobot Gravity SGP40)는 보통 풀업
저항이 내장돼 있으므로 별도 풀업이 필요 없다. 베어 칩을 직접 결선한다면
SDA/SCL 라인에 각 **4.7 kΩ pull-up** 을 3V3 로 추가한다.

## 2. 핀 매핑

I²C 는 `machine.I2C0` 을 사용한다. ESP32-S3 의 GPIO 매트릭스 덕분에 SDA/SCL
은 임의 GPIO 에 라우팅 가능하지만, 본 프로젝트에서는 다른 페리페럴(ST7789,
DHT22, WS2812)과 충돌이 없는 좌측 헤더 **GPIO5 / GPIO6** 에 고정한다.

| SGP40 핀 | 보드 헤더 | GPIO | 비고 |
|----------|-----------|------|------|
| `VDD` (VCC) | 3V3 | — | **3.3 V**. 5V 직결 시 칩 파손. |
| `GND`       | GND | — | ESP32 GND 와 반드시 공유. |
| `SDA`       | 5   | GPIO5 | I²C0 데이터 (필요 시 4.7 kΩ pull-up to 3V3) |
| `SCL`       | 6   | GPIO6 | I²C0 클럭 (필요 시 4.7 kΩ pull-up to 3V3) |

다른 페리페럴 매핑은 [docs/gpio-pin-mapping.md](gpio-pin-mapping.md) 참고.
SGP40 항목도 같은 표(§4) 에 동시에 등록돼 있다.

### 결선 ASCII

```
   ESP32-S3 DevKit                SGP40 breakout
   ┌──────────────┐               ┌────────────┐
   │ 3V3 ─────────┼───────────────┤ VDD / VCC  │
   │ GND ─────────┼───────────────┤ GND        │
   │ GPIO5 (SDA) ─┼──── SDA ──────┤ SDA        │
   │ GPIO6 (SCL) ─┼──── SCL ──────┤ SCL        │
   └──────────────┘               └────────────┘
```

### 다른 I²C 디바이스와 함께 쓰기

I²C 는 멀티-드롭 버스이므로 SGP40 외에 다른 센서(예: SHT4x, BME280)를
**같은 GPIO5/6** 쌍에 병렬로 붙여도 된다. 주의:

- 주소 충돌 없는지 확인 (SGP40 = `0x59`).
- 풀업 저항은 **버스 전체에 한 쌍만** 필요. 브레이크아웃 두 개가 각자 풀업을
  들고 있다면 한 쪽 풀업을 떼는 게 안전.
- 1 Hz 측정을 보장하려면 다른 디바이스의 누적 트랜잭션 시간을 100 ms 이내
  로 유지.

## 3. TinyGo 코드 사용법

루트 `main.go` 가 이미 SGP40 을 초기화하고 1초마다 raw tick 을 시리얼과
ST7789 콘솔에 동시에 찍는다. 핵심 부분만 발췌:

```go
import (
    "machine"
    "esp32-s3-ex1/internal/sgp40"
)

const (
    pinSDA    = machine.GPIO5
    pinSCL    = machine.GPIO6
    i2cFreqHz = 400_000
)

machine.I2C0.Configure(machine.I2CConfig{
    Frequency: i2cFreqHz,
    SDA:       pinSDA,
    SCL:       pinSCL,
})

dev := sgp40.New(machine.I2C0)
_ = dev.Configure()

raw, err := dev.MeasureRaw()           // 50%RH / 25°C 기본 보정
// 또는
raw, err = dev.MeasureRawCompensated(rh, t) // SHT4x 등에서 얻은 실측치 주입
```

### 드라이버 API 요약 (`internal/sgp40`)

| 함수 | 용도 | 주의 |
|------|------|------|
| `New(bus)` | 디바이스 핸들 생성 | I²C 설정은 caller 가 먼저 수행 |
| `Configure()` | 파워업 대기 + heater off | 자가진단은 하지 않음 |
| `MeasureRaw()` | 50%RH / 25°C 보정으로 raw tick 1회 측정 | 1 Hz 권장 |
| `MeasureRawCompensated(rh, t)` | RH(%) / T(°C) 실측치로 보정 측정 | RH 0~100, T −45~130 범위 |
| `HeaterOff()` | 가열 정지 (idle) | 측정 일시 중단 시 |
| `SelfTest()` | 320 ms 자가진단 | 실패 시 `ErrSelfTest` |
| `SerialNumber()` | 48-bit 시리얼 | 부트 로그용 |

### 실측 보정값을 함께 쓰고 싶다면

`MeasureRawCompensated` 에 SHT4x / BME280 등 외부 RH·T 측정값을 넘기면
습도·온도 변화에 따른 cross-sensitivity 가 크게 줄어든다. SHT4x 도 같은
I²C0 버스에 올려두고 매 사이클마다 RH·T 를 읽어 SGP40 호출에 넘기는 패턴이
Sensirion 권장 구성이다.

## 4. raw tick(`SRAW`) 값의 의미

`MeasureRaw` / `MeasureRawCompensated` 가 돌려주는 16-bit 정수는 SGP40 의
**가스 감지 저항을 ADC 로 디지털화한 원시 신호**(SRAW, signal raw)다. 시리얼/
화면에 `voc raw: 28664` 처럼 찍히는 그 숫자다.

### 물리적으로 뭐를 측정한 건가

SGP40 안에는 가열판 위에 얹힌 **금속산화물(MOX) 박막** 이 있다. 박막의
전기 저항은 주변 공기 중 **환원성 가스(VOC, 알코올, 폼알데하이드, 케톤,
유기 용매 등) 농도에 따라 변한다**:

- 깨끗한 공기 → 박막 저항이 **높다**
- VOC 농도 증가 → 박막 저항이 **낮아진다**

칩이 이 저항을 로그 스케일로 ADC 한 결과가 SRAW 다. 즉 SRAW 자체는
"VOC 농도(ppm/ppb)" 가 **아니라** 가스 저항을 부호화한 내부값이다.

### 값의 범위와 방향

| 항목 | 값 |
|------|-----|
| 비트 폭 | 16-bit unsigned |
| 이론 범위 | `0` … `65535` |
| 실제 공기 중 베이스라인 | 약 `25000` ~ `32000` (칩 / 환경마다 다름) |
| 깨끗한 공기 | **상대적으로 높은** SRAW |
| VOC 증가 | **상대적으로 낮은** SRAW |
| 같은 SRAW 라도 칩끼리 비교 의미 없음 | MOX 박막 개체차 + 베이스라인 drift |

> **방향이 직관과 반대**다. "공기가 나빠질수록 큰 값" 이 아니라 "공기가
> 나빠질수록 **작은 값**" 이다. 화면/로그를 사람이 읽을 때는 이 점을
> 잊지 말 것.

### 부팅 직후 / 안정화 특성

- **전원 인가 직후 ~수십 초**: 가열판 온도 + MOX 박막이 안정화되는 동안
  SRAW 가 한 방향으로 계속 흐른다 (보통 점점 커지는 모습). 이 구간 값은
  공기질 신호로 해석하면 안 된다.
- **수 분 ~ 수 시간**: 베이스라인이 천천히 자리잡는다. Sensirion 권장은
  **최초 가동 후 최소 수 분 워밍업** 후 사용.
- **장기 drift**: MOX 센서 특성상 며칠 단위로도 베이스라인이 이동한다. 한
  보드의 어제 값과 오늘 값을 절대 비교하지 말 것.

### 그래서 SRAW 를 어떻게 쓰나

세 가지 쓰임새가 있다:

1. **상대 변화 관찰** — 한 세션 안에서 "지금 SRAW 가 5분 전 대비 얼마나
   떨어졌나" 는 의미가 있다. 데모/실험용은 이걸로 충분.
2. **자체 베이스라인 + 임계치** — 부팅 후 N분간 평균낸 값을 기준선으로
   삼고, "기준선 대비 X % 이상 하락" 같은 단순 임계치를 호스트에서 직접
   계산. 빠르게 알림(LED, 부저)을 만들 때 유용.
3. **VOC Index (0–500) 변환** — 사람이 읽을 수 있는 표준 지표가 필요하면
   §5 의 Sensirion Gas Index Algorithm 을 추가 구현해 SRAW → VOC Index 로
   매핑한다.

### 빠른 직관 테스트

알코올 면봉 / 매니큐어 리무버 / 방향제 등을 센서에서 5–10 cm 거리로
가져가 보면 수 초 안에 SRAW 가 **수천 단위로 떨어지는** 것이 보인다. 자극원
을 치우면 다시 천천히 베이스라인으로 돌아온다. 이 동작이 보이면 결선 +
드라이버 + 측정 흐름이 모두 살아 있다는 가장 확실한 증거다.

## 5. VOC Index (0–500) 변환 — 별도 작업

`MeasureRaw` 가 돌려주는 16-bit tick 은 **VOC Index 가 아니다**. 사람이
읽는 0–500 지표로 환산하려면 Sensirion 의 **Gas Index Algorithm** (적응형
베이스라인 + 시그모이드 매핑) 을 호스트에서 돌려야 한다. 본 드라이버는 raw
tick 만 노출하므로, 필요하다면 별도 Go 포팅을 `internal/sgp40` 아래에 추가
한다. 데모용으로는 raw tick 만으로도 공기질 변화 추이(낮을수록 깨끗)를
관찰할 수 있다.

## 6. 검증 절차

1. **빌드**: `make build` 가 에러 없이 끝나면 드라이버까지 컴파일된 것.
2. **플래시 / 모니터**: `make flash` → `make monitor` 로 시리얼 연결.
3. **부트 로그**:
   - `sgp40 serial: <hex>` — I²C 통신 성공.
   - `voc raw: <0..65535>` 가 1초마다 출력 — 측정 동작.
4. **이상 상황별 점검**:
   - `i2c configure err` — `pinSDA/pinSCL` 이 다른 페리페럴과 겹치지 않는지
     [docs/gpio-pin-mapping.md](gpio-pin-mapping.md) §1, §4 확인.
   - `sgp40 init err` 또는 `sgp40 serial err` — 결선/풀업/전원 점검. 전압이
     **3V3** 인지, 모듈 보드의 점퍼 설정도 확인.
   - `voc err: invalid CRC` — 버스 노이즈. 배선 길이 단축, 풀업 4.7 kΩ
     추가, I²C 클럭을 `i2cFreqHz = 100_000` 으로 낮춰서 재시도.
   - raw 값이 계속 동일 — heater off 상태일 수 있음. `Configure()` 호출
     순서 확인 (`HeaterOff` 직후 첫 측정 시 가열이 재시작됨).
5. **반응 테스트**: 알코올 면봉 / 매니큐어 리무버를 센서 근처(5–10 cm)에
   가져가면 raw tick 이 수초 내로 떨어진다 (작을수록 VOC 농도 ↑).

## 7. 체크리스트 (새 보드에 적용 시)

- [ ] VCC 가 **3.3 V** 인가? (5V 금지)
- [ ] GND 가 ESP32 GND 와 직결돼 있는가?
- [ ] SDA/SCL 이 GPIO5/6 인가? 변경했다면 `main.go` 상수와 본 문서 §2 동기화.
- [ ] 풀업 저항(보드 내장 또는 외부 4.7 kΩ) 존재 확인.
- [ ] `make build` → `make flash` → `make monitor` 순서로 `voc raw:` 출력 확인.
