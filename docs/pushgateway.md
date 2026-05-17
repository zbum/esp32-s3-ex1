# Prometheus Pushgateway 연동 가이드

ESP32-S3 가 WiFi 로 접속해서 **SGP40 raw VOC tick + AHT10 온도/습도** 를
Prometheus [Pushgateway](https://github.com/prometheus/pushgateway) 로
주기적으로 POST 하는 설정. 코드는 모두 `main.go` + `internal/pushgateway/`
안에 들어 있고, WiFi 자격증명과 게이트웨이 주소는 **빌드 시 ldflags 로
주입** 한다 (소스 트리에 남기지 않는다).

## 1. 데이터 흐름

```
  SGP40 + AHT10 (I2C0)       ESP32-S3                     Pushgateway
 ──────────────────────  →  ────────────────────────  →  ─────────────
 SGP40 measure_raw_signal    raw TCP POST                /metrics/job/<job>/
 AHT10 measure (T/RH)        (espradio + netlink)         instance/<instance>
                             body (multi-line):           Prometheus scrape
                               aht10_temperature_celsius
                               aht10_humidity_percent
                               sgp40_voc_raw
```

AHT10 의 T/RH 값은 SGP40 의 `MeasureRawCompensated` 보정값으로도 같이
들어가서, 습도/온도 cross-sensitivity 가 줄어든 raw tick 이 push 된다.

호스트 측 Prometheus 는 Pushgateway 를 평소처럼 scrape 하면 된다 — 보드가
오프라인이어도 마지막 push 값이 게이트웨이에 남는다 (게이트웨이가 그렇게
설계됨).

## 2. 빌드 시 주입하는 변수

`main.go` 상단의 변수는 비어 있는 상태로 컴파일되므로, **SSID 가 비어 있으면
WiFi/푸시 단계가 통째로 스킵** 되고 보드는 기존 디스플레이 + WS2812 + SGP40
로컬 모드로만 동작한다.

| Make 변수 | `main.go` 심볼 | 기본값 | 용도 |
|-----------|----------------|--------|------|
| `SSID`          | `ssid`         | (없음) | WiFi SSID. 비우면 WiFi 비활성. |
| `PASSWORD`      | `password`     | (없음) | WiFi 패스프레이즈 |
| `PUSH_URL`      | `pushURL`      | (없음) | 게이트웨이 base. 예: `http://192.168.0.10:9091` |
| `PUSH_JOB`      | `pushJob`      | `esp32-s3-ex1` | Prometheus `job` 라벨 |
| `PUSH_INSTANCE` | `pushInstance` | `sgp40`         | Prometheus `instance` 라벨 |

최종 POST 대상:

```
<PUSH_URL>/metrics/job/<PUSH_JOB>/instance/<PUSH_INSTANCE>
```

Body 는 Prometheus text exposition 여러 줄 (한 push 에 묶음):

```
aht10_temperature_celsius 23.45
aht10_humidity_percent 47.12
sgp40_voc_raw 28664
```

센서 하나가 측정 실패하면 해당 라인은 그냥 빠진다. AHT10 단독, SGP40 단독
구성도 그대로 동작.

## 3. 플래시 예시

WiFi + Pushgateway 같이:

```bash
make flash \
  SSID=YourSSID PASSWORD=YourPassword \
  PUSH_URL=http://192.168.0.10:9091 \
  PUSH_JOB=esp32-s3-ex1 PUSH_INSTANCE=sgp40 \
  PORT=/dev/cu.usbmodem21101
```

WiFi 없이 (로컬 디스플레이/시리얼만):

```bash
make flash PORT=/dev/cu.usbmodem21101
```

부트 직후 시리얼 / TFT 콘솔에 다음 줄이 보이면 정상:

```
sgp40 serial: <hex>
aht10: ready
wifi: connecting to YourSSID
wifi mac: xx:xx:xx:xx:xx:xx
wifi: connected
push: http://192.168.0.10:9091/metrics/job/esp32-s3-ex1/instance/sgp40
...
temp: 23.5C  rh: 47.1%
voc raw: 28664
```

## 4. 게이트웨이 측 점검

호스트에서 게이트웨이가 받았는지 빠른 확인:

```bash
curl -s http://192.168.0.10:9091/metrics | grep -E 'sgp40_voc_raw|aht10_'
```

예상 출력:

```
# TYPE aht10_humidity_percent untyped
aht10_humidity_percent{instance="sgp40",job="esp32-s3-ex1"} 47.12
# TYPE aht10_temperature_celsius untyped
aht10_temperature_celsius{instance="sgp40",job="esp32-s3-ex1"} 23.45
# TYPE sgp40_voc_raw untyped
sgp40_voc_raw{instance="sgp40",job="esp32-s3-ex1"} 28664
```

`TYPE` 가 `untyped` 인 이유: 본 펌웨어는 body 에 `# TYPE ... gauge` 헤더를
보내지 않는다. Prometheus 가 scrape 할 때는 큰 문제가 없지만, 명시적으로
gauge 로 잡고 싶으면 `internal/pushgateway/pushgateway.go` 의 호출부에서 헤더
줄을 함께 보내도록 확장하면 된다.

## 5. 코드 구성

- **`internal/pushgateway/pushgateway.go`** — `Pusher{URL}` + `Push(body)` 뿐인
  얇은 래퍼. `net/http.Post` 위에 trailing newline 처리, 3xx 이상 status 에서
  에러를 돌려준다.
- **`main.go::setupWiFi`** — `espradio/netlink.Esplink` 를 `drivers/netdev`
  에 바인딩하고 `NetConnect` 호출. SSID 가 비어 있으면 즉시 리턴.
- **`main.go::setupPusher`** — `pushURL` + job + instance 를 합쳐 Pusher 를
  생성하고 패키지 변수에 저장.
- **`main.go::sampleAndMaybePush`** — 매 1초 cycle 의 측정 + 조건부 push.
  AHT10 → SGP40 순으로 읽고(보정값 전달), pushIntervalTicks 마다 세 메트릭
  라인을 한 body 로 묶어 POST. 한쪽 센서가 빠져도 나머지 라인은 그대로 push.
- **`main.go::main` 루프** — `cycleLED` + 1Hz `sampleAndMaybePush` 호출만
  한다. WiFi 가 꺼져 있거나 push 가 실패해도 시리얼/디스플레이는 계속 동작.

## 6. 인터벌 / 부하

- **SGP40 측정 주기 = 1 Hz** (콘솔 / TFT 라이브 표시).
- **Push 주기 = 60 s** (`pushIntervalTicks = 120`, 500 ms × 120).
- 분리한 이유: lneto 의 TCP 소켓 풀은 작고, `net/http.Post` 1회마다 새
  커넥션이 열렸다 닫히며 수십 초 TIME_WAIT 상태로 머문다. 1 Hz push 면
  풀이 금세 비워지지 않아 **`push err: resource exhausted`** 가 난다.
  실측해 보면 15 s 간격도 두 번째 push 부터 실패 — TIME_WAIT 가 그보다
  길다. 60 s 가 안정 동작 최소선이고, 그래도 가끔 실패하면 더 늘리는 게
  안전.
- Prometheus scrape 간격(보통 15 s) 보다 길어지지만, Pushgateway 는 마지막
  값만 보관해서 scrape 측에서 같은 값을 반복해 읽는 형태가 된다. 시계열은
  여전히 1분 해상도로 채워진다.
- 더 줄이려면 lneto 의 TCP 회수 동작이 좋아질 때까지 기다리거나, raw
  `net.Dial` + 수동 close 패턴(참고 프로젝트 `tinygo-air-measurer/httpclient.go`)
  으로 다시 작성해야 한다.

## 7. 트러블슈팅

| 증상 | 점검 |
|------|------|
| `wifi err: ...` | SSID/PASSWORD 오타, 2.4 GHz 대역인지 확인 (ESP32-S3 는 2.4 GHz only). |
| `push err: dial tcp: ...` | `PUSH_URL` 호스트가 보드와 같은 LAN 인지, 방화벽이 9091 열려 있는지. |
| `push err: status 400` | 보통 body 가 빈 줄로 끝나지 않은 경우. `pushgateway.Push` 가 자동 trailing newline 을 붙이지만 메트릭 이름에 공백이 들어가면 거부됨. |
| `push err: status 405` | URL 끝의 `job/.../instance/...` 경로 오타. 게이트웨이는 정확한 `/metrics/job/X` 형식을 요구. |
| Pushgateway 에는 값이 보이는데 Prometheus 에 안 나옴 | Prometheus `prometheus.yml` 의 `scrape_configs` 에 pushgateway job 이 등록돼 있고 `honor_labels: true` 인지 확인. |
| 빌드는 되는데 부트 후 멈춤 | WiFi 연결이 매우 느릴 때 발생 가능. `setupWiFi` 가 블로킹 호출 — SSID 가 잘못된 환경에 던지면 안 됨. |
| `SHA-256 comparison failed: ... Attempting to boot anyway...` | **무시.** espradio 가 펌웨어 블롭에 박아두는 정상 부트 메시지(`espradio` README 도 그대로 보여줌). 뒤에 `entry 0x...` → `Connecting to WiFi...` 가 이어지면 정상. |
| `push err: resource exhausted` 가 반복 | lneto TCP 소켓 풀 고갈. `pushIntervalTicks` 를 더 늘릴 것 (현재 30 → 15 s, 60 → 30 s). |

## 8. 보안 / 비밀 관리

- WiFi 패스프레이즈와 PUSH_URL 은 **소스 트리에 커밋되지 않는다** (Makefile
  변수만 노출). 빌드 시 `-X main.password=...` 로 들어가 펌웨어 바이너리
  안에는 평문으로 남는다. 보드를 외부에 배포한다면 이 점을 인지할 것.
- 게이트웨이가 인증을 요구하지 않는 사내 LAN 사용을 전제로 했다. 외부 망에
  노출된 게이트웨이라면 reverse proxy + basic auth 를 앞에 두는 것이 안전.
