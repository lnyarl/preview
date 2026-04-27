// 이 파일의 책임:
//   - Phase 10 Admin UI 핸들러 묶음: /admin/repos 인덱스, /admin/repos/{owner}/{repo}/secrets GET/POST.
//   - textarea 한 덩어리(KEY=VALUE 라인)를 파싱·검증·diff 한 뒤 store 에 적용한다 (결정 2).
//   - 모든 진입점에서 repo_full_name 을 strings.ToLower 정규화 (결정 15).
//   - slog 키는 admin_ui_repo_secrets_* 컨벤션 (NF-Slog-1).
//
// 파싱/직렬화 헬퍼는 admin_ui_repo_secrets_parse.go 로 분리 (NF-File-2 준수).
//
// 참고: docs/specs/phase-10-repo-build-secrets.md §5-4 ~ §5-8.
package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lnyarl/preview/internal/store"
)

// reposIndexView 는 /admin/repos 의 렌더 모델.
type reposIndexView struct {
	Title string
	Rows  []repoRow
	Error string
}

// repoRow 는 인덱스 표 한 행 (결정 14).
type repoRow struct {
	RepoFullName string
	SecretCount  int
	EditURLPath  string
}

// repoSecretsView 는 /admin/repos/{owner}/{repo}/secrets 의 렌더 모델.
type repoSecretsView struct {
	Title           string
	RepoFullName    string
	OwnerSegment    string
	RepoSegment     string
	RawText         string
	SavedFlash      bool
	AddedN          int
	UpdatedN        int
	RemovedN        int
	ValidationError string
	Error           string
}

// reposIndex 는 GET /admin/repos 핸들러.
// previews + repo_secrets 의 union 을 정렬해 secret 카운트와 함께 표시한다 (결정 14).
func (h *AdminUIHandler) reposIndex(w http.ResponseWriter, r *http.Request) {
	view := reposIndexView{Title: "Repositories"}

	all := map[string]struct{}{}
	if h.PreviewStore != nil {
		repos, err := h.PreviewStore.ListRepos(r.Context())
		if err != nil {
			h.Logger.Warn("admin_ui_repo_secrets_index_previews_failed", "err", err.Error())
			view.Error = "failed to list previews"
		} else {
			for _, repo := range repos {
				if repo == "" {
					continue
				}
				all[strings.ToLower(repo)] = struct{}{}
			}
		}
	}
	if h.RepoSecrets != nil {
		repos, err := h.RepoSecrets.ListRepos(r.Context())
		if err != nil {
			h.Logger.Warn("admin_ui_repo_secrets_index_secrets_failed", "err", err.Error())
			if view.Error == "" {
				view.Error = "failed to list repo secrets"
			}
		} else {
			for _, repo := range repos {
				if repo == "" {
					continue
				}
				all[strings.ToLower(repo)] = struct{}{}
			}
		}
	}

	repos := make([]string, 0, len(all))
	for repo := range all {
		repos = append(repos, repo)
	}
	sort.Strings(repos)

	view.Rows = make([]repoRow, 0, len(repos))
	for _, repo := range repos {
		count := 0
		if h.RepoSecrets != nil {
			rows, err := h.RepoSecrets.List(r.Context(), repo)
			if err != nil {
				h.Logger.Warn("admin_ui_repo_secrets_index_count_failed",
					"repo", repo, "err", err.Error())
			} else {
				count = len(rows)
			}
		}
		view.Rows = append(view.Rows, repoRow{
			RepoFullName: repo,
			SecretCount:  count,
			EditURLPath:  "/admin/repos/" + repo + "/secrets",
		})
	}

	h.renderHTML(w, http.StatusOK, "repos.gohtml", view)
}

// repoSecretsGet 는 GET /admin/repos/{owner}/{repo}/secrets 핸들러.
// 빈 path segment 는 404. DB 에 row 없는 repo 도 200 (사전 등록 동선, F-13).
func (h *AdminUIHandler) repoSecretsGet(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.PathValue("owner"))
	repo := strings.TrimSpace(r.PathValue("repo"))
	if owner == "" || repo == "" {
		http.Error(w, "owner and repo required", http.StatusNotFound)
		return
	}
	repoFullName := strings.ToLower(owner + "/" + repo)

	view := repoSecretsView{
		Title:        "Secrets: " + repoFullName,
		RepoFullName: repoFullName,
		OwnerSegment: strings.ToLower(owner),
		RepoSegment:  strings.ToLower(repo),
		SavedFlash:   r.URL.Query().Get("msg") == "saved",
	}

	if h.RepoSecrets != nil {
		rows, err := h.RepoSecrets.List(r.Context(), repoFullName)
		if err != nil {
			h.Logger.Error("admin_ui_repo_secrets_get_list_failed",
				"repo", repoFullName, "err", err.Error())
			view.Error = "failed to load secrets"
		} else {
			view.RawText = secretsToRawText(rows)
		}
	}

	h.renderHTML(w, http.StatusOK, "repo_secrets.gohtml", view)
}

// repoSecretsPost 는 POST /admin/repos/{owner}/{repo}/secrets 핸들러.
//
// 흐름 (§5-4 / 결정 2 / 결정 3a~3c):
//  1. ParseForm → raw textarea 추출.
//  2. parseSecretsRaw 로 line 단위 파싱 (validation 실패 시 200 재렌더 + Error).
//  3. RepoSecrets.List 로 현재 row 조회 → diff (added/updated/removed).
//  4. Upsert/Delete 적용 (개별 호출 — sqlite single-writer 환경에서 sequential 해도 race-free).
//  5. 303 → ?msg=saved.
func (h *AdminUIHandler) repoSecretsPost(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.PathValue("owner"))
	repo := strings.TrimSpace(r.PathValue("repo"))
	if owner == "" || repo == "" {
		http.Error(w, "owner and repo required", http.StatusNotFound)
		return
	}
	repoFullName := strings.ToLower(owner + "/" + repo)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	raw := r.FormValue("raw")

	view := repoSecretsView{
		Title:        "Secrets: " + repoFullName,
		RepoFullName: repoFullName,
		OwnerSegment: strings.ToLower(owner),
		RepoSegment:  strings.ToLower(repo),
		RawText:      raw,
	}

	parsed, parseErr := parseSecretsRaw(raw)
	if parseErr != nil {
		view.ValidationError = parseErr.Error()
		h.Logger.Info("admin_ui_repo_secrets_post_validation",
			"repo", repoFullName, "err", parseErr.Error())
		h.renderHTML(w, http.StatusOK, "repo_secrets.gohtml", view)
		return
	}

	if h.RepoSecrets == nil {
		view.Error = "repo secret store not configured"
		h.renderHTML(w, http.StatusInternalServerError, "repo_secrets.gohtml", view)
		return
	}

	if err := h.applySecretDiff(r.Context(), repoFullName, parsed); err != nil {
		h.Logger.Error("admin_ui_repo_secrets_post_apply_failed",
			"repo", repoFullName, "err", err.Error())
		view.Error = "failed to save secrets"
		h.renderHTML(w, http.StatusInternalServerError, "repo_secrets.gohtml", view)
		return
	}

	http.Redirect(w, r, "/admin/repos/"+repoFullName+"/secrets?msg=saved", http.StatusSeeOther)
}

// applySecretDiff 는 parsed 와 DB 의 현재 상태를 비교해 added/updated/removed 를 적용한다.
// counts 는 slog audit 용 (결정 8 / NF-Slog-1).
func (h *AdminUIHandler) applySecretDiff(ctx context.Context, repoFullName string, parsed map[string]string) error {
	current, err := h.RepoSecrets.List(ctx, repoFullName)
	if err != nil {
		return fmt.Errorf("list current: %w", err)
	}
	currentMap := make(map[string]string, len(current))
	for _, c := range current {
		currentMap[c.Key] = c.Value
	}
	added, updated, removed := 0, 0, 0
	now := h.now()

	// Sort keys so that Upsert order is deterministic — easier diagnostics in test logs.
	parsedKeys := make([]string, 0, len(parsed))
	for k := range parsed {
		parsedKeys = append(parsedKeys, k)
	}
	sort.Strings(parsedKeys)

	for _, k := range parsedKeys {
		v := parsed[k]
		if cur, ok := currentMap[k]; ok {
			if cur == v {
				continue
			}
			updated++
		} else {
			added++
		}
		if uerr := h.RepoSecrets.Upsert(ctx, store.RepoSecret{
			RepoFullName: repoFullName,
			Key:          k,
			Value:        v,
			UpdatedAt:    now,
		}); uerr != nil {
			return fmt.Errorf("upsert key=%s: %w", k, uerr)
		}
	}
	// 삭제 — current 에 있고 parsed 에 없는 키.
	for k := range currentMap {
		if _, ok := parsed[k]; ok {
			continue
		}
		removed++
		if derr := h.RepoSecrets.Delete(ctx, repoFullName, k); derr != nil {
			return fmt.Errorf("delete key=%s: %w", k, derr)
		}
	}

	h.Logger.Info("admin_ui_repo_secrets_save",
		"repo", repoFullName,
		"n_keys", len(parsed),
		"added", added,
		"updated", updated,
		"removed", removed,
	)
	return nil
}

// errReposNotConfigured 는 cmd/hub wiring 누락 시 사용 (테스트/방어용).
var errReposNotConfigured = errors.New("admin_ui_repo_secrets: RepoSecrets store not configured")
