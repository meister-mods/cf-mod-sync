package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testServerToken = "client-token-with-at-least-32-characters"

type cacheFixture struct {
	upstream *httptest.Server
	jar      []byte
	apiCalls atomic.Int64
	blobs    atomic.Int64
	deny     atomic.Bool
	fail     atomic.Bool
}

func newCacheFixture(t *testing.T, release <-chan struct{}) *cacheFixture {
	t.Helper()
	fixture := &cacheFixture{jar: jarData(t)}
	digest := sha1.Sum(fixture.jar)
	fixture.upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/blob/") {
			fixture.blobs.Add(1)
			if release != nil {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.Write(fixture.jar)
			return
		}
		fixture.apiCalls.Add(1)
		if r.Header.Get("x-api-key") != "upstream-test-secret" {
			t.Error("missing upstream authentication")
		}
		if fixture.fail.Load() {
			http.Error(w, "upstream-test-secret https://edge.forgecdn.net/private.jar?token=private-url-token", http.StatusBadRequest)
			return
		}
		mod := Mod{ID: 10, GameID: 432, ClassID: 6, Slug: "create", Name: "Create", IsAvailable: true, AllowModDistribution: !fixture.deny.Load()}
		switch {
		case r.URL.Path == "/v1/mods/search":
			json.NewEncoder(w).Encode(map[string]any{"data": []Mod{mod}})
		case r.URL.Path == "/v1/mods/20":
			mod.ID, mod.Slug, mod.Name = 20, "common-lib", "Common Library"
			json.NewEncoder(w).Encode(map[string]any{"data": mod})
		case strings.HasSuffix(r.URL.Path, "/download-url"):
			json.NewEncoder(w).Encode(map[string]any{"data": fixture.upstream.URL + "/blob/jar"})
		case strings.HasSuffix(r.URL.Path, "/files"):
			id := int64(10)
			if strings.Contains(r.URL.Path, "/20/") {
				id = 20
			}
			file := File{
				ID: id * 100, ModID: id, FileName: fmt.Sprintf("demo-%d.jar", id),
				FileDate: "2026-10-21T00:00:00Z", FileLength: int64(len(fixture.jar)),
				GameVersions: []string{"1.20.1", "Forge"}, ReleaseType: 1, IsAvailable: true,
				Hashes: []Hash{{Algo: 1, Value: hex.EncodeToString(digest[:])}},
			}
			if id == 10 {
				file.Dependencies = []Dependency{{ModID: 20, RelationType: 3}}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []File{file}, "pagination": Pagination{TotalCount: 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.upstream.Close)
	return fixture
}

func (f *cacheFixture) service(t *testing.T, cacheDir string) *modServer {
	t.Helper()
	service, err := newModServer(context.Background(), ServerOptions{
		CacheDir: cacheDir, Token: testServerToken, Workers: 4, MaxBytes: 1 << 20,
		MetadataTTL: time.Hour, JobTTL: time.Hour,
	}, &Client{APIBase: f.upstream.URL + "/v1", HTTP: f.upstream.Client(), Key: "upstream-test-secret"}, f.upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.cancel)
	return service
}

func requestAPI(service *modServer, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testServerToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	service.handler().ServeHTTP(recorder, request)
	return recorder
}

func startTestJob(t *testing.T, service *modServer, refresh bool) JobSnapshot {
	t.Helper()
	body, _ := json.Marshal(JobRequest{
		ProjectURLs: []string{projectURL("create")}, MinecraftVersion: "1.20.1", ReleaseChannel: "all", Refresh: refresh,
	})
	response := requestAPI(service, "POST", "/api/v1/jobs", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create job: %d %s", response.Code, response.Body)
	}
	var job JobSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response = requestAPI(service, "GET", "/api/v1/jobs/"+job.ID, nil)
		if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if terminalJob(job.State) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return job
}

func TestServerCacheLifecycleAndRestart(t *testing.T) {
	fixture := newCacheFixture(t, nil)
	dir := t.TempDir()
	service := fixture.service(t, dir)
	first := startTestJob(t, service, false)
	if first.State != "completed" || first.Total != 2 || first.Completed != 2 || fixture.blobs.Load() != 2 {
		t.Fatalf("unexpected first job: %+v; downloads=%d", first, fixture.blobs.Load())
	}
	for _, file := range first.Files {
		if file.Verified == nil || file.DownloadPath == "" {
			t.Fatalf("file is not ready: %+v", file)
		}
		response := requestAPI(service, "GET", file.DownloadPath, nil)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fixture.jar) ||
			response.Header().Get("X-Checksum-SHA256") != file.Verified.SHA256 {
			t.Fatalf("cache delivery failed: %d", response.Code)
		}
		request := httptest.NewRequest("GET", file.DownloadPath, nil)
		request.Header.Set("Authorization", "Bearer "+testServerToken)
		request.Header.Set("Range", "bytes=0-9")
		recorder := httptest.NewRecorder()
		service.handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusPartialContent || recorder.Body.Len() != 10 {
			t.Fatal("range request failed")
		}
	}
	apiCount := fixture.apiCalls.Load()
	second := startTestJob(t, service, false)
	if second.CacheHits != 2 || !second.SelectionCached || fixture.apiCalls.Load() != apiCount || fixture.blobs.Load() != 2 {
		t.Fatalf("warm cache used upstream: %+v", second)
	}
	restarted := fixture.service(t, dir)
	third := startTestJob(t, restarted, false)
	if third.CacheHits != 2 || !third.SelectionCached || fixture.apiCalls.Load() != apiCount {
		t.Fatal("cache did not survive restart")
	}
	_ = startTestJob(t, restarted, true)
	if fixture.apiCalls.Load() <= apiCount || fixture.blobs.Load() != 2 {
		t.Fatal("refresh should renew metadata and reuse validated bytes")
	}
	file := first.Files[0]
	_, path, err := service.cachedEntry(file.ModID, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if response := requestAPI(service, "GET", file.DownloadPath, nil); response.Code != http.StatusNotFound {
		t.Fatal("served corrupt cache bytes")
	}
	repaired := startTestJob(t, service, false)
	if repaired.State != "completed" || fixture.blobs.Load() != 3 {
		t.Fatal("corrupt cache was not repaired")
	}
}

func TestWarmCacheBypassesColdScanAndDownloadSlots(t *testing.T) {
	fixture := newCacheFixture(t, nil)
	service := fixture.service(t, t.TempDir())
	if job := startTestJob(t, service, false); job.State != "completed" {
		t.Fatalf("could not prime cache: %+v", job)
	}
	// Simulate an unrelated cold plan and all network workers being occupied.
	service.planSlot <- struct{}{}
	for i := 0; i < cap(service.slots); i++ {
		service.slots <- struct{}{}
	}
	defer func() {
		<-service.planSlot
		for len(service.slots) > 0 {
			<-service.slots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	items, issues, _, cached := service.resolveCached(ctx, []string{"create"}, Options{Version: "1.20.1", Channel: "all", Deps: true, PinnedFiles: map[string]int64{}}, false)
	if !cached || len(issues) != 0 || len(items) != 2 {
		t.Fatalf("warm plan waited for cold work: cached=%v items=%d issues=%+v", cached, len(items), issues)
	}
	for _, item := range items {
		entry, err := service.ensureFile(ctx, item)
		if err != nil || entry.Verified == nil || entry.Status != "cached" {
			t.Fatalf("warm file waited for a network worker: %+v %v", entry, err)
		}
	}
}

func TestBaselineReviewGatesBatchDownloadPaths(t *testing.T) {
	fixture := newCacheFixture(t, nil)
	service := fixture.service(t, t.TempDir())
	for _, allow := range []bool{false, true} {
		body, _ := json.Marshal(JobRequest{
			ProjectURLs: []string{projectURL("create")}, MinecraftVersion: "1.20.1", ForgeVersion: "47.4.28",
			ReleaseChannel: "release", AllowReviewedChanges: allow,
			InstalledFiles: []InstalledFile{{Filename: "missing-original.jar", SHA256: strings.Repeat("0", 64),
				ModsTOML: "modLoader=\"javafml\"\nloaderVersion=\"[47,)\"\n[[mods]]\nmodId=\"missing_original\"\nversion=\"1.0\"\n"}},
		})
		response := requestAPI(service, "POST", "/api/v1/jobs", body)
		if response.Code != http.StatusAccepted {
			t.Fatalf("create: %d %s", response.Code, response.Body)
		}
		var job JobSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !terminalJob(job.State) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			response = requestAPI(service, "GET", "/api/v1/jobs/"+job.ID, nil)
			if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
		}
		if job.Compatibility == nil || !job.Compatibility.RequiresReview || len(job.Compatibility.Changes) == 0 {
			t.Fatalf("missing baseline mod not reported: %+v", job)
		}
		expected := "failed"
		if allow {
			expected = "completed"
		}
		if job.State != expected {
			t.Fatalf("allow=%v state=%s errors=%+v", allow, job.State, job.Errors)
		}
		for _, file := range job.Files {
			if (file.DownloadPath != "") != allow {
				t.Fatalf("allow=%v download path=%q", allow, file.DownloadPath)
			}
		}
	}
}

func TestConcurrentCacheFillAndIndependentCancellation(t *testing.T) {
	release := make(chan struct{})
	fixture := newCacheFixture(t, release)
	service := fixture.service(t, t.TempDir())
	selections, issues := resolveMods(context.Background(), service.client, []string{"create"}, Options{Version: "1.20.1", Channel: "all"})
	if len(issues) != 0 || len(selections) != 1 {
		t.Fatal(issues)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { _, err := service.ensureFile(ctx, selections[0]); cancelled <- err }()
	for deadline := time.Now().Add(5 * time.Second); fixture.blobs.Load() == 0; {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("download did not start")
		}
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := service.ensureFile(context.Background(), selections[0])
			if err != nil || entry.Verified == nil {
				t.Errorf("shared download failed: %v", err)
			}
		}()
	}
	cancel()
	if err := <-cancelled; err != context.Canceled {
		t.Fatalf("cancelled waiter returned %v", err)
	}
	close(release)
	wg.Wait()
	if fixture.blobs.Load() != 1 {
		t.Fatalf("expected one shared upstream download, got %d", fixture.blobs.Load())
	}
}

func TestBatchWaitsForCompatibilityValidation(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	fixture := newCacheFixture(t, release)
	service := fixture.service(t, t.TempDir())
	items, issues := resolveMods(context.Background(), service.client, []string{"create"}, Options{Version: "1.20.1", Channel: "all"})
	if len(issues) != 0 || len(items) != 1 {
		t.Fatal(issues)
	}
	dir := service.fileDir(items[0].mod.ID, items[0].file.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, items[0].file.FileName), fixture.jar, 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(JobRequest{ProjectURLs: []string{projectURL("create")}})
	response := requestAPI(service, "POST", "/api/v1/jobs", body)
	var job JobSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fixture.blobs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.blobs.Load() == 0 {
		t.Fatal("dependency fill did not start")
	}
	response = requestAPI(service, "GET", "/api/v1/jobs/"+job.ID, nil)
	json.Unmarshal(response.Body.Bytes(), &job)
	for _, file := range job.Files {
		if file.DownloadPath != "" {
			t.Fatal("file was released before batch compatibility validation")
		}
	}
	once.Do(func() { close(release) })
	for time.Now().Before(deadline) {
		response = requestAPI(service, "GET", "/api/v1/jobs/"+job.ID, nil)
		json.Unmarshal(response.Body.Bytes(), &job)
		if terminalJob(job.State) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if job.State != "completed" {
		t.Fatalf("unexpected job: %+v", job)
	}
	for _, file := range job.Files {
		if file.DownloadPath == "" {
			t.Fatal("validated batch file was not released")
		}
	}
}
func TestServerAuthorizationValidationAndRedaction(t *testing.T) {
	fixture := newCacheFixture(t, nil)
	service := fixture.service(t, t.TempDir())
	for _, path := range []string{"/api/v1/status", "/api/v1/jobs/unknown", "/api/v1/files/10/1000"} {
		recorder := httptest.NewRecorder()
		service.handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated route %s: %d", path, recorder.Code)
		}
	}
	origin := httptest.NewRequest("GET", "/healthz", nil)
	origin.Header.Set("Origin", "https://www.curseforge.com")
	recorder := httptest.NewRecorder()
	service.handler().ServeHTTP(recorder, origin)
	if recorder.Code != http.StatusForbidden {
		t.Fatal("allowed a website to call the local API")
	}
	for _, body := range []string{
		`{"project_urls":["https://evil.example/minecraft/mc-mods/create"]}`,
		`{"project_urls":["https://www.curseforge.com/minecraft/mc-mods/create"],"minecraft_version":"../escape"}`,
		`{"project_urls":[],"release_channel":"all"}`,
		`{"project_urls":["https://www.curseforge.com/minecraft/mc-mods/create"],"api_key":"client-must-not-send-a-key"}`,
	} {
		if response := requestAPI(service, "POST", "/api/v1/jobs", []byte(body)); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
	if fixture.apiCalls.Load() != 0 {
		t.Fatal("invalid input reached upstream")
	}
	fixture.fail.Store(true)
	job := startTestJob(t, service, false)
	data, _ := json.Marshal(job)
	if job.State != "failed" || bytes.Contains(data, []byte("upstream-test-secret")) ||
		bytes.Contains(data, []byte(testServerToken)) || bytes.Contains(data, []byte("private-url-token")) {
		t.Fatalf("invalid error redaction: %s", data)
	}
	fixture.fail.Store(false)
	fixture.deny.Store(true)
	job = startTestJob(t, service, true)
	if job.State != "failed" || fixture.blobs.Load() != 0 {
		t.Fatal("downloaded a distribution-disabled project")
	}
}

func TestSafeWindowsCacheFilenames(t *testing.T) {
	for _, name := range []string{"../escape.jar", "CON.jar", "lpt1.jar", "dir/file.jar", ".hidden.jar"} {
		if safeJarName(name) {
			t.Errorf("unsafe name accepted: %s", name)
		}
	}
}

func TestServerHealth(t *testing.T) {
	fixture := newCacheFixture(t, nil)
	service := fixture.service(t, t.TempDir())
	response := requestAPI(service, "GET", "/healthz", nil)
	body, _ := io.ReadAll(response.Result().Body)
	if response.Code != 200 || !bytes.Contains(body, []byte("ok")) || fixture.apiCalls.Load() != 0 {
		t.Fatal("health check touched upstream or failed")
	}
}

func TestDesktopClientRoundTrip(t *testing.T) {
	dll := os.Getenv("CFMODSYNC_CLIENT_CHECKS")
	if dll == "" {
		t.Skip("set CFMODSYNC_CLIENT_CHECKS to the built C# checks DLL for the desktop round trip")
	}
	fixture := newCacheFixture(t, nil)
	service := fixture.service(t, t.TempDir())
	server := httptest.NewServer(service.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "dotnet", dll, server.URL, testServerToken, t.TempDir())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("desktop client integration failed: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}
