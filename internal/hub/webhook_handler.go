// 이 파일의 책임:
//   - POST /webhooks/github : GitHub webhook 수신 + HMAC 검증 + previews UPSERT.
//   - GET /admin/previews : 관리자 list (Step 1 evaluator 검증 편의용).
//   - GET /admin/previews/{id} : 단일 조회.
//
// HMAC 검증은 crypto/hmac.Equal 사용 (timing-safe, 결정 3 / NF-Security-1).
// secret 부재는 cmd/hub Config.Validate() 에서 fail-fast 하므로 본 핸들러는
// secret 비어있지 않음을 가정한다(NF-Security-3).
//
// 응답 코드 매핑(결정 15):
//   - X-Hub-Signature-256 누락/형식 오류 → 401 missing_signature
//   - HMAC mismatch                       → 401 invalid_signature
//   - 본문 JSON 파싱 실패                 → 400 invalid_payload
//   - X-GitHub-Event != pull_request       → 200 ignored
//   - action != opened|synchronize|reopened|closed → 200 ignored
//   - 정상 처리                           → 202 {preview_id, status}
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-3, §5-5, 결정 11/15.
package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/store"
)

// pullRequestEvent 는 GitHub `pull_request` webhook 의 minimal DTO (결정 7).
// json.Unmarshal 은 unknown field 를 무시하므로 GitHub 페이로드 변경에 안전.
type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"` // Phase 6: git clone URL (결정 6)
	} `json:"repository"`
}

// TeardownSender 는 Agent 에게 JOB_TEARDOWN 을 송신하는 인터페이스.
// 구현: ConnRegistry 위의 WSJobSender (ws_registry.go).
type TeardownSender interface {
	SendTeardown(ctx context.Context, agentID, previewID string) error
}

// WebhookHandler 는 /webhooks/github 와 /admin/previews 를 처리한다.
// TeardownSender 는 옵션 — nil 일 때 JOB_TEARDOWN 송신만 스킵한다.
// Phase 6 (결정 12): CacheNotifier/ProxyMiddleware 제거.
type WebhookHandler struct {
	Store          store.PreviewStore
	WebhookSecret  []byte
	Logger         *slog.Logger
	TeardownSender TeardownSender  // optional, nil-safe
	UI             *AdminUIHandler // Phase 3: SSR 분기 (옵션, 결정 6)
	now            func() time.Time
}

// SetUI 는 SSR 핸들러를 주입한다 (Phase 3, 결정 6).
func (h *WebhookHandler) SetUI(ui *AdminUIHandler) { h.UI = ui }

// NewWebhookHandler 는 WebhookHandler 를 조립한다. secret 은 비어있지 않다고 가정.
func NewWebhookHandler(s store.PreviewStore, secret string, logger *slog.Logger) *WebhookHandler {
	return &WebhookHandler{
		Store:         s,
		WebhookSecret: []byte(secret),
		Logger:        logger,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// Register 는 mux 에 webhook 및 /admin/previews 라우트를 추가한다.
func (h *WebhookHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/github", h.handleWebhook)
	mux.HandleFunc("GET /admin/previews", h.listPreviews)
	mux.HandleFunc("GET /admin/previews/{id}", h.getPreview)
	mux.HandleFunc("DELETE /admin/previews/{id}", h.deletePreview)
}

// SetTeardownSender 는 JOB_TEARDOWN 송신자를 주입한다.
func (h *WebhookHandler) SetTeardownSender(s TeardownSender) { h.TeardownSender = s }

// PreviewView 는 /admin/previews 응답 DTO. nullable 필드는 *string/*int 로 명시.
//
// Phase 6: public_url 제거. preview_urls (Agent 가 산출한 service→URL 매핑의 raw
// JSON 직렬화 문자열) 와 repo_clone_url (webhook 에서 추출한 git clone URL) 추가.
//
// Phase 9: is_adhoc 추가. Admin Test Build 로 만들어진 row 는 true, webhook 발 false.
type PreviewView struct {
	ID              string   `json:"id"`
	RepoFullName    string   `json:"repo_full_name"`
	PrNumber        int      `json:"pr_number"`
	CommitSha       string   `json:"commit_sha"`
	Branch          string   `json:"branch"`
	Status          string   `json:"status"`
	AssignedAgentID *string  `json:"assigned_agent_id"`
	ContainerID     *string  `json:"container_id"`
	AgentHost       *string  `json:"agent_host"`
	AgentPort       *int     `json:"agent_port"`
	RepoCloneURL    string   `json:"repo_clone_url"`
	PreviewURLs     string   `json:"preview_urls"`
	Labels          []string `json:"labels"`
	ErrorMessage    *string  `json:"error_message"`
	IsAdhoc         bool     `json:"is_adhoc"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// PreviewToView 는 도메인 Preview 를 HTTP 응답 DTO 로 변환한다.
// cmd/hub previews list 서브커맨드도 동일 shape 을 사용하므로 export 됨.
func PreviewToView(p store.Preview) PreviewView {
	labels := p.Labels
	if labels == nil {
		labels = []string{}
	}
	return PreviewView{
		ID:              p.ID,
		RepoFullName:    p.RepoFullName,
		PrNumber:        p.PrNumber,
		CommitSha:       p.CommitSha,
		Branch:          p.Branch,
		Status:          p.Status,
		AssignedAgentID: p.AssignedAgentID,
		ContainerID:     p.ContainerID,
		AgentHost:       p.AgentHost,
		AgentPort:       p.AgentPort,
		RepoCloneURL:    p.RepoCloneURL,
		PreviewURLs:     p.PreviewURLs,
		Labels:          labels,
		ErrorMessage:    p.ErrorMessage,
		IsAdhoc:         p.IsAdhoc,
		CreatedAt:       p.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// webhookMaxBodyBytes 는 DoS 방지를 위한 webhook 페이로드 최대 크기 (M-3).
const webhookMaxBodyBytes = 10 << 20 // 10 MiB

func (h *WebhookHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// 본문 raw 읽기 — HMAC 계산 후 JSON decode.
	r.Body = http.MaxBytesReader(w, r.Body, webhookMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", "read body failed")
		return
	}

	sigHdr := r.Header.Get("X-Hub-Signature-256")
	if sigHdr == "" || !strings.HasPrefix(sigHdr, "sha256=") {
		writeError(w, http.StatusUnauthorized, "missing_signature", "X-Hub-Signature-256 header missing or malformed")
		return
	}
	sigHex := strings.TrimPrefix(sigHdr, "sha256=")
	expectedHex := computeHMACSHA256(h.WebhookSecret, body)
	// hex 디코드 후 hmac.Equal — 길이 차이도 timing-safe.
	receivedBytes, decodeErr := hex.DecodeString(sigHex)
	if decodeErr != nil {
		writeError(w, http.StatusUnauthorized, "invalid_signature", "signature hex decode failed")
		return
	}
	expectedBytes, _ := hex.DecodeString(expectedHex)
	if !hmac.Equal(expectedBytes, receivedBytes) {
		writeError(w, http.StatusUnauthorized, "invalid_signature", "HMAC mismatch")
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "pull_request" {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "event": event})
		return
	}

	var p pullRequestEvent
	if err := json.Unmarshal(body, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_payload", "JSON parse failed")
		return
	}

	// PR 번호: pull_request.number 우선, 없으면 number.
	prNum := p.PullRequest.Number
	if prNum == 0 {
		prNum = p.Number
	}
	if prNum == 0 || p.Repository.FullName == "" {
		writeError(w, http.StatusBadRequest, "invalid_payload", "missing pr number or repo full_name")
		return
	}

	switch p.Action {
	case "opened", "synchronize", "reopened":
		h.handleUpsert(w, r.Context(), p, prNum)
	case "closed":
		h.handleClose(w, r.Context(), p, prNum)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "action": p.Action})
	}
}

// handleUpsert 는 opened/synchronize/reopened 처리.
//
// Phase 9 Decision Matrix (synchronize 액션 기준, §5-6):
//   - Case A (active 존재 + 같은 sha): Upsert idempotent — ON CONFLICT 가 같은 row
//     UPDATE. teardown / 신규 INSERT 없음.
//   - Case B (active 존재 + 다른 sha): 기존 active row teardown(status 전이 + JOB_TEARDOWN)
//   - 신규 INSERT(status='queued') + 신규 row 의 첫 event 에 supersede 메모.
//   - Case C (active 없음 + 같은 sha 의 done/failed): Upsert ON CONFLICT 발동 → reopen.
//   - Case D (active 없음 + 새 sha): 신규 INSERT 만.
//
// opened/reopened 는 Case A/B 분기를 적용하지 않는다(통상 같은 PR 의 active 가 없는 흐름).
//
// Adhoc 진입점이 아니므로 IsAdhoc=false.
func (h *WebhookHandler) handleUpsert(w http.ResponseWriter, ctx context.Context, p pullRequestEvent, prNum int) {
	now := h.now()
	labels := labelsFromPR(p)
	id := uuid.NewString()
	newSha := p.PullRequest.Head.SHA
	if p.Repository.CloneURL == "" {
		h.Logger.Warn("webhook_clone_url_missing", "repo", p.Repository.FullName, "pr", prNum)
	}

	// === synchronize: Case B (다른 sha) 사전 처리 ===
	// 기존 active row 가 있고 sha 가 다르면 teardown + JOB_TEARDOWN 송신.
	// supersededID 는 신규 INSERT 후 첫 event 에 supersede 정보를 남기기 위해 보존.
	supersededID := ""
	if p.Action == "synchronize" {
		existing, gerr := h.Store.GetActiveByRepoAndPR(ctx, p.Repository.FullName, prNum)
		if gerr == nil && existing != nil && existing.CommitSha != newSha {
			if uerr := h.Store.UpdateStatus(ctx, existing.ID, "", "teardown",
				"superseded_by_sha="+newSha, now, store.PreviewFields{}); uerr != nil {
				h.Logger.Error("preview_supersede_teardown_failed", "err", uerr.Error(), "preview_id", existing.ID)
				writeError(w, http.StatusInternalServerError, "internal", "supersede failed")
				return
			}
			if h.TeardownSender != nil && existing.AssignedAgentID != nil {
				if terr := h.TeardownSender.SendTeardown(ctx, *existing.AssignedAgentID, existing.ID); terr != nil {
					h.Logger.Warn("teardown_send_failed", "preview_id", existing.ID,
						"agent_id", *existing.AssignedAgentID, "err", terr.Error())
				}
			}
			supersededID = existing.ID
		}
		// Case A 또는 Case C/D 는 fall through. Upsert 가 idempotent / reopen / 신규 INSERT 처리.
	}

	preview := store.Preview{
		ID:           id,
		RepoFullName: p.Repository.FullName,
		PrNumber:     prNum,
		CommitSha:    newSha,
		Branch:       p.PullRequest.Head.Ref,
		Labels:       labels,
		RepoCloneURL: p.Repository.CloneURL,
		IsAdhoc:      false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	created, prev, err := h.Store.Upsert(ctx, preview)
	if err != nil {
		h.Logger.Error("preview_upsert_failed", "err", err.Error(), "repo", p.Repository.FullName, "pr", prNum)
		writeError(w, http.StatusInternalServerError, "internal", "upsert failed")
		return
	}

	finalID := id
	finalStatus := "queued"
	switch {
	case !created && prev != nil:
		// Case A 또는 Case C — 기존 row 가 있는 분기.
		finalID = prev.ID
		finalStatus = prev.Status
		// Case C: done/failed 였다면 reopen.
		if prev.Status == "done" || prev.Status == "failed" {
			if uerr := h.Store.UpdateStatus(ctx, prev.ID, prev.Status, "queued",
				"reopened_by_"+p.Action, now, store.PreviewFields{}); uerr != nil {
				h.Logger.Error("preview_reopen_failed", "err", uerr.Error(), "preview_id", prev.ID)
				writeError(w, http.StatusInternalServerError, "internal", "reopen failed")
				return
			}
			finalStatus = "queued"
		}
	case created && supersededID != "":
		// Case B — 신규 INSERT 직후 자기-루프 UpdateStatus 로 supersede 추적 message 기록.
		if uerr := h.Store.UpdateStatus(ctx, id, "queued", "queued",
			"created_after_supersede_of="+supersededID, now, store.PreviewFields{}); uerr != nil {
			// supersede event 기록 실패는 운영 timeline 가독성 손실에 그치므로 WARN 만 남기고 진행.
			h.Logger.Warn("preview_supersede_event_failed", "err", uerr.Error(), "preview_id", id)
		}
	}

	h.Logger.Info("preview_webhook_processed",
		"action", p.Action,
		"preview_id", finalID,
		"repo", p.Repository.FullName,
		"pr", prNum,
		"status", finalStatus,
		"created", created,
		"is_adhoc", false,
		"superseded_id", supersededID,
	)
	writeJSON(w, http.StatusAccepted, map[string]any{"preview_id": finalID, "status": finalStatus})
}

// handleClose 는 closed action 처리. 기존 row 가 없으면 200 ignored.
// 있으면 UpdateStatus(*→teardown) 호출.
func (h *WebhookHandler) handleClose(w http.ResponseWriter, ctx context.Context, p pullRequestEvent, prNum int) {
	// 기존 row 조회 — Upsert 후 prev 를 받는 경로 대신, 명시적으로 GetByRepoAndPR
	// 가 인터페이스에 없으므로 ListAll + 필터로 단순화. row 수가 적은 Step 1 환경에선 충분.
	existing, err := findPreviewByRepoPR(ctx, h.Store, p.Repository.FullName, prNum)
	if err != nil {
		h.Logger.Error("preview_close_lookup_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "lookup failed")
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "no_existing_preview"})
		return
	}
	now := h.now()
	errMsg := "closed_via_webhook"
	if err := h.Store.UpdateStatus(ctx, existing.ID, "", "teardown", errMsg, now, store.PreviewFields{ErrorMessage: &errMsg}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "no_existing_preview"})
			return
		}
		h.Logger.Error("preview_close_failed", "err", err.Error(), "preview_id", existing.ID)
		writeError(w, http.StatusInternalServerError, "internal", "close failed")
		return
	}

	// JOB_TEARDOWN 송신 (assigned/running/building 상태의 agent 에게).
	// 에러는 무시 — reconciler 가 후속 처리.
	if h.TeardownSender != nil && existing.AssignedAgentID != nil {
		if err := h.TeardownSender.SendTeardown(ctx, *existing.AssignedAgentID, existing.ID); err != nil {
			h.Logger.Warn("teardown_send_failed", "preview_id", existing.ID,
				"agent_id", *existing.AssignedAgentID, "err", err.Error())
		}
	}

	h.Logger.Info("preview_webhook_processed",
		"action", "closed",
		"preview_id", existing.ID,
		"repo", p.Repository.FullName,
		"pr", prNum,
		"status", "teardown",
	)
	writeJSON(w, http.StatusAccepted, map[string]any{"preview_id": existing.ID, "status": "teardown"})
}

// deletePreview 는 DELETE /admin/previews/{id} 핸들러.
// 강제 teardown — 호출 후 status=teardown, agent 에 JOB_TEARDOWN 송신, 캐시 무효화.
func (h *WebhookHandler) deletePreview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "preview id required")
		return
	}
	p, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "preview not found")
			return
		}
		h.Logger.Error("preview_delete_lookup_failed", "err", err.Error(), "preview_id", id)
		writeError(w, http.StatusInternalServerError, "internal", "get failed")
		return
	}
	now := h.now()
	if err := h.Store.UpdateStatus(r.Context(), id, "", "teardown", "manual_teardown", now, store.PreviewFields{}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "preview not found")
			return
		}
		h.Logger.Error("preview_delete_failed", "err", err.Error(), "preview_id", id)
		writeError(w, http.StatusInternalServerError, "internal", "teardown failed")
		return
	}
	if h.TeardownSender != nil && p.AssignedAgentID != nil {
		if err := h.TeardownSender.SendTeardown(r.Context(), *p.AssignedAgentID, id); err != nil {
			h.Logger.Warn("teardown_send_failed", "preview_id", id,
				"agent_id", *p.AssignedAgentID, "err", err.Error())
		}
	}
	h.Logger.Info("preview_manual_teardown", "preview_id", id)
	writeJSON(w, http.StatusAccepted, map[string]any{"preview_id": id, "status": "teardown"})
}

// findPreviewByRepoPR 은 PreviewStore 인터페이스 위에서 (repo, pr) 조회를 수행.
// Step 1 에서는 ListAll + 메모리 필터(인터페이스에 GetByRepoAndPR 추가를 보류).
// Step 2/3 에서 ListAllPreviews 를 GetPreviewByRepoAndPR 로 교체할 수 있다.
func findPreviewByRepoPR(ctx context.Context, s store.PreviewStore, repo string, prNum int) (*store.Preview, error) {
	all, err := s.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].RepoFullName == repo && all[i].PrNumber == prNum {
			p := all[i]
			return &p, nil
		}
	}
	return nil, nil
}

func (h *WebhookHandler) listPreviews(w http.ResponseWriter, r *http.Request) {
	// Phase 3: SSR 분기 (Accept != application/json) — 결정 6.
	if !wantsJSON(r) && h.UI != nil {
		h.UI.PreviewsList(w, r)
		return
	}
	list, err := h.Store.ListAll(r.Context())
	if err != nil {
		h.Logger.Error("preview_list_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	views := make([]PreviewView, 0, len(list))
	for _, p := range list {
		views = append(views, PreviewToView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

func (h *WebhookHandler) getPreview(w http.ResponseWriter, r *http.Request) {
	// Phase 3: SSR 분기 (Accept != application/json) — 결정 6.
	if !wantsJSON(r) && h.UI != nil {
		h.UI.PreviewDetail(w, r)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "preview id required")
		return
	}
	p, err := h.Store.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "preview not found")
			return
		}
		h.Logger.Error("preview_get_failed", "err", err.Error(), "preview_id", id)
		writeError(w, http.StatusInternalServerError, "internal", "get failed")
		return
	}
	writeJSON(w, http.StatusOK, PreviewToView(*p))
}

// computeHMACSHA256 은 secret + payload 의 HMAC-SHA256 을 lowercase hex 로 반환한다.
func computeHMACSHA256(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// labelsFromPR 은 GitHub PR labels 배열을 라벨 문자열 슬라이스로 변환한다.
// GitHub label name 그대로를 값으로 보존한다. 빈 PR 라벨은 빈 슬라이스([]string{}).
// dispatcher 는 set-membership 매칭으로 이 슬라이스를 사용한다.
func labelsFromPR(p pullRequestEvent) []string {
	if len(p.PullRequest.Labels) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(p.PullRequest.Labels))
	for _, l := range p.PullRequest.Labels {
		out = append(out, l.Name)
	}
	return out
}
