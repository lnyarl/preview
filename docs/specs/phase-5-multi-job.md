# Phase 5 — Agent 다중 Job 처리 (READY 재전송 + MaxJobs 슬롯 제어)

Status: **REVIEW_REVISED** (plan-reviewer 1차 REQUEST_CHANGES 9건 반영)
Author: planner
Date: 2026-04-25

---

## 1. Phase 개요

### 1-1. 배경

현재(Phase 0~4) Agent 는 Hub 에 WebSocket 으로 연결한 직후 `READY` 메시지를 **딱 1 회** 송신한다. 위치는 `internal/agent/client.go` 의 `once()` 함수 169~178 줄 — WELCOME 수신·conn 등록 직후 단 한 번 `protocol.NewEnvelope(TypeReady, ReadyData{Capacity: 1})` 를 써넣는 코드 블럭이다.

Hub 측 `HubDispatcher` 는 `READY` 를 받을 때마다 큐에서 대기 중인 job 1 개를 claim 후 `JOB_ASSIGN` 으로 송신하는 **pull-based** 모델이다 (Phase 2 결정 4). 따라서 현재 모델에서 1 회 연결 = **최대 1 개 job** 만 받을 수 있다. Agent 는 build → run 을 끝내면 더 이상 새 job 을 받지 않고, PR teardown 까지 idle 상태가 된다.

`Runner.Handle()` 의 의미적 라이프사이클을 보면 다음 사실이 드러난다.

- `Handle()` 은 **컨테이너가 `running` 상태가 되어 STATUS_UPDATE 가 송신된 직후 리턴**한다. (runner.go 234 줄)
- 컨테이너 자체는 `JOB_TEARDOWN` (= PR closed) 까지 살아 있다.
- 즉 `Handle()` 리턴 시점 = **빌드/배포는 끝났고 Agent 가 다음 build 를 받을 여유가 생긴 시점**이다.
- `Runner.inFlight` 는 이미 그 의미를 정확히 추적하고 있다 (Add/Defer Add(-1)).

### 1-2. 목표

Agent 가 **연결 1 회당 N 개의 build job** 을 직렬 또는 병렬로 처리할 수 있도록 한다. 동시 슬롯 한도는 `Config.MaxJobs` (기본 1, 이미 Phase 2 에서 정의됨) 이다. 동작 모델:

1. 연결 직후 — `MaxJobs` 만큼 `READY` 를 송신한다(슬롯 채움).
2. `Handle()` 이 리턴할 때마다 — `inFlight < MaxJobs` 면 `READY` 1 개를 추가 송신해 슬롯을 다시 채운다.
3. Pause 중에는 — `READY` 송신을 중단한다(graceful shutdown 동안 신규 job 거절은 Phase 3 결정 11 의 Runner 측 거절과 합쳐 안전).

이 변경으로 운영자는 Agent 1 개에 `--max-jobs 5` 를 주고 5 개의 PR 을 동시에 빌드·실행할 수 있다.

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- Hub 측 dispatcher 변경 — 이미 READY 를 N 번 받으면 N 개 assign 한다.
- 큐 우선순위 / fair scheduling / label 기반 라우팅 (Phase 2 와 동일).
- READY 메시지 페이로드 변경(Capacity 필드 의미 확장) — wire 호환성 보존.
- Agent 의 build 병렬성 자체에 대한 새 동기화(이미 `sync.Map` + `inFlight atomic` 으로 안전).
- 동적 MaxJobs 변경 (실행 중 reload) — 본 Phase 는 **시작 시 고정**.
- 시스템 자원 측정 기반 자동 조절 — out of scope.

### 1-4. 성공 기준 (요약)

- `Agent --max-jobs 3` 으로 시작 → Hub 큐에 5 개 job 이 있으면 첫 3 개가 즉시 동시 building. 1 개 끝날 때마다 다음 1 개 시작. 최종 5 개 모두 running.
- `Agent --max-jobs 1` (기본) → Phase 4 와 동일한 외부 동작(연속 처리, 동시 1 개).
- 단위 테스트: ReadySender mock 으로 송신 횟수와 시점이 검증 가능.
- 회귀 0: Phase 0~4 의 모든 단위 / 통합 / e2e 테스트 통과.

---

## 2. In / Out of Scope

### 2-1. In Scope

- `internal/agent/client.go` — 초기 READY 송신 로직을 "1 회" → "MaxJobs 회" 로 확장. 외부에서 호출 가능한 `SendReady(ctx)` 메서드 노출.
- 신규 인터페이스 `ReadySender` 정의 (in `internal/agent/runner.go`) — Runner 가 Client 를 직접 import 하지 않도록 의존 역전.
- `Runner` 에 `ReadySender` 의존 주입(`SetReadySender`) — Phase 4 의 `SetHolder` 와 동일 패턴.
- `Runner.Handle()` 리턴 직전(또는 defer 로) `inFlight < MaxJobs` 조건 검사 후 `ReadySender.SendReady` 호출.
- `Runner` 에 `MaxJobs int` 필드 + `SetMaxJobs(n int)` 메서드 추가. `cmd/agent/main.go` 가 wiring 시 주입.
- `Pause()` 후 — 신규 READY 송신 중단(이미 `Paused()` 메서드 있음).
- `agent.ParseConfig` 에 `MaxJobs` 64 hard cap 적용 (결정 11).
- 단위 테스트 (Runner + Client + 통합).

### 2-2. Out of Scope

- Hub 측 코드 변경.
- READY 페이로드 의미 변경(`Capacity` 필드는 그대로 진단용으로만).
- WebSocket reconnect 시점의 in-flight job 처리 — 재연결 시 `RunningPreviewIDs()` 로 HELLO 가 그대로 보고됨. 본 Phase 의 READY 재전송 로직만 reconnect 후에도 자연스럽게 동작.
- `MaxJobs` 동적 reload (Hub 가 `CONFIG_UPDATE` 로 푸시) — 향후 Phase.
- **READY rate-limit / throttle**: `maybeSendReady` 가 짧은 시간 내 N 회 conn.Write 를 직렬 호출하는 동작에 대한 시간 게이트. 본 Phase 는 `MaxJobs ≤ 64` (결정 11) 가정 하에 throttle 없이 운영. 더 큰 N 또는 Hub 측 read 병목 발견 시 도입 검토 — out of scope.
- **Pause window 의 noise 가시화**: Pause 직후 race 로 1 회 송신된 READY 가 만든 `paused 거절 → failed STATUS_UPDATE` 1 건을 metric/alert 로 분리 표시하는 작업. 본 Phase 는 STATUS_UPDATE failed 로그(기존)만으로 관측 충분 — out of scope.

### 2-3. Deferred (다음 Phase 후보)

- READY rate limit / debouncing.
- Agent → Hub 의 health-check / capacity 정기 보고.
- 시스템 자원 (CPU/메모리/디스크) 기반 동적 슬롯 조절.
- Job 별 priority / preemption.

---

## 3. 설계 결정 (Design Decisions)

### 결정 1 — 슬롯 모델은 "가용 capacity = MaxJobs - inFlight"

**선택**: Agent 는 `inFlight` 가 변할 때마다 "지금 비어 있는 슬롯 수" 를 계산해 그 차이만큼 READY 를 송신한다. 초기 연결 시 inFlight=0 이므로 MaxJobs 회. Handle 리턴 시 slot 1 개가 비므로 1 회.
**근거**: Hub 의 dispatcher 가 이미 "READY 1 회 = job 1 개 assign" 모델로 동작한다(Phase 2 §5-1). Agent 가 보내는 READY 의 갯수 = 비어있는 슬롯 수, 가 가장 단순한 1:1 매핑.
**대안 기각**: READY 페이로드에 `Capacity: N` 을 실어 Hub 가 N 개 assign — Hub 측 dispatcher 코드를 모두 바꿔야 함. 본 Phase 범위 폭증.

### 결정 2 — 의존 역전: Runner 는 `ReadySender` 인터페이스에만 의존

**선택**: `internal/agent/runner.go` 에 다음 인터페이스 신설.

```go
type ReadySender interface {
    SendReady(ctx context.Context) error
}
```

Runner 는 `*Client` 를 직접 import 하지 않는다. `cmd/agent/main.go` 가 wiring 시 `runner.SetReadySender(client)` 로 주입한다. Client 가 인터페이스 만족: `func (c *Client) SendReady(ctx context.Context) error`.
**근거**: 이미 Phase 2 에서 `HubSender` 인터페이스를 같은 식으로 도입했고(client.SendStatusUpdate), Phase 4 에서 `Holder` 도 같은 방식으로 주입한다. 일관성 + 순환 의존 회피 + 단위 테스트 시 fake mock 주입 가능.
**대안 기각**:
- Runner 가 `*Client` 직접 의존 → Client → Runner → Client 순환. import 불가능.
- Client 가 `inFlight` 를 직접 폴링 → Client 가 Runner 의 내부 상태를 들여다보는 게 됨. 책임 누설.
- 채널 기반 통신 (`runner.OnHandleDone <- struct{}{}` ) → 닫힘/timeout/select 케이스가 늘어 복잡도 증가. 인터페이스 호출 1 줄로 끝낼 수 있는데 비대.

### 결정 3 — READY 송신은 `Handle()` 의 `defer` 안에서

**선택**: `Handle()` 함수의 `r.inFlight.Add(1); defer r.inFlight.Add(-1)` 직후, **inFlight 감소가 완료된 뒤** READY 를 송신한다. 즉 별도 `defer` 블럭으로 두 단계.

```go
r.inFlight.Add(1)
defer func() {
    r.inFlight.Add(-1)
    r.maybeSendReady(ctx) // 항상 -1 이후 호출
}()
```

성공·실패·panic 모두에서 슬롯이 다시 열려야 하므로 defer 가 자연스러운 위치다. **순서 주의**: `inFlight.Add(-1)` 이 먼저 실행된 뒤 `maybeSendReady` 가 검사해야 한다.
**근거**: `Handle()` 안에 여러 fail path(repocache, run step, allocate port, container start) 가 있어 모든 분기에서 슬롯 회복을 보장하려면 defer 가 유일한 안전점. 또한 panic 까지 회복.
**대안 기각**:
- 성공 path 만에서 호출 → fail 시 슬롯 영구 점유.
- `Teardown()` 에서 호출 → Teardown 은 Hub 의 명시 메시지가 와야 트리거되므로 build 실패만으로는 슬롯이 회복되지 않음.

### 결정 4 — Pause 중에는 READY 송신 차단

**선택**: `r.Paused()` 가 true 면 `maybeSendReady` 는 송신하지 않고 즉시 리턴. 이미 진행 중인 build 가 끝나도 새 job 을 받지 않는다.
**근거**: Phase 3 결정 11 — graceful shutdown 동안 신규 JOB_ASSIGN 거절 정책을 운영자에게 일관 보장. Runner.Handle() 에서 paused 시 거절(failed STATUS_UPDATE) 하는 분기와 합쳐 "shutdown 중에 새 job 안 옴" 이라는 시각적 단순성을 만든다.
**대안 기각**: READY 는 보내고 Runner.Handle() 에서만 거절 → 운영자가 shutdown 진행 중인 Agent 에 job 이 계속 assign 되는 모습을 보고 혼란.

**Pause 와 inFlight/defer 상호작용 (중요)**:
- 현재 `Handle()` 의 paused 거절 분기는 `inFlight.Add(1)` 호출 **이전** 에 즉시 return 한다 (runner.go 122~132 줄). 따라서 그 경로에서는 defer 가 등록되지 않으며 `maybeSendReady` 도 호출되지 않는다 — 의도된 동작이다(Pause 후 신규 READY 차단).
- 본 Phase 의 변경은 paused 거절 분기를 **건드리지 않는다**. inFlight.Add(1) 은 paused 검사 통과 이후에만 일어나도록 위치를 그대로 유지한다.
- **금지 리팩터링**: paused 거절 분기 안에서도 `inFlight.Add(1); defer Add(-1)` 를 거치게 만들면 defer 의 maybeSendReady 가 매 거절마다 1 회 호출되어 — Hub 에 노이즈 READY 가 누적된다 (paused 검사가 그 안에서 또 차단해 주긴 하지만, 1 회 lock/atomic load 의 낭비). 따라서 paused 거절 → 즉시 return → defer 미등록 의 흐름을 유지한다.
- 진행 중 Handle 의 defer 가 paused 검사 race 로 1 회 통과해 송신될 가능성이 있다(리스크 표 참조). 그 부작용은 다음 항(F-9 / 리스크) 에서 다룬다.

### 결정 5 — 초기 READY 도 같은 경로 (`maybeSendReady` 루프) 로 송신

**선택**: 연결 직후 `MaxJobs` 회 READY 를 송신하는 코드를 별도로 두지 않고, `maybeSendReady` 를 루프로 호출. 즉 `for inFlight < MaxJobs && !paused` 동안 READY 1 회씩 송신.
**근거**: 단일 진입점 — "READY 송신 결정 로직" 이 한 곳에만 있게 한다. inFlight, MaxJobs, Paused 의 3 자 검사를 두 곳에 두면 분기 누락 위험.
**대안 기각**: 초기에는 `for i:=0; i<MaxJobs; i++ { send }` + 이후엔 단발 — 분기 두 개. 초기 상태에서 다른 goroutine 이 먼저 inFlight 를 증가시키는 race 시 잘못된 갯수 송신 가능.

### 결정 6 — READY 송신은 비동기/best-effort, 실패는 warn 로그

**선택**: `Client.SendReady` 가 conn write 실패하면 에러를 리턴하지만, `Runner.maybeSendReady` 는 그 에러를 warn 로그로만 남기고 build 흐름을 막지 않는다. WS 가 끊겼으면 어차피 재연결 시 `once()` 의 초기 READY 루프가 재시도한다.
**근거**: build 결과(STATUS_UPDATE) 송신은 실패 시 다음 build 가 막힐 수 있어 critical 이지만, READY 는 "다음 슬롯 알림" 으로 누락되어도 다음 disconnect/reconnect 가 자기 치유한다. 정상 흐름을 막을 이유 없음.
**대안 기각**: SendReady 실패 시 Handle 리턴값을 error 로 → 호출자(client.go 의 dispatchMessage 의 goroutine) 가 그 에러를 받아도 할 일이 없다. log 만 남는다.

### 결정 7 — `SendReady` 는 connMu 보호 하의 conn write

**선택**: `Client.SendReady` 는 기존 `SendStatusUpdate` 와 같은 패턴 — `connMu.Lock(); conn := c.conn; connMu.Unlock();` 으로 conn 핸들 캡처 후 conn 이 nil 이면 error 리턴, 아니면 5 초 timeout 으로 `conn.Write`.
**근거**: SendStatusUpdate 와 동일한 안전 패턴. WS 연결이 끊긴 사이의 호출에 nil deref 없음.
**대안 기각**: conn 직접 lock 잡은 채 write — write 가 ctx timeout 이상 걸리면 다른 송신자(StatusUpdate) 까지 멈춤. 캡처 후 unlock 이 정답.

### 결정 8 — 초기 READY 송신은 `once()` 안에서, conn 등록 직후

**선택**: 기존 169~178 줄(READY 1 회 송신)을 그대로 두되, `for i := 0; i < c.cfg.MaxJobs; i++ { sendReadyOnce(...) }` 루프로 바꾼다. **이 시점에는 Runner 가 inFlight=0 일 것을 가정**하고 단순 루프. 실제 검사는 `Runner.maybeSendReady` 가 갖고 있으므로, 초기에도 그것을 호출해도 되지만 — 연결이 막 살아난 뒤 Runner 의 inFlight 감시 시작점을 분명히 하기 위해 client.once() 안에서 명시적으로 MaxJobs 만큼 송신.
**근거**: Reconnect 시나리오에서 inFlight > 0 일 수 있다(이전 연결에서 시작된 build 가 아직 진행 중). 그럴 땐 `MaxJobs - inFlight` 만큼만 송신해야 하므로 `Runner.maybeSendReady(...)` 패턴이 자연스럽다.
**구체화**: 초기 송신도 `runner.maybeSendReady(ctx)` 1 회 호출로 통일. `maybeSendReady` 내부가 `for inFlight < MaxJobs && !paused { sendOne }` 루프이므로 결정 5 와 일관.
**대안 기각**: 초기엔 무조건 MaxJobs 회 송신 → reconnect 시 over-subscribe.

### 결정 9 — Capacity 필드 의미 보존

**선택**: `ReadyData.Capacity` 는 그대로 `1` 로 송신. 페이로드 의미: "이 1 개의 READY 메시지가 1 개 슬롯에 해당". MaxJobs 갯수는 메시지 갯수로 표현.
**근거**: Hub 측 dispatcher 가 `Capacity` 를 무시하고 메시지 1 회당 1 개 assign 하는 모델. 페이로드 의미를 바꾸려면 Hub 도 바뀌어야 함 — out of scope.
**대안 기각**: `Capacity: MaxJobs` 송신 → Hub 가 무시하므로 의미 없는 noise. 호환성 측면에서도 1 이 안전.

### 결정 10 — `MaxJobs` 는 시작 시 고정, Holder 와는 별개

**선택**: `MaxJobs` 는 `Config.MaxJobs` (CLI 플래그/env) 로만 결정. Phase 4 의 `Holder` (RunCommands/ContainerPort) 와는 완전히 별개로 둔다. Hub 측 UI 에 노출하지 않는다(이번 Phase).
**근거**: MaxJobs 변경은 메모리/CPU 관점에서 운영자 머신의 capacity 와 직결된다. Hub 가 원격으로 결정하면 운영자가 모르는 사이 머신 자원이 폭주할 수 있다. 운영자 측 결정 권한을 보존.
**대안 기각**: Holder 에 추가 → Hub UI 노출이 따라온다. 본 Phase 범위 폭증.

### 결정 11 — `MaxJobs` hard cap 64

**선택**: `Config.MaxJobs` 는 `1..64` 범위로 클램프. `agent.ParseConfig` 의 기존 `if *maxJobs < 1 { *maxJobs = 1 }` 직후에 `if *maxJobs > 64 { *maxJobs = 64 }` 를 추가. 64 초과 입력 시 warn 로그 1회 + 64 로 강제.
**근거**:
- 운영자 실수로 `--max-jobs 10000` 입력 시 `maybeSendReady` 가 10000 회 conn.Write 시도 → conn buffer 폭주 + Hub read loop 마비 가능. 실수에 대한 방어선 필요.
- 64 의 근거: 일반 머신 코어 수의 4~8 배 상한으로 충분. 정상 사용 사례에 영향 없음. 더 큰 값을 원하면 코드 상수 변경(설계 의도 거치도록).
- 422 reject 가 아닌 클램프인 이유: CLI 는 GUI 와 달리 즉시 피드백이 없다. 거절 시 운영자가 다시 명령어 작성해야 함 — 안전한 값으로 보정 + 로그가 더 친절.

**대안 기각**:
- 상한 없음 → 위 폭주 시나리오 미방어.
- 422 거절(exit 2) → 자동 부팅 스크립트가 깨지면 운영 사고 위험.
- 상수 16 / 32 → 32 코어 머신에서 정상 사용을 막을 수 있음. 64 가 안전한 상한.

---

## 4. 명세 상세

### 4-1. 인터페이스 계약

#### 4-1-1. `internal/agent/runner.go` 신규 인터페이스

```go
// ReadySender 는 Runner 가 Hub 로 READY 를 송신할 때 사용하는 의존.
// 구현은 client.go 의 Client 가 conn.Write 를 수행한다.
// nil 주입 허용 — nil 이면 maybeSendReady 가 no-op.
type ReadySender interface {
    SendReady(ctx context.Context) error
}
```

#### 4-1-2. `Runner` 구조체 확장

```go
type Runner struct {
    // ... 기존 필드 (docker, cache, hub, advHost, logger, holder, jobs, paused, inFlight) ...
    ready    ReadySender // Phase 5: nil 허용 (= no-op).
    maxJobs  int         // Phase 5: 기본 1.
}

// SetReadySender 는 READY 송신 의존을 주입한다 (Phase 5).
func (r *Runner) SetReadySender(rs ReadySender) { r.ready = rs }

// SetMaxJobs 는 동시 슬롯 한도를 설정한다. n < 1 이면 1 로 보정.
func (r *Runner) SetMaxJobs(n int) {
    if n < 1 { n = 1 }
    r.maxJobs = n
}
```

`NewRunner` 시그니처는 변경하지 않고, `cmd/agent/main.go` 가 `SetReadySender` 와 `SetMaxJobs` 를 별도 호출한다. (Phase 4 의 SetHolder 와 동일 패턴.)

**default 정책**: `NewRunner` 안에서 `r.maxJobs = 1` 을 명시 set 한다. `SetMaxJobs` 가 호출되지 않은 채 `maybeSendReady` 가 동작해도 1 슬롯 동작이 보장되도록 단일 진입점에서 default 를 잡는다. `maybeSendReady` 안의 `if r.maxJobs < 1 { r.maxJobs = 1 }` 보정은 그 단일 진입점이 우회되는 비정상 경로(예: 테스트에서 직접 `&Runner{}` 생성)에 대비한 **안전망**으로 그대로 둔다. 정상 wiring 흐름에서는 NewRunner default 가 작동한다.

#### 4-1-3. `Runner.maybeSendReady` (신규 비공개 메서드)

```go
// maybeSendReady 는 가용 슬롯 수 만큼 READY 를 Hub 에 송신한다.
// inFlight < maxJobs 이고 paused 가 아닌 동안 1 개씩 보낸다.
// 송신 실패는 warn 로그만 남기고 다음 슬롯으로 넘어가지 않는다(즉 1 회 실패 시 루프 종료).
// ready == nil 이면 no-op.
func (r *Runner) maybeSendReady(ctx context.Context) {
    if r.ready == nil { return }
    if r.maxJobs < 1 { r.maxJobs = 1 } // 안전 보정
    for {
        if r.paused.Load() { return }
        cur := r.inFlight.Load()
        if cur >= int64(r.maxJobs) { return }
        if err := r.ready.SendReady(ctx); err != nil {
            r.logger.Warn("agent_ready_send_failed", "err", err.Error())
            return
        }
    }
}
```

> 주의: 이 루프에는 race 가 있다 — `inFlight` 검사와 `SendReady` 사이에 다른 goroutine 이 새 job 을 받아 inFlight 를 늘렸을 수 있다. 그 경우 over-subscribe(MaxJobs+1 개) 가 일시적으로 발생할 수 있다. 본 Phase 는 결정 1 의 단순 모델을 우선시하고, **best-effort** 로 다룬다. Hub 측 dispatcher 도 큐가 비어 있으면 READY 를 무시하므로 worst case 영향은 "큐에 일이 있을 때 1~2 개 over-build" 정도로 한정. 운영상 실 영향 없음.

**1 회 SendReady 실패 시 회복 경로**:
WS 가 살아 있는 상태에서 `conn.Write` 가 일시 실패(예: 일시적 backpressure, conn buffer full 시 timeout 등)하면 위 루프는 `return` 으로 즉시 종료한다(연속 실패 누적 차단). 이때 비어 있는 슬롯이 그대로 남는다. 회복은 다음 두 경로 중 하나로 자기 치유된다.

1. **다음 Handle 종료** — 진행 중 다른 build 가 끝나면 그 defer 가 `maybeSendReady` 를 다시 호출. 이 호출은 현재 가용 슬롯 수(MaxJobs - inFlight) 만큼 다시 송신 시도한다. 즉 직전에 못 보낸 슬롯도 함께 채운다.
2. **WS 재연결** — `conn.Write` 실패가 일시적이지 않고 연결 자체가 죽어가는 신호라면 read loop 가 곧 에러를 감지해 `once()` 가 종료, 백오프 후 재연결. 재연결 후 `client.once()` 가 `runner.maybeSendReady(ctx)` 를 1 회 호출(§4-1-6)하므로 가용 슬롯 전체가 다시 채워진다.

따라서 일시적 send 실패는 다음 build 완료 또는 재연결 어느 쪽이 먼저 발생하든 자기 치유되며, **별도 retry 로직을 두지 않는다** — 의도된 한계가 아니라 의도된 단순화다.

#### 4-1-4. `Handle()` 변경점

```go
func (r *Runner) Handle(ctx context.Context, msg protocol.JobAssignData) error {
    pid := msg.PreviewID
    if r.paused.Load() { /* 기존 거절 분기 그대로 */ return nil }

    r.inFlight.Add(1)
    // Phase 5: defer 한 묶음으로 슬롯 회복 + READY 재전송.
    defer func() {
        r.inFlight.Add(-1)
        r.maybeSendReady(ctx)
    }()

    // ... 이하 기존 로직 동일 ...
}
```

기존 `defer r.inFlight.Add(-1)` 한 줄을 위 블럭으로 교체.

#### 4-1-5. `Client.SendReady` (신규 공개 메서드)

```go
// SendReady 는 현재 conn 으로 READY envelope 1 개를 송신한다.
// Runner.ReadySender 인터페이스 만족.
func (c *Client) SendReady(ctx context.Context) error {
    c.connMu.Lock()
    conn := c.conn
    c.connMu.Unlock()
    if conn == nil {
        return errors.New("client.SendReady: not connected")
    }
    env, err := protocol.NewEnvelope(protocol.TypeReady, protocol.ReadyData{Capacity: 1})
    if err != nil { return err }
    b, _ := json.Marshal(env)
    wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    return conn.Write(wctx, websocket.MessageText, b)
}
```

`SendStatusUpdate` 와 같은 패턴이며, 같은 connMu 를 공유하므로 동시 write race 없음.

#### 4-1-6. `Client.once()` 의 초기 READY 송신부 변경

기존 (169~178 줄):

```go
// READY 1회 송신 (결정 10: capacity=1 MVP).
readyEnv, _ := protocol.NewEnvelope(protocol.TypeReady, protocol.ReadyData{Capacity: 1})
rb, _ := json.Marshal(readyEnv)
rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
if werr := conn.Write(rctx, websocket.MessageText, rb); werr != nil {
    rcancel()
    c.logger.Warn("ws_ready_write_failed", "err", werr.Error())
} else {
    rcancel()
}
```

변경:

```go
// Phase 5: 가용 슬롯 수만큼 READY 송신 (재연결 시 inFlight>0 가능 → maybeSendReady 가 정확).
if c.runner != nil {
    c.runner.maybeSendReady(ctx)
} else {
    // 호환: runner 미주입 시 Phase 1~4 와 동일하게 1 회 송신.
    if err := c.SendReady(ctx); err != nil {
        c.logger.Warn("ws_ready_write_failed", "err", err.Error())
    }
}
```

> `Client` 와 `Runner` 둘 다 `internal/agent` 동일 패키지이므로 비공개 메서드 `maybeSendReady` 를 그대로 호출 가능하다 — 별도 export 없이 비공개로 둔다.

### 4-2. wiring 변경 (`cmd/agent/main.go`)

기존 82~90 줄:

```go
c := agent.NewClient(cfg, logger)
runner := agent.NewRunner(docker, cache, c, cfg.AdvertiseHost, logger)
c.SetRunner(runner)

// Phase 4: in-memory 빌드 설정 보관소.
holder := agent.NewHolder()
c.SetHolder(holder)
runner.SetHolder(holder)
```

추가 (Phase 4 holder 주입 직후):

```go
// Phase 5: 다중 Job 슬롯 + READY 재전송 의존.
runner.SetReadySender(c)
runner.SetMaxJobs(cfg.MaxJobs)
```

`Config.MaxJobs` 는 이미 Phase 2 에서 `--max-jobs` 플래그/`AGENT_MAX_JOBS` env 로 파싱된다(config.go 48 줄, 67~69 줄 보정).

### 4-3. 시퀀스 다이어그램 (ASCII)

#### 4-3-1. 정상 다중 처리 (`MaxJobs=3`, 큐에 5 개 job)

```
Agent                         Hub
  | --- HELLO ----------------->|
  | <--- WELCOME --------------|
  | --- READY (1) ------------->|  // maybeSendReady: inFlight=0/3
  | --- READY (2) ------------->|
  | --- READY (3) ------------->|
  | <--- JOB_ASSIGN(p1) -------|
  | <--- JOB_ASSIGN(p2) -------|
  | <--- JOB_ASSIGN(p3) -------|
  | (Handle goroutine x3 동시 실행, inFlight=3)
  | --- STATUS_UPDATE(p1, building) ---> |
  | --- STATUS_UPDATE(p2, building) ---> |
  | --- STATUS_UPDATE(p3, building) ---> |
  | (p1 빌드 완료, run, running 송신, Handle 리턴)
  | --- STATUS_UPDATE(p1, running) ---> |
  | (defer: inFlight 3→2, maybeSendReady 호출)
  | --- READY (4) ------------->|  // 슬롯 1 개 회복
  | <--- JOB_ASSIGN(p4) -------|
  | (inFlight 2→3 again)
  | (p2 완료...)
  | --- READY (5) ------------->|
  | <--- JOB_ASSIGN(p5) -------|
  | ... 큐 비고 모든 job running 상태로 진입 ...
```

#### 4-3-2. Pause(graceful shutdown) 시

```
Agent                         Hub
  | (build p1, p2 진행 중, inFlight=2/3)
  | (SIGTERM 도착)
  | runner.Pause()  → paused=true
  | (p1 끝)
  | --- STATUS_UPDATE(p1, running) ---> |
  | (defer: maybeSendReady → paused 검사 → return; READY 미송신)
  | (p2 끝)
  | (defer: 동일하게 READY 미송신)
  | (drain 완료, ws close)
```

#### 4-3-3. Reconnect 시 (inFlight > 0)

```
Agent                         Hub
  | (이전 연결: build p1 진행 중, inFlight=1)
  | (WS 끊김 — read loop 에러)
  | (재연결 backoff 후)
  | --- HELLO (RunningPreviews=[p1]) -->|
  | <--- WELCOME --------------|
  | (이 시점 inFlight=1, MaxJobs=3 가정)
  | maybeSendReady 호출
  | --- READY (1) ------------->|  // 1 개만 송신 (3-1=2? 2회)
  | --- READY (2) ------------->|  // 정확히 maxJobs - inFlight = 2 회
```

> 위 그림에서 정확한 갯수는 2 — 결정 1 에 따라 inFlight=1, MaxJobs=3 → 가용 슬롯 2 개 → READY 2 회.

### 4-4. 디렉토리/파일 변경 (참고)

```
internal/agent/
  runner.go          [수정] ReadySender 인터페이스 + maxJobs 필드 + maybeSendReady + Handle defer 변경
  client.go          [수정] SendReady 메서드 + once() 의 초기 READY 루프
  runner_test.go     [수정] fakeReadySender 추가, MaxJobs 시나리오 단위 테스트
  client_test.go     [수정] SendReady 단위 테스트, 초기 READY MaxJobs 회 송신 검증
cmd/agent/main.go    [수정] runner.SetReadySender(c) + runner.SetMaxJobs(cfg.MaxJobs)
docs/specs/phase-5-multi-job.md [본 문서]
```

---

## 5. 기능 체크리스트 (F-*)

### 5-1. ReadySender 의존 / Runner 통합

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | `ReadySender` 인터페이스가 `internal/agent/runner.go` 에 정의되고 `SendReady(ctx) error` 시그니처 |  소스 grep + go build |
| F-2 | `Runner.SetReadySender(rs)` 호출 후 maybeSendReady 가 그 의존을 사용한다 | 단위(fake mock 송신 횟수) |
| F-3 | `Runner.SetReadySender(nil)` 또는 미주입 시 maybeSendReady 가 panic 없이 no-op | 단위 |
| F-4 | `Runner.SetMaxJobs(n)` 가 n<1 입력 시 1 로 보정 | 단위 |
| F-5 | `Runner.SetMaxJobs(5)` 후 maybeSendReady 가 inFlight=0 시 5 회 송신 | 단위(fake mock) |

### 5-2. Handle 시점 송신

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-6 | 단일 Handle 성공 path 후 maybeSendReady 가 1 회 호출되고 fake ReadySender 가 1 회 송신 | 단위 |
| F-7 | Handle 실패 path (build error / port error / docker error) 어디서 실패해도 defer 가 실행되어 maybeSendReady 가 1 회 호출 | 단위 (분기마다) |
| F-8 | Handle 진입 시 paused=true 면 inFlight 증가 없이 즉시 리턴, READY 도 송신하지 않음 | 단위 |
| F-9 | Handle 진행 중 Pause() 호출 시 — 진행 중 build 는 끝까지 진행, defer 의 maybeSendReady 가 paused 검사로 송신 차단 (결정적 케이스) | 단위: fakeReadySender 와 fakeDocker 로 Handle 1 개 in-flight → `runner.Pause()` → Handle 정상 종료 시퀀스 → fakeReadySender.SendReady 호출 카운트 == 0 검증. (Pause 호출이 maybeSendReady 진입보다 먼저인 결정적 시점만 검사) |
| F-9b | F-9 의 race 케이스 (Pause 직후 maybeSendReady 가 paused 검사 race 로 1 회 통과) | **비검증**: Hub 가 그 1 개 READY 로 JOB_ASSIGN 을 보내도 다음 Handle 진입의 paused 거절 분기가 즉시 failed STATUS_UPDATE 송신으로 흡수 → 데이터 무결성 영향 없음. 따라서 단위 테스트에서 검증하지 않고, 리스크 표(§8) 와 결정 4 에 한정해 명시. |

### 5-3. inFlight 슬롯 동기화

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-10 | inFlight 가 maxJobs 미만일 때 maybeSendReady 가 차이만큼 정확히 송신 (inFlight=1, max=3 → 2 회) | 단위 |
| F-11 | inFlight 가 maxJobs 와 같으면 maybeSendReady 가 0 회 송신 | 단위 |
| F-12 | inFlight 가 maxJobs 를 초과하는 (race) 상황에서도 0 회 송신 (`>=` 비교) | 단위 |
| F-13 | Concurrent 다수 Handle 종료가 동시에 maybeSendReady 호출해도 -race 통과 | `go test -race` |

### 5-4. Client 송신 경로

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-14 | `Client.SendReady(ctx)` 가 conn=nil 이면 error 리턴, panic 없음 | 단위 |
| F-15 | `Client.SendReady(ctx)` 가 정상 conn 에 READY envelope 1 개를 write 한다 | 단위: 기존 `internal/agent/client_test.go` 의 `fakeHub` (httptest.NewServer + websocket.Accept, 25~68 줄) 패턴을 차용. fakeHub 가 WELCOME 송신 후 read loop 에서 받은 첫 메시지가 `TypeReady` envelope 이고 `ReadyData.Capacity == 1` 임을 검증 |
| F-16 | `Client.SendReady` 가 `SendStatusUpdate` 와 connMu 를 공유해 동시 write race 없음 | `go test -race` |
| F-17 | `Client.once()` 초기 READY 송신부가 runner != nil 이면 maybeSendReady 1 회 호출, 아니면 SendReady 1 회 호출 | 단위 (fake runner) |

### 5-5. 통합 / 회귀

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-18 | MaxJobs=1 (기본) 시 외부 동작이 Phase 0~4 와 동일 (회귀 0) | Phase 4 단위/통합 테스트 재실행 |
| F-19 | MaxJobs=3, fake Hub 가 5 개 JOB_ASSIGN 을 큐잉, 모두 building → running 으로 진행 | 통합 (fake hub stub) |
| F-20 | wiring: cmd/agent/main.go 가 `runner.SetReadySender(c)` 와 `runner.SetMaxJobs(cfg.MaxJobs)` 를 호출 | grep + go build |
| F-21 | `--max-jobs 5` CLI 플래그가 Runner.maxJobs 까지 전달된다 | 단위: `agent.ParseConfig([]string{"--hub-url", "...", "--token", "...", "--repo-url", "...", "--max-jobs", "5"})` 호출 → `cfg.MaxJobs == 5` 검증 + 별도 단위로 `r := NewRunner(...); r.SetMaxJobs(cfg.MaxJobs); fakeReady := &fakeReadySender{}; r.SetReadySender(fakeReady); r.maybeSendReady(ctx)` → fakeReady.calls == 5 검증. (실제 fake Hub 통합은 F-19 가 담당) |
| F-22 | reconnect 후 inFlight 가 0 이 아니면 가용 슬롯만큼만 READY 송신 (over-subscribe 없음) | 단위 (reconnect 시뮬) |
| F-23 | `agent.ParseConfig` 가 `--max-jobs 10000` 입력 시 `cfg.MaxJobs == 64` 로 클램프 (결정 11) | 단위: ParseConfig 호출 후 MaxJobs 값 검증 + warn 로그 출력 검증 |

---

## 6. 비기능 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-1 | 외부 의존성(go.mod) 추가 0 개 | `go mod tidy` diff |
| NF-2 | 신규 코드는 책임 주석(3~5 줄) 포함 | grep `// 이 파일의 책임:` (수정 부분은 헤더 주석 갱신) |
| NF-3 | 어떤 파일도 300 줄을 넘지 않는다 | `wc -l internal/agent/*.go`. **현재 `runner.go` = 300 줄(한도 정확)**. 본 Phase 추가량 추정: `ReadySender` 인터페이스(5줄) + `maxJobs` 필드(1줄) + `SetReadySender` / `SetMaxJobs` (각 4줄) + `maybeSendReady` (15줄) + 책임 주석 헤더 갱신(2줄) ≈ +30 줄 → 330 줄 예상으로 한도 초과. **분할 정책**: `ReadySender` 인터페이스 + `maxJobs` 필드 + `SetReadySender` / `SetMaxJobs` / `maybeSendReady` 를 신규 파일 `internal/agent/ready.go` 로 분리한다. 책임은 "Runner 의 슬롯 capacity / READY 송신 트리거" 하나로 단일. `runner.go` 측에는 `Handle()` 의 defer 변경(2줄)만 남겨 +2 ~ +3 줄 증가에 그치도록 한다. |
| NF-4 | `go vet ./...`, `golangci-lint run` clean | CI |
| NF-5 | `go test -race ./...` green (특히 Runner concurrent Handle 시나리오) | CI |
| NF-6 | `internal/agent/runner.go` 가 `internal/hub` 또는 외부 패키지를 import 하지 않는다 (단방향 유지) | depguard / grep |
| NF-7 | `slog` 키 명명 컨벤션 (`agent_ready_*`, `agent_job_*`) 일관 | grep |
| NF-8 | 새 인터페이스 `ReadySender` 의 mock 이 단위 테스트에서 사용 가능 (fake 구현 50줄 이내) | 테스트 코드 리뷰 |
| NF-9 | Phase 0~4 의 모든 단위 + 통합 + e2e 테스트 통과 (회귀 0) | full test 재실행 |
| NF-10 | `Capacity` wire 페이로드는 `1` 그대로 (호환성 보존) | grep `Capacity:` 가 1 외 값 없는지 |
| NF-11 | client.go / runner.go 의 헤더 책임 주석에 Phase 5 변경 반영 | 시각 리뷰 |

---

## 7. 단계 분할 (구현·평가용)

본 Phase 는 구현 분량이 작고 한 번에 검증 가능하므로 **단일 Step** 으로 진행한다.

### 단일 Step 범위

- `Runner.ReadySender` 인터페이스 + `maxJobs` 필드 + `SetReadySender` / `SetMaxJobs` / `maybeSendReady` 추가
- `Handle()` 의 defer 블럭 변경
- `Client.SendReady` 메서드 추가
- `Client.once()` 초기 READY 송신부 변경
- `cmd/agent/main.go` wiring
- 단위 테스트 (Runner + Client) — F-1 ~ F-17, F-22, F-23
- 통합 테스트 (fake Hub 스텁) — F-18 ~ F-21

**완료 기준**: F-1 ~ F-23, NF-1 ~ NF-11 통과. 사용자 시연: `--max-jobs 3` Agent 가 동시에 3 개 PR build 진행을 admin UI 에서 관찰 가능.

---

## 8. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| `inFlight` 검사와 `SendReady` 사이의 race 로 over-subscribe 가능 (1~2 개 초과) | Hub dispatcher 가 큐가 비면 READY 무시 → worst case "큐에 일이 있을 때 1~2 개 초과 build". 운영상 영향 미미. 결정 1 의 best-effort 모델로 명시. 향후 entropy 가 커지면 Runner 측에 semaphore 추가 검토. |
| `maybeSendReady` 가 `SendReady` 에러 시 루프 종료해 슬롯이 영구히 비지 않을 우려 | 다음 Handle 종료 시 다시 호출되어 재시도. WS 가 죽었으면 reconnect 가 자기 치유. F-13 / F-22 로 검증. |
| Pause 직후 진행 중 Handle 의 defer maybeSendReady 가 race 로 paused 검사를 통과해 1 회 송신할 가능성 | paused.Store(true) 가 atomic 이고, Hub 측이 그 1 개의 READY 로 assign 해도 Runner.Handle 가 paused 거절 분기로 즉시 failed 송신 → 운영자에게 노이즈는 있으나 데이터 무결성 영향 없음. F-9 로 검증. |
| Reconnect 시 HELLO 의 RunningPreviews 와 inFlight 가 mismatch (이전 build 가 새 연결 살아 있는 동안 끝나면) | maybeSendReady 가 매번 latest inFlight 를 읽으므로 자기 치유. 결정 5 의 단일 진입점 설계. |
| MaxJobs 가 10+ 등 매우 큰 값일 때 maybeSendReady 루프 안에서 동기 conn.Write 가 직렬 진행 → 초기 송신이 느림 (5 초 timeout x N) | conn.Write 정상 시 ms 단위. timeout 누적은 비정상 상태에서만. 본 Phase 는 단일 송신자 정책 유지. 비정상 시 warn 로그로 가시. |
| 운영자가 `--max-jobs` 를 머신 capacity 를 초과해 설정하면 시스템 부하 폭주 | 결정 10 의 운영자 권한 모델 + 결정 11 의 hard cap 64 로 1 차 방어. README 에 가이드(보통 머신 코어 수에 비례) 추가 권장 (본 Phase 외 문서 작업). |
| 운영자가 `--max-jobs 10000` 등 비현실적 값 입력 → maybeSendReady 가 conn.Write 폭주 | 결정 11 의 64 클램프로 차단. F-23 (신규) 로 검증. |

---

## 9. 변경 파일 목록 (참고; 구현자가 자유 결정)

```
internal/agent/ready.go           [신규] ReadySender 인터페이스 + maxJobs 필드 메서드 + maybeSendReady (NF-3 분할)
internal/agent/runner.go          [수정] Runner 구조체에 ready/maxJobs 필드 선언 + NewRunner 의 default maxJobs=1 + Handle defer 변경
internal/agent/client.go          [수정] SendReady 메서드 + once() 초기 송신 루프
internal/agent/config.go          [수정] ParseConfig 에 MaxJobs 64 hard cap 클램프 (결정 11)
internal/agent/config_test.go     [수정] F-23 클램프 검증 단위 테스트
internal/agent/ready_test.go      [신규] fakeReadySender + maybeSendReady 다중 시나리오 단위 테스트
internal/agent/runner_test.go     [수정] Handle defer 의 maybeSendReady 호출 검증 (F-6 ~ F-9)
internal/agent/client_test.go     [수정] SendReady 단위 테스트 (F-15) + 초기 READY 루프 검증 (F-17)
cmd/agent/main.go                 [수정] runner.SetReadySender(c) + SetMaxJobs(cfg.MaxJobs)
docs/specs/phase-5-multi-job.md   [본 문서]
```

> 주의: `Runner` 구조체 선언 자체는 `runner.go` 에 그대로 둔다. `ready.go` 는 같은 패키지의 보조 메서드 파일이므로 `(r *Runner)` 리시버를 그대로 사용 가능 — Go 의 분할 컴파일 단위가 패키지이므로 cross-file 메서드 정의 표준 관용. 책임 주석 헤더는 `ready.go` 가 "READY 송신 트리거" 단일 책임으로 명시.

`internal/protocol/messages.go` 는 변경 없음(Capacity 의미 보존, 결정 9).

---

## 10. 다음 Phase 연결점

- **MaxJobs 동적 reload**: 차후 Phase 에서 Hub Admin UI 에서 운영자가 MaxJobs 를 조절할 수 있게 한다면, Phase 4 의 `Holder` 와 `CONFIG_UPDATE` 패턴을 그대로 차용 가능 (`AgentConfigData` 에 `MaxJobs` 필드 추가). 본 Phase 의 `SetMaxJobs(n)` 는 이미 idempotent 라 그 시점에 재사용 가능.
- **시스템 자원 기반 자동 조절**: `MaxJobs` 가 결정 시점 고정이라는 본 Phase 의 단순 모델은, 추후 Agent 가 자기 자원을 측정해 `maybeSendReady` 의 가용 슬롯 계산을 동적으로 바꾸는 확장의 기반이 된다.
- **다중 Agent 라우팅 (label group)**: Phase 4 의 deferred 항목과 본 Phase 의 capacity 모델이 합쳐지면, "label=home 인 Agent 그룹의 총 capacity 가 N 일 때 N 개의 PR 를 동시에 분산 build" 가 가능해진다.
- **READY back-pressure**: Hub 큐가 매우 클 때 Agent 가 일부러 READY 를 늦춰 보내는 throttle — 본 Phase 의 `maybeSendReady` 단일 진입점에 시간 기반 게이트만 끼워 넣으면 된다.

---

## 11. 미해결/확인 사항 (Open Questions)

본 Phase 는 모든 항목을 본문(§3 결정, §2-2 비범위) 에서 결의했다. 이전 초안의 5 개 Q 는 다음과 같이 처리되었다.

| 이전 Q | 처리 결과 |
|---|---|
| Q1: inFlight↔SendReady race 를 mutex 로 막을 것인가 | **결정 1** 의 best-effort 모델로 종결. Hub 가 큐 비면 무시 + Handle 거절 분기로 무결성 영향 없음 → 추가 lock 불필요. |
| Q2: 초기 READY 단일 진입점 vs 명시 N 회 루프 | **결정 5 + 결정 8** 로 단일 진입점(`maybeSendReady` 1 회 호출) 확정. |
| Q3: READY rate-limit 도입 | **§2-2 Out of Scope** 로 이동. `MaxJobs ≤ 64` 가정 하에 throttle 불필요. |
| Q4: MaxJobs hard cap | **결정 11** 로 격상. `1..64` 클램프 + warn 로그. |
| Q5: Pause window 의 noise(failed 1 건) 가시화 | **§2-2 Out of Scope** 로 이동. 기존 STATUS_UPDATE failed 로그로 관측 충분. |

이후 새 Q 가 발견되면 본 섹션 아래에 (Q6, Q7 …) 으로 추가하고, plan-review 단계에서 다시 결의/비범위로 분리한다.

---

(끝)
