# AHT10/AHT20 결선 가이드

ASAIR T/RH 센서 모듈을 본 보드(ESP32-S3-N8R2 / N16R8 44핀 USB-C DevKit)에
연결하는 방법. 본 프로젝트에서 사용 중인 모듈은 **외부 라벨이 "AHT10"**
이지만 실제 silicon 은 **AHT20** 명령 셋(init `0xBE`) 으로만 응답한다는
점을 먼저 짚어둔다. 이 문서는 그 호환성 이슈를 반영해 TinyGo 표준
`tinygo.org/x/drivers/aht20` 드라이버 사용을 전제로 정리한다.

> 데이터시트:
> - AHT10: <http://www.aosong.com/userfiles/files/media/Data%20Sheet%20AHT10.pdf>
> - AHT20: <http://www.aosong.com/userfiles/files/media/Data%20Sheet%20AHT20.pdf>

## 1. 칩 / 모듈 개요

| 항목 | 값 |
|------|-----|
| 인터페이스 | I²C, 최대 400 kHz |
| I²C 주소 | `0x38` (AHT10/AHT20 공통, 고정) |
| 동작 전압 | 1.8 V – 3.6 V (**3V3** 사용 권장) |
| 측정 출력 | 온도 ±0.3 °C, 습도 ±2 %RH |
| 권장 샘플링 | ≤ 1 Hz (자가 발열 방지) — 본 프로젝트는 push 주기와 동일한 60 s |

## 2. AHT10 vs AHT20 — 라벨 함정

데이터/측정 트리거 포맷은 거의 같지만 다음이 다르다:

| 항목 | AHT10 | AHT20 |
|------|-------|-------|
| 초기화 명령 | `0xE1` | `0xBE` |
| 상태 읽기 | 명령 없이 1바이트 read | `0x71` 송신 후 read |
| 측정 응답 길이 | 6 바이트 | 7 바이트(마지막 CRC) |

본 프로젝트에서 사용 중인 GY-AHT10 라벨 모듈은 보드 인쇄가 AHT10 이지만,
손에 잡힌 칩은 `0xE1` 에는 NACK, `0xBE` 에는 ACK 하는 **AHT20 silicon**.
TinyGo 표준 `aht20` 드라이버를 그대로 사용한다.

## 3. 핀 매핑

I²C 는 SGP40 과 **`machine.I2C0` 를 공유**한다. 두 센서 주소가 다르므로
(`0x38` vs `0x59`) 같은 버스에서 충돌 없이 동작한다. 단, 초기화 순서는
지켜야 한다 — §5 참고.

| AHT10 핀 | 보드 헤더 | GPIO | 비고 |
|----------|-----------|------|------|
| `VDD` (VCC) | 3V3 | — | **3.3 V**. 5V 직결 시 칩 파손. |
| `GND`       | GND | — | ESP32 GND 와 반드시 공유. |
| `SDA`       | 5   | GPIO5 | I²C0 데이터, SGP40 과 공유 |
| `SCL`       | 6   | GPIO6 | I²C0 클럭, SGP40 과 공유 |

전체 매핑 규칙은 [docs/gpio-pin-mapping.md](gpio-pin-mapping.md) §4 참고.

### 결선 ASCII

```
   ESP32-S3 DevKit              AHT10/AHT20 breakout
   ┌──────────────┐             ┌────────────┐
   │ 3V3 ─────────┼─────────────┤ VDD / VCC  │
   │ GND ─────────┼─────────────┤ GND        │
   │ GPIO5 (SDA) ─┼──── SDA ────┤ SDA        │
   │ GPIO6 (SCL) ─┼──── SCL ────┤ SCL        │
   └──────────────┘             └────────────┘
```

SGP40 도 같은 GPIO5/6 쌍에 병렬로 연결된다 (breadboard 위 같은 레일에
점퍼를 양쪽으로 꽂으면 됨). 풀업은 어느 한 쪽의 breakout 내장만으로
충분.

## 4. TinyGo 코드 사용법

루트 `main.go::setupAHT20` 이 한 번 호출되어 I²C0 를 구성하고 AHT20
드라이버를 초기화한다. 60초마다 `sampleAndMaybePush` 가 `rh.Read()` 로
측정값을 얻은 후 Pushgateway 로 전송한다.

```go
import (
    "machine"
    "tinygo.org/x/drivers/aht20"
)

machine.I2C0.Configure(machine.I2CConfig{
    Frequency: 400_000,
    SDA:       machine.GPIO5,
    SCL:       machine.GPIO6,
})

dev := aht20.New(machine.I2C0)
dev.Configure()                      // 반환값 없음
if err := dev.Read(); err != nil {   // 첫 측정으로 동작 확인
    println("aht20 read err:", err)
}
tempC := dev.Celsius()
humPct := dev.RelHumidity()
```

### 드라이버 API 요약 (`tinygo.org/x/drivers/aht20`)

| 함수 | 용도 | 주의 |
|------|------|------|
| `New(bus)` | 디바이스 핸들 생성 (값 타입) | I²C 설정은 caller 가 먼저 |
| `Configure()` | calibrated bit 미설정 시 init 송신 | 에러 반환 없음 — 동작 확인은 첫 `Read()` 로 |
| `Status()` | 1바이트 status 직접 읽기 | busy/calibrated 비트 확인용 |
| `Read()` | 한 번 측정 → 80 ms × 최대 3 retry | 결과는 `Celsius()` / `RelHumidity()` 로 |
| `Celsius()` / `RelHumidity()` | 직전 Read 의 변환값 | float32 |

## 5. SGP40 과의 공존 — 초기화 순서

같은 I²C0 버스에 SGP40 + AHT20 매달릴 때 **반드시 AHT20 init 이 SGP40
설정보다 먼저 끝나야** 한다. AHT20 의 init 핸드셰이크 동안 SDA 가 일시적
으로 LOW 로 잡히는데, 그 사이 SGP40 트랜잭션을 시작하면 START 조건이
깨지면서 SGP40 까지 `expected ACK not NACK` 으로 실패한다.

`main.go::main` 은 이미 이 순서를 강제한다:

```go
setupAHT20()   // I²C0 Configure + AHT20 init
setupSGP40()   // SGP40 attach (버스는 이미 idle)
```

## 6. Pushgateway 메트릭

`main.go::sampleAndMaybePush` 가 한 번의 POST body 에 다음 라인을 함께
실어 보낸다 (push 주기는 `pushIntervalTicks * 500 ms`, 현재 60 s):

```
aht20_temperature_celsius 23.45
aht20_humidity_percent 47.12
sgp40_voc_raw 28664
```

Prometheus 측에서는 평소대로 Pushgateway scrape 만 하면 세 메트릭이 같은
`job` / `instance` 라벨로 노출된다. 부분 구성(AHT20 만, 또는 SGP40 만)도
지원 — 측정 실패한 라인은 body 에서 빠진다. 전체 데이터 흐름과 빌드 인자는
[docs/pushgateway.md](pushgateway.md) 참고.

## 7. 검증 절차

1. **빌드**: `make build` 가 에러 없이 끝나면 드라이버까지 컴파일된 것.
2. **플래시 / 모니터**: `make flash` → `make monitor` 로 시리얼 연결.
3. **부트 로그**:
   - `aht20: ready  temp=23.5C rh=47.1%` — I²C0 init 성공 + 첫 Read 통과.
   - 60 초마다 `temp:` / `voc raw:` / `pushed @ i=...` 라인이 한 묶음씩
     출력.
4. **이상 상황별 점검**:
   - `aht20 read err: i2c: error: expected ACK not NACK` — 모듈이 진짜
     AHT10 silicon 일 수 있음 (init 명령 차이). 한 번에 확신이 안 가면
     `0xE1` 으로 init 시도하는 호환 드라이버를 다시 만들어 시도.
   - SGP40 쪽이 `voc err: ACK not NACK` 으로 떨어진다면 — §5 의 초기화
     순서가 깨졌을 가능성. `setupAHT20()` 가 `setupSGP40()` 보다 먼저
     호출되는지 확인.
   - 측정값이 항상 0 — Calibration 실패. `Status()` 반환의 bit3 확인.
5. **반응 테스트**: 손가락으로 센서 패키지를 잠시 잡고 있으면 습도가 수
   초 안에 +5 ~ +10 % 정도 올라간다. 떼면 천천히 내려온다.

## 8. 체크리스트 (새 보드에 적용 시)

- [ ] VCC 가 **3.3 V** 인가? (5V 금지)
- [ ] GND 가 ESP32 GND 와 직결돼 있는가?
- [ ] SDA/SCL 이 GPIO5/6 인가? 변경했다면 `main.go` 의 `pinSDA/pinSCL`
      상수와 본 문서 §3 동기화.
- [ ] SGP40 과 풀업 저항이 **버스 전체에 한 쌍만** 존재하는가? (양쪽
      breakout 에 내장이면 한 쪽 풀업 제거 권장)
- [ ] `make build` → `make flash` → `make monitor` 로 `aht20: ready` 로그
      확인.
