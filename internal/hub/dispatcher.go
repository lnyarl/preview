// 이 파일의 책임:
//   - Dispatcher: Agent 의 READY 메시지에 대한 작업 매칭/할당 진입점.
//   - 흐름(§5-1-3): GetByID → ListQueuedForCandidates → LabelsMatch → Claim →
//     RepoSecrets.List(hydration, Phase 10 결정 4) → SendJobAssign(env).
//   - Claim 실패(ErrNotFound, 다른 worker 선점)는 무시 (다음 READY 사이클로 이월).
//   - hydration 실패는 빌드 차단이 아니라 WARN + 빈 env (Phase 10 결정 9).
//
// 동시성: 같은 agent 가 여러 READY 를 보낼 수 있으나 ClaimPreview 의 SQL CAS
// 가드(WHERE status='queued')가 race-free 보장. dispatcher 자체는 stateless.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1-3, 결정 5.
//
//	docs/specs/phase-10-repo-build-secrets.md §3 결정 4/9, §4-3.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// JobSender 는 Hub 가 Agent 에게 JOB_ASSIGN 을 보내는 인터페이스.
// 실제 구현은 ConnRegistry 위에서 wsConn.Write 를 호출(server.go 또는 ws_registry.go).
// 단위 테스트는 fake 주입.
//
// Phase 10: 시그니처에 env 인자 추가. dispatcher 가 hydrate 한 build secrets 를
// wire 로 옮기는 통로. nil 허용 → JobAssignData.BuildEnv 미설정(omitempty).
type JobSender interface {
	SendJobAssign(ctx context.Context, agentID string, p store.Preview, env map[string]string) error
}

// Dispatcher 는 READY → JOB_ASSIGN 매칭/할당 로직을 보관한다.
//
// Phase 10: RepoSecrets 의존 추가. nil 허용 → hydration 자체 skip (F-21b).
type Dispatcher struct {
	AgentStore   store.AgentStore
	PreviewStore store.PreviewStore
	RepoSecrets  store.RepoSecretStore // Phase 10 (nil 허용)
	Sender       JobSender
	Logger       *slog.Logger
	now          func() time.Time
	paused       atomic.Bool // Phase 3: Pause() 호출 후 신규 OnReady 즉시 no-op.
}

// Pause 는 graceful shutdown 시 신규 OnReady 를 no-op 으로 만든다 (결정 10).
// 진행 중인 OnReady 는 끝까지 진행. atomic.Bool 로 lock 무료.
func (d *Dispatcher) Pause() { d.paused.Store(true) }

// Paused 는 현재 paused 여부를 반환한다 (테스트/진단용).
func (d *Dispatcher) Paused() bool { return d.paused.Load() }

// NewDispatcher 는 Dispatcher 를 조립한다 (결정 6: RepoURLResolver 제거).
// JobAssignData.RepoURL 은 preview.RepoCloneURL 을 직접 사용한다.
//
// Phase 10: rs 인자 추가 (RepoSecretStore). nil 허용 → 모든 OnReady 가 hydration
// skip + BuildEnv 미설정 envelope 송신. F-21b 의 호환 동선.
func NewDispatcher(as store.AgentStore, ps store.PreviewStore, rs store.RepoSecretStore, sender JobSender, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		AgentStore:   as,
		PreviewStore: ps,
		RepoSecrets:  rs,
		Sender:       sender,
		Logger:       logger,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// OnReady 는 READY 메시지 1건 처리 진입점.
// 후보 0 / 매칭 0 / Claim 실패(ErrNotFound) 모두 nil 반환 (no-op).
// Sender.SendJobAssign 실패는 에러로 반환되며 호출자(WS handler) 가 로그.
//
// Phase 3: Pause() 후 호출되면 즉시 nil 반환 (결정 10).
//
// Phase 10 (결정 4 / 9): Claim 성공 직후 RepoSecrets.List(repo) 로 hydration.
// nil store / 에러는 WARN + 빈 env 로 진행 (dispatch 차단 안 함).
func (d *Dispatcher) OnReady(ctx context.Context, agentID string) error {
	if d.paused.Load() {
		return nil
	}
	agent, err := d.AgentStore.GetByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("dispatcher.OnReady: get agent: %w", err)
	}

	candidates, err := d.PreviewStore.ListQueuedForCandidates(ctx)
	if err != nil {
		return fmt.Errorf("dispatcher.OnReady: list candidates: %w", err)
	}
	matched := make([]string, 0, len(candidates))
	for _, p := range candidates {
		if LabelsMatch(p.Labels, agent.Labels) {
			matched = append(matched, p.ID)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	claimed, err := d.PreviewStore.Claim(ctx, matched, agentID, d.now())
	if errors.Is(err, store.ErrNotFound) {
		// 다른 worker 선점 — 다음 READY 로 이월.
		return nil
	}
	if err != nil {
		return fmt.Errorf("dispatcher.OnReady: claim: %w", err)
	}

	d.Logger.Info("dispatcher_assigned",
		"preview_id", claimed.ID,
		"agent_id", agentID,
		"repo", claimed.RepoFullName,
		"pr", claimed.PrNumber,
	)

	// Phase 10: secret hydration. nil store → skip (F-21b).
	// 에러는 WARN + 빈 env (결정 9 / R-4).
	env := d.hydrateBuildEnv(ctx, claimed.RepoFullName, claimed.ID)
	return d.Sender.SendJobAssign(ctx, agentID, *claimed, env)
}

// hydrateBuildEnv 는 RepoSecrets.List 호출 결과를 map[string]string 으로 빌드한다.
// nil store → nil map. 에러 → WARN + nil map (결정 9). 빈 결과 → nil map (omitempty).
//
// 한 OnReady 에 한 번만 호출됨이 보장되어 dispatch latency 가산은 SQL 1건 (R-4 완화).
func (d *Dispatcher) hydrateBuildEnv(ctx context.Context, repoFullName, previewID string) map[string]string {
	if d.RepoSecrets == nil {
		return nil
	}
	rows, err := d.RepoSecrets.List(ctx, repoFullName)
	if err != nil {
		d.Logger.Warn("dispatcher_secret_hydrate_failed",
			"repo", repoFullName,
			"preview_id", previewID,
			"err", err.Error(),
		)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	env := make(map[string]string, len(rows))
	for _, r := range rows {
		env[r.Key] = r.Value
	}
	d.Logger.Info("dispatcher_secret_hydrated",
		"repo", repoFullName,
		"preview_id", previewID,
		"n_keys", len(env),
	)
	return env
}

// JobAssignFromPreview 는 store.Preview + env 를 JobAssignData 로 변환한다.
// RepoURL 은 preview.RepoCloneURL 우선, 비어있으면 RepoFullName fallback (결정 6/7).
// JobSender 구현체가 이 헬퍼를 호출해 wire envelope 를 만든다.
//
// Phase 10: env map 인자 추가. nil/빈 → BuildEnv 미설정 (omitempty).
func JobAssignFromPreview(p store.Preview, env map[string]string) protocol.JobAssignData {
	repoURL := p.RepoCloneURL
	if repoURL == "" {
		repoURL = p.RepoFullName
	}
	data := protocol.JobAssignData{
		PreviewID:    p.ID,
		RepoFullName: p.RepoFullName,
		RepoURL:      repoURL,
		CommitSHA:    p.CommitSha,
		Branch:       p.Branch,
		Labels:       p.Labels,
	}
	if len(env) > 0 {
		data.BuildEnv = env
	}
	return data
}
