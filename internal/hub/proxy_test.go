// proxy_test.go: MatchHost 테이블, Director 호스트 재작성, 캐시 무효화.
package hub

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

func TestProxyMatchHost(t *testing.T) {
	cases := []struct {
		host     string
		wantPR   int
		wantBase string
		wantOK   bool
	}{
		{"pr-1.preview.localhost:3000", 1, "localhost", true},
		{"pr-42.preview.localhost", 42, "localhost", true},
		{"pr-7.preview.example.com:8443", 7, "example.com", true},
		{"pr-7.preview.dev.example.com", 7, "dev.example.com", true},
		{"preview.localhost:3000", 0, "", false},
		{"pr-abc.preview.localhost", 0, "", false},
		// port non-numeric — regex (?::\d+)? 가 ":abc" 와 매칭 안됨, 전체 실패.
		{"pr-1.preview.localhost:abc", 0, "", false},
		{"pr-1.PREVIEW.localhost", 0, "", false},
		{"prefix-pr-1.preview.localhost", 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			pr, base, ok := MatchHost(tc.host)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if ok {
				if pr != tc.wantPR {
					t.Errorf("prNumber=%d want %d", pr, tc.wantPR)
				}
				if base != tc.wantBase {
					t.Errorf("base=%q want %q", base, tc.wantBase)
				}
			}
		})
	}
}

func TestProxyDirectorHostRewrite(t *testing.T) {
	// backend echoes r.Host
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	host := backendURL.Host
	port, _ := strconv.Atoi(backendURL.Port())
	hostOnly := backendURL.Hostname()

	preview := &store.Preview{
		ID:        "test-id",
		Status:    "running",
		PrNumber:  1,
		AgentHost: &hostOnly,
		AgentPort: &port,
	}

	fakeStore := &fakeProxyStore{preview: preview}
	pm := NewProxyMiddleware(fakeStore, "localhost", slog.Default())

	req := httptest.NewRequest("GET", "http://pr-1.preview.localhost:3000/", nil)
	req.Host = "pr-1.preview.localhost:3000"
	w := httptest.NewRecorder()
	pm.Wrap(http.NotFoundHandler()).ServeHTTP(w, req)

	body := w.Body.String()
	if body != host {
		t.Errorf("want body=%q (target host), got %q", host, body)
	}
}

func TestProxyCacheEvictOnStatusChange(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	port, _ := strconv.Atoi(backendURL.Port())
	host := backendURL.Hostname()

	p := &store.Preview{
		ID:        "evict-id",
		Status:    "running",
		PrNumber:  99,
		AgentHost: &host,
		AgentPort: &port,
	}
	fs := &fakeProxyStore{preview: p}
	pm := NewProxyMiddleware(fs, "localhost", slog.Default())
	h := pm.Wrap(http.NotFoundHandler())

	// 1차 요청 → cache miss → 조회 1회.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Host = "pr-99.preview.localhost"
	h.ServeHTTP(httptest.NewRecorder(), req1)
	if fs.callCount != 1 {
		t.Fatalf("want 1 FindByHost call, got %d", fs.callCount)
	}

	// 2차 요청 → cache hit → 조회 0 추가.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Host = "pr-99.preview.localhost"
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if fs.callCount != 1 {
		t.Fatalf("want still 1 FindByHost call after cache hit, got %d", fs.callCount)
	}

	// Invalidate → 캐시 비움.
	pm.Invalidate("evict-id")

	// 3차 요청 → teardown 상태 → 503.
	p.Status = "teardown"
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.Host = "pr-99.preview.localhost"
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 after evict+teardown, got %d", w3.Code)
	}
}

// fakeProxyStore 는 테스트용 최소 구현. FindByHost 만 의미있게 동작.
type fakeProxyStore struct {
	preview   *store.Preview
	callCount int
}

func (f *fakeProxyStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	f.callCount++
	if f.preview == nil {
		return nil, store.ErrNotFound
	}
	return f.preview, nil
}

// 나머지 메서드는 panic 으로 미구현 표시 (본 테스트에서 호출되지 않음).
func (f *fakeProxyStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) GetByID(_ context.Context, _ string) (*store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) UpdateStatus(_ context.Context, _, _, _, _ string, _ time.Time, _ store.PreviewFields) error {
	panic("not impl")
}
func (f *fakeProxyStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) ListAll(_ context.Context) ([]store.Preview, error) {
	panic("not impl")
}
func (f *fakeProxyStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}
