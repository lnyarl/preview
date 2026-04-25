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
	} `json:"repository"`
}

// WebhookHandler 는 /webhooks/github 와 /admin/previews 를 처리한다.
type WebhookHandler struct {
	Store         store.PreviewStore
	WebhookSecret []byte
	Logger        *slog.Logger
	now           func() time.Time
}

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
}

// PreviewView 는 /admin/previews 응답 DTO. nullable 필드는 *string/*int 로 명시.
type PreviewView struct {
	ID              string            `json:"id"`
	RepoFullName    string            `json:"repo_full_name"`
	PrNumber        int               `json:"pr_number"`
	CommitSha       string            `json:"commit_sha"`
	Branch          string            `json:"branch"`
	Status          string            `json:"status"`
	AssignedAgentID *string           `json:"assigned_agent_id"`
	ContainerID     *string           `json:"container_id"`
	AgentHost       *string           `json:"agent_host"`
	AgentPort       *int              `json:"agent_port"`
	PublicURL       *string           `json:"public_url"`
	Labels          map[string]string `json:"labels"`
	ErrorMessage    *string           `json:"error_message"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

// PreviewToView 는 도메인 Preview 를 HTTP 응답 DTO 로 변환한다.
// cmd/hub previews list 서브커맨드도 동일 shape 을 사용하므로 export 됨.
func PreviewToView(p store.Preview) PreviewView {
	labels := p.Labels
	if labels == nil {
		labels = map[string]string{}
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
		PublicURL:       p.PublicURL,
		Labels:          labels,
		ErrorMessage:    p.ErrorMessage,
		CreatedAt:       p.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (h *WebhookHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// 본문 raw 읽기 — HMAC 계산 후 JSON decode.
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
// 신규는 INSERT(status='queued'), 기존은 UPDATE(필드만).
// 기존 row 의 status 가 done|failed 였다면 별도로 UpdateStatus(*→queued) 호출
// (결정 11: status 변경은 UpdateStatus 단일 진입점).
func (h *WebhookHandler) handleUpsert(w http.ResponseWriter, ctx context.Context, p pullRequestEvent, prNum int) {
	now := h.now()
	labels := labelsFromPR(p)
	id := uuid.NewString()
	preview := store.Preview{
		ID:           id,
		RepoFullName: p.Repository.FullName,
		PrNumber:     prNum,
		CommitSha:    p.PullRequest.Head.SHA,
		Branch:       p.PullRequest.Head.Ref,
		Labels:       labels,
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
	if !created {
		// 기존 row — Upsert 로 sha/labels/branch 갱신 완료. ID 는 기존 row 의 것을 사용.
		// 재오픈(prev.Status in done|failed)이면 UpdateStatus 호출.
		if prev != nil {
			finalID = prev.ID
			finalStatus = prev.Status
			if prev.Status == "done" || prev.Status == "failed" {
				if uerr := h.Store.UpdateStatus(ctx, prev.ID, prev.Status, "queued", "reopened_via_webhook", now, store.PreviewFields{}); uerr != nil {
					h.Logger.Error("preview_reopen_failed", "err", uerr.Error(), "preview_id", prev.ID)
					writeError(w, http.StatusInternalServerError, "internal", "reopen failed")
					return
				}
				finalStatus = "queued"
			}
		}
	}
	h.Logger.Info("preview_webhook_processed",
		"action", p.Action,
		"preview_id", finalID,
		"repo", p.Repository.FullName,
		"pr", prNum,
		"status", finalStatus,
		"created", created,
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
	h.Logger.Info("preview_webhook_processed",
		"action", "closed",
		"preview_id", existing.ID,
		"repo", p.Repository.FullName,
		"pr", prNum,
		"status", "teardown",
	)
	writeJSON(w, http.StatusAccepted, map[string]any{"preview_id": existing.ID, "status": "teardown"})
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

// labelsFromPR 은 GitHub PR labels 배열을 map[string]string 로 변환한다.
// Phase 2 의 라벨 매칭(결정 4) 에서 key=value 형태가 표준이지만, GitHub label 은
// 단순 문자열이므로 본 Step 에서는 name=name 매핑(존재 자체를 표현)으로 둔다.
// Step 2 의 dispatcher 는 이 형태를 그대로 사용한다.
func labelsFromPR(p pullRequestEvent) map[string]string {
	if len(p.PullRequest.Labels) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(p.PullRequest.Labels))
	for _, l := range p.PullRequest.Labels {
		out[l.Name] = l.Name
	}
	return out
}
