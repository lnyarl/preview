// 이 파일의 책임:
//   - Phase 10 Admin UI repo secrets 핸들러 단위 테스트.
//   - reposIndex: previews + repo_secrets union, secret 카운트 표시.
//   - repoSecretsGet: textarea 안에 KEY=VALUE 렌더, 안내 문구 노출, 빈 repo 도 200.
//   - repoSecretsPost: 정상 저장 → 303, validation 실패 → 200 + 에러 + DB 미변경.
//   - 결정 3a/3b/3c 의 라인 파싱 (등호 split, KEY trim, VALUE 보존, 코멘트 처리).
//   - 결정 15 lowercase 정규화 (path "Foo/Bar" → store key "foo/bar").
package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/store"
)

// uiFakeRepoSecretStore — 핸들러 단위 테스트용 in-memory 구현.
// store 인터페이스와 동일하게 입력 repoFullName 을 lowercase 정규화한다 (실제 sqlite 구현체와 동등).
type uiFakeRepoSecretStore struct {
	mu      sync.Mutex
	rows    map[string]map[string]string // repo → key → value
	listErr error
}

func newUIFakeRepoSecretStore() *uiFakeRepoSecretStore {
	return &uiFakeRepoSecretStore{rows: map[string]map[string]string{}}
}

func (f *uiFakeRepoSecretStore) List(_ context.Context, repo string) ([]store.RepoSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	repo = strings.ToLower(repo)
	kv := f.rows[repo]
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	// key ASC 정렬.
	sortStrings(keys)
	out := make([]store.RepoSecret, 0, len(keys))
	for _, k := range keys {
		out = append(out, store.RepoSecret{
			RepoFullName: repo, Key: k, Value: kv[k], UpdatedAt: time.Now().UTC(),
		})
	}
	return out, nil
}

func (f *uiFakeRepoSecretStore) Upsert(_ context.Context, s store.RepoSecret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := strings.ToLower(s.RepoFullName)
	if f.rows[repo] == nil {
		f.rows[repo] = map[string]string{}
	}
	f.rows[repo][s.Key] = s.Value
	return nil
}

func (f *uiFakeRepoSecretStore) Delete(_ context.Context, repo, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo = strings.ToLower(repo)
	if kv := f.rows[repo]; kv != nil {
		delete(kv, key)
	}
	return nil
}

func (f *uiFakeRepoSecretStore) DeleteAllFor(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, strings.ToLower(repo))
	return nil
}

func (f *uiFakeRepoSecretStore) ListRepos(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.rows))
	for k := range f.rows {
		out = append(out, k)
	}
	sortStrings(out)
	return out, nil
}

// sortStrings 는 표준 라이브러리 sort.Strings 의 alias — 본 테스트 파일이
// "sort" 를 import 하지 않아도 되도록 helper 로 빼냈다. 동일 동작.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func newRepoSecretsTestUI(t *testing.T, rs store.RepoSecretStore) *AdminUIHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(newAdminUIAgentStore(), newAdminUIPreviewStore(), tg, logger)
	if rs != nil {
		ui.SetRepoSecrets(rs)
	}
	return ui
}

// pathReq 는 net/http mux PathValue 를 시뮬레이션하기 위해 mux 를 통해 요청을 라우팅한다.
func pathReq(t *testing.T, ui *AdminUIHandler, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ui.Register(mux)
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------- /admin/repos 인덱스 ----------

func TestReposIndexShowsUnion(t *testing.T) {
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)

	// previews 측: acme/web (preview 1건).
	now := time.Now().UTC()
	ps := ui.PreviewStore.(*adminUIFakePreviewStore)
	ps.put(store.Preview{
		ID: "p1", RepoFullName: "acme/web", PrNumber: 1, Status: "running",
		Branch: "m", CreatedAt: now, UpdatedAt: now,
	})
	// secrets 측: org/preregistered (secret 만 있고 preview 없음).
	_ = rs.Upsert(context.Background(), store.RepoSecret{RepoFullName: "org/preregistered", Key: "X", Value: "x", UpdatedAt: now})

	rr := pathReq(t, ui, "GET", "/admin/repos", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "acme/web") {
		t.Errorf("body missing acme/web:\n%s", body)
	}
	if !strings.Contains(body, "org/preregistered") {
		t.Errorf("body missing org/preregistered:\n%s", body)
	}
	if !strings.Contains(body, "1 keys") {
		t.Errorf("body missing secret count for org/preregistered (1 keys):\n%s", body)
	}
}

func TestLayoutHasReposLink(t *testing.T) {
	ui := newRepoSecretsTestUI(t, newUIFakeRepoSecretStore())
	rr := pathReq(t, ui, "GET", "/admin/repos", "")
	body := rr.Body.String()
	if !strings.Contains(body, `href="/admin/repos"`) {
		t.Errorf("layout missing Repos nav link:\n%s", body)
	}
}

// ---------- /admin/repos/{owner}/{repo}/secrets GET ----------

func TestRepoSecretsGetEmptyOK(t *testing.T) {
	// F-13: DB 비어있어도 200 + 빈 textarea (사전 등록 동선).
	ui := newRepoSecretsTestUI(t, newUIFakeRepoSecretStore())
	rr := pathReq(t, ui, "GET", "/admin/repos/foo/bar/secrets", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Secrets: foo/bar") {
		t.Errorf("title missing:\n%s", body)
	}
}

func TestRepoSecretsGetRendersSavedRows(t *testing.T) {
	rs := newUIFakeRepoSecretStore()
	now := time.Now().UTC()
	_ = rs.Upsert(context.Background(), store.RepoSecret{RepoFullName: "foo/bar", Key: "API_KEY", Value: "k1", UpdatedAt: now})
	_ = rs.Upsert(context.Background(), store.RepoSecret{RepoFullName: "foo/bar", Key: "DATABASE_URL", Value: "postgres://x", UpdatedAt: now})

	ui := newRepoSecretsTestUI(t, rs)
	rr := pathReq(t, ui, "GET", "/admin/repos/foo/bar/secrets", "")
	body := rr.Body.String()
	if !strings.Contains(body, "API_KEY=k1") {
		t.Errorf("body missing API_KEY=k1:\n%s", body)
	}
	if !strings.Contains(body, "DATABASE_URL=postgres://x") {
		t.Errorf("body missing DATABASE_URL=postgres://x:\n%s", body)
	}
}

func TestRepoSecretsGetHasNotices(t *testing.T) {
	// F-12b/c/d/e: plaintext 경고, Dockerfile caveat, VALUE 공백 보존, lowercase.
	ui := newRepoSecretsTestUI(t, newUIFakeRepoSecretStore())
	rr := pathReq(t, ui, "GET", "/admin/repos/foo/bar/secrets", "")
	body := rr.Body.String()
	for _, want := range []string{
		"Plaintext storage warning",
		"Dockerfile build mode caveat",
		"preserved",       // VALUE 공백 보존
		"lowercase",       // F-12e
	} {
		if !strings.Contains(body, want) {
			t.Errorf("notice %q missing:\n%s", want, body)
		}
	}
}

func TestRepoSecretsGetSavedFlash(t *testing.T) {
	ui := newRepoSecretsTestUI(t, newUIFakeRepoSecretStore())
	rr := pathReq(t, ui, "GET", "/admin/repos/foo/bar/secrets?msg=saved", "")
	body := rr.Body.String()
	if !strings.Contains(body, "Saved.") {
		t.Errorf("flash 'Saved.' missing:\n%s", body)
	}
}

// ---------- POST: validation ----------

func TestRepoSecretsPostValidationKeyRegex(t *testing.T) {
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "1BAD=v\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (validation re-render)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "does not match") {
		t.Errorf("expected validation error message:\n%s", rr.Body.String())
	}
	// DB 미변경 검증.
	rows, _ := rs.List(context.Background(), "foo/bar")
	if len(rows) != 0 {
		t.Errorf("DB should be unchanged on validation failure; got=%+v", rows)
	}
}

func TestRepoSecretsPostValidationMissingEquals(t *testing.T) {
	// F-17b: '=' 없는 라인.
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "JUST_KEY\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	// html/template escapes single quotes → "missing &#39;=&#39; separator". 둘 중 하나만 있으면 OK.
	if !strings.Contains(body, "missing '=' separator") && !strings.Contains(body, "missing &#39;=&#39; separator") {
		t.Errorf("expected missing '=' error:\n%s", body)
	}
}

func TestRepoSecretsPostValidationEmptyKey(t *testing.T) {
	// F-17c: =foo (KEY 빈 문자열).
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "=foo\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "empty KEY") {
		t.Errorf("expected empty KEY error:\n%s", rr.Body.String())
	}
}

// ---------- POST: parsing rules ----------

func TestParseSecretsRawAllRules(t *testing.T) {
	// 결정 3a/3b/3c 의 모든 케이스를 한 번에.
	raw := strings.Join([]string{
		"# leading comment",
		"  ",                                // 빈/공백 라인
		"KEY=foo",                           // 정상
		"KEY2=a=b=c",                        // F-17d: 첫 = 만 split
		"JWT=eyJhbGc...==",                  // F-17e: base64 padding 보존
		"COLOR=#abcdef",                     // F-17f: inline # 보존
		"  KEY3  =value",                    // F-17h: KEY trim
		"KEY4= value ",                      // F-17i: VALUE 공백 보존
		"KEY5=",                             // F-17j: 빈 VALUE 합법
		`KEY6=foo\nbar`,                     // F-17k: \n literal 보존
	}, "\n")
	out, err := parseSecretsRaw(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	want := map[string]string{
		"KEY":   "foo",
		"KEY2":  "a=b=c",
		"JWT":   "eyJhbGc...==",
		"COLOR": "#abcdef",
		"KEY3":  "value",
		"KEY4":  " value ",
		"KEY5":  "",
		"KEY6":  `foo\nbar`,
	}
	if len(out) != len(want) {
		t.Fatalf("len=%d want %d: %+v", len(out), len(want), out)
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("key=%s want=%q got=%q", k, v, out[k])
		}
	}
}

// ---------- POST: 정상 저장 ----------

func TestRepoSecretsPostSaveAndDiff(t *testing.T) {
	rs := newUIFakeRepoSecretStore()
	ctx := context.Background()
	now := time.Now().UTC()
	// 사전 시드: 기존 row.
	_ = rs.Upsert(ctx, store.RepoSecret{RepoFullName: "foo/bar", Key: "OLD", Value: "old", UpdatedAt: now})
	_ = rs.Upsert(ctx, store.RepoSecret{RepoFullName: "foo/bar", Key: "KEEP", Value: "v1", UpdatedAt: now})

	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	// KEEP 은 같은 값으로 (no-op), CHANGE 는 새 값으로 (updated... 음 아니지: KEEP 그대로 + KEEP 새로) 도 추가.
	form.Set("raw", "KEEP=v1\nCHANGE_ME=new\nNEW=fresh\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303; body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/admin/repos/foo/bar/secrets") || !strings.Contains(loc, "msg=saved") {
		t.Errorf("Location=%q expected msg=saved redirect", loc)
	}
	// DB 검증: KEEP 보존, OLD 삭제, CHANGE_ME/NEW 추가.
	rows, _ := rs.List(ctx, "foo/bar")
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
	}
	want := map[string]string{
		"KEEP":       "v1",
		"CHANGE_ME":  "new",
		"NEW":        "fresh",
	}
	if len(got) != len(want) {
		t.Fatalf("rows=%v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key=%s want=%q got=%q", k, v, got[k])
		}
	}
	if _, ok := got["OLD"]; ok {
		t.Errorf("OLD should be removed")
	}
}

func TestRepoSecretsPostEmptyDeletesAll(t *testing.T) {
	// F-18: 빈 raw 제출 → 같은 repo 모든 row 삭제.
	rs := newUIFakeRepoSecretStore()
	ctx := context.Background()
	now := time.Now().UTC()
	_ = rs.Upsert(ctx, store.RepoSecret{RepoFullName: "foo/bar", Key: "A", Value: "a", UpdatedAt: now})
	_ = rs.Upsert(ctx, store.RepoSecret{RepoFullName: "foo/bar", Key: "B", Value: "b", UpdatedAt: now})

	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	rows, _ := rs.List(ctx, "foo/bar")
	if len(rows) != 0 {
		t.Errorf("rows should be empty after blank submit: %+v", rows)
	}
}

func TestRepoSecretsPostLowercaseNormalization(t *testing.T) {
	// F-18c: path "Foo/Bar" → DB "foo/bar".
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "API_KEY=k1\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/Foo/Bar/secrets", form.Encode())
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/admin/repos/foo/bar/secrets") {
		t.Errorf("Location=%q expected lowercase repo segments", loc)
	}
	rows, _ := rs.List(context.Background(), "foo/bar")
	if len(rows) != 1 || rows[0].Key != "API_KEY" {
		t.Fatalf("row not stored under lowercase: %+v", rows)
	}
}

func TestRepoSecretsPostCommentLines(t *testing.T) {
	// F-18b: # comment 라인은 무시.
	rs := newUIFakeRepoSecretStore()
	ui := newRepoSecretsTestUI(t, rs)
	form := url.Values{}
	form.Set("raw", "# 첫 줄 코멘트\nA=a\n# 두 번째 코멘트\nB=b\n")
	rr := pathReq(t, ui, "POST", "/admin/repos/foo/bar/secrets", form.Encode())
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rr.Code)
	}
	rows, _ := rs.List(context.Background(), "foo/bar")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (comments excluded): %+v", rows)
	}
}
