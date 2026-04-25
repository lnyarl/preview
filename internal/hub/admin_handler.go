// 이 파일의 책임:
//   - POST /admin/agents (생성 + 토큰 1회 반환)
//   - GET  /admin/agents (목록)
//   - DELETE /admin/agents/{id}
//   - GET /health
//
// 에러 응답 shape: { "error": "<code>", "message": "..." } (§5-3).
// 공통 code: invalid_name, duplicate_name, not_found, invalid_token, missing_auth, internal.
package hub

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/store"
)

// nameRE 는 CreateAgentRequest.name 검증 정규식. ^[a-zA-Z0-9_-]{1,64}$
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// AdminHandler 는 /admin/* 및 /health 를 처리한다.
type AdminHandler struct {
	Store    store.AgentStore
	TokenGen *token.Generator
	Logger   *slog.Logger
	UI       *AdminUIHandler // Phase 3: SSR 분기 핸들러 (옵션, nil 이면 JSON-only).
}

// NewAdminHandler 는 AdminHandler 를 조립한다.
func NewAdminHandler(s store.AgentStore, tg *token.Generator, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{Store: s, TokenGen: tg, Logger: logger}
}

// SetUI 는 SSR 핸들러를 주입한다 (Phase 3, 결정 6).
// nil 이면 기존 JSON-only 동작.
func (h *AdminHandler) SetUI(ui *AdminUIHandler) { h.UI = ui }

// Register 는 mux 에 /health, /admin/agents, /admin/agents/{id} 라우트를 붙인다.
// Phase 3: GET /admin/agents 와 POST /admin/agents 는 Accept-header 분기 (결정 6).
func (h *AdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /admin/agents", h.createAgent)
	mux.HandleFunc("GET /admin/agents", h.listAgents)
	mux.HandleFunc("DELETE /admin/agents/{id}", h.deleteAgent)
}

// RegisterAdminOnly 는 mux 에 /admin/* 라우트만 등록한다 (server.go 의 adminMux 분리용).
// /health 는 별도로 mainMux 에 register.
func (h *AdminHandler) RegisterAdminOnly(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/agents", h.createAgent)
	mux.HandleFunc("GET /admin/agents", h.listAgents)
	mux.HandleFunc("DELETE /admin/agents/{id}", h.deleteAgent)
}

// Health 는 /health 엔드포인트의 핸들러 export — server.go 가 mainMux 에 직접 등록.
func (h *AdminHandler) Health(w http.ResponseWriter, r *http.Request) { h.health(w, r) }

// wantsJSON 는 Accept 헤더에 application/json 이 포함되었는지 판단.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// createAgentRequest / response 는 §5-2 Admin API body schema.
type createAgentRequest struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels,omitempty"`
}

type createAgentResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// AgentView 는 GET 응답용 DTO. token_hash 필드를 일체 노출하지 않는다.
type AgentView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Labels     []string `json:"labels"`
	Status     string   `json:"status"`
	LastSeenAt *string  `json:"last_seen_at"`
	CreatedAt  string   `json:"created_at"`
}

func (h *AdminHandler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *AdminHandler) createAgent(w http.ResponseWriter, r *http.Request) {
	// Phase 3: Accept != application/json 이면 SSR 폼 흐름 (결정 6).
	if !wantsJSON(r) && h.UI != nil {
		h.UI.CreateAgentForm(w, r)
		return
	}
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", "request body must be JSON")
		return
	}
	if !nameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must match ^[a-zA-Z0-9_-]{1,64}$")
		return
	}
	raw, hash, err := h.TokenGen.Generate()
	if err != nil {
		h.Logger.Error("token_generate_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "token generation failed")
		return
	}
	labels := req.Labels
	if labels == nil {
		labels = []string{}
	}
	a := store.Agent{
		ID:        uuid.NewString(),
		Name:      req.Name,
		TokenHash: hash,
		Labels:    labels,
		Status:    "offline",
		CreatedAt: time.Now().UTC(),
	}
	if err := h.Store.Create(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "duplicate_name", "agent name already exists")
			return
		}
		h.Logger.Error("agent_create_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "create failed")
		return
	}
	h.Logger.Info("agent_registered", "agent_id", a.ID, "name", a.Name)
	writeJSON(w, http.StatusCreated, createAgentResponse{
		ID:    a.ID,
		Name:  a.Name,
		Token: raw,
	})
}

func (h *AdminHandler) listAgents(w http.ResponseWriter, r *http.Request) {
	// Phase 3: Accept != application/json 이면 SSR (결정 6).
	if !wantsJSON(r) && h.UI != nil {
		h.UI.AgentsList(w, r)
		return
	}
	list, err := h.Store.List(r.Context())
	if err != nil {
		h.Logger.Error("agent_list_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	views := make([]AgentView, 0, len(list))
	for _, a := range list {
		views = append(views, toView(a))
	}
	writeJSON(w, http.StatusOK, views)
}

func (h *AdminHandler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "agent id required")
		return
	}
	if err := h.Store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "agent not found")
			return
		}
		h.Logger.Error("agent_delete_failed", "err", err.Error(), "agent_id", id)
		writeError(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	h.Logger.Info("agent_deleted", "agent_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// toView 는 도메인 Agent 를 HTTP 응답 DTO 로 변환한다. token_hash 를 제거.
func toView(a store.Agent) AgentView {
	labels := a.Labels
	if labels == nil {
		labels = []string{}
	}
	v := AgentView{
		ID:        a.ID,
		Name:      a.Name,
		Labels:    labels,
		Status:    a.Status,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if a.LastSeenAt != nil {
		s := a.LastSeenAt.UTC().Format(time.RFC3339Nano)
		v.LastSeenAt = &s
	}
	return v
}

// writeJSON 은 응답 헤더를 세팅하고 JSON 을 직렬화한다.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 는 §5-3 에러 shape 로 응답한다.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}
