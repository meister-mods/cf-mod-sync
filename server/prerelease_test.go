package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecognizedPrereleaseMarkers(t *testing.T) {
	for _, version := range []string{"1.0-alpha.1", "1.0Beta2", "1.0-snapshot", "1.0-RC1", "1.0-preview", "1.0-prerelease.1", "1.0-pre-release.2", "1.0-nightly", "1.0-dev", "1.0-eap.2"} {
		if reason := prereleaseReason("release", "example.jar", &JarMetadata{Mods: []ModIdentity{{ID: "example", Version: version}}}); reason == "" {
			t.Errorf("missed prerelease version %s", version)
		}
	}
	for _, version := range []string{"1.2.43-RELEASE", "1.0-final", "1.1.8-hotfix2", "1.0"} {
		if reason := prereleaseReason("release", "example-"+version+".jar", &JarMetadata{Mods: []ModIdentity{{ID: "example", Version: version}}}); reason != "" {
			t.Errorf("stable version %s rejected: %s", version, reason)
		}
	}
}

func TestStrictPrereleaseExclusionAcrossPinsAndCache(t *testing.T) {
	versions := map[int64]string{1: "1.0", 2: "5.0-snapshot", 3: "6.0", 4: "4.0", 5: "3.0"}
	payloads := map[int64][]byte{}
	files := map[int64]File{}
	for id, version := range versions {
		raw := modJar(t, "example", version, "[47,48)", "")
		payloads[id] = raw
		digest := sha1.Sum(raw)
		name := fmt.Sprintf("example-%d.jar", id)
		if id == 4 {
			name = "example-4.0-rc1.jar"
		}
		if id == 5 {
			name = "example-3.0-prerelease.jar"
		}
		release := 1
		if id == 3 {
			release = 2
		}
		files[id] = File{ID: id, ModID: 10, FileName: name, FileDate: fmt.Sprintf("2026-10-%02dT00:00:00Z", id), FileLength: int64(len(raw)),
			Hashes: []Hash{{Algo: 1, Value: hex.EncodeToString(digest[:])}}, ReleaseType: release, IsAvailable: true, GameVersions: []string{"1.20.1", "Forge"}}
	}
	mod := Mod{ID: 10, GameID: 432, ClassID: 6, Slug: "example", Name: "Example", IsAvailable: true, AllowModDistribution: true}
	var upstream *httptest.Server
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int64
		switch {
		case strings.HasPrefix(r.URL.Path, "/blob/"):
			fmt.Sscanf(r.URL.Path, "/blob/%d", &id)
			w.Write(payloads[id])
			return
		case r.URL.Path == "/v1/mods/search":
			json.NewEncoder(w).Encode(map[string]any{"data": []Mod{mod}})
		case strings.HasSuffix(r.URL.Path, "/download-url"):
			fmt.Sscanf(r.URL.Path, "/v1/mods/10/files/%d/download-url", &id)
			json.NewEncoder(w).Encode(map[string]any{"data": fmt.Sprintf("%s/blob/%d", upstream.URL, id)})
		case r.URL.Path == "/v1/mods/10/files":
			var all []File
			for _, file := range files {
				all = append(all, file)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": all})
		default:
			fmt.Sscanf(r.URL.Path, "/v1/mods/10/files/%d", &id)
			json.NewEncoder(w).Encode(map[string]any{"data": files[id]})
		}
	}))
	defer upstream.Close()
	service, err := newModServer(context.Background(), ServerOptions{CacheDir: t.TempDir(), Token: testServerToken, Workers: 3, MaxBytes: 1 << 20, MetadataTTL: time.Hour, JobTTL: time.Hour},
		&Client{APIBase: upstream.URL + "/v1", HTTP: upstream.Client(), Key: "test"}, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer service.cancel()
	run := func(strict bool, pin int64) JobSnapshot {
		t.Helper()
		url := projectURL("example")
		if pin > 0 {
			url += fmt.Sprintf("/files/%d", pin)
		}
		body, _ := json.Marshal(JobRequest{ProjectURLs: []string{url}, MinecraftVersion: "1.20.1", ForgeVersion: "47.4.28", ReleaseChannel: "all", ExcludePrereleases: strict})
		response := requestAPI(service, "POST", "/api/v1/jobs", body)
		if response.Code != http.StatusAccepted {
			t.Fatalf("job create: %d %s", response.Code, response.Body)
		}
		var job JobSnapshot
		json.Unmarshal(response.Body.Bytes(), &job)
		deadline := time.Now().Add(5 * time.Second)
		for !terminalJob(job.State) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			response = requestAPI(service, "GET", "/api/v1/jobs/"+job.ID, nil)
			if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
		}
		return job
	}
	// Prime the unrestricted plan/cache with a beta; strict requests must not reuse it.
	if job := run(false, 0); job.State != "completed" || job.Files[0].FileID != 3 {
		t.Fatalf("unrestricted selection: %+v", job)
	}
	for attempt := 0; attempt < 2; attempt++ {
		job := run(true, 0)
		if job.State != "completed" || !job.ExcludePrereleases || len(job.Files) != 1 || job.Files[0].FileID != 1 {
			t.Fatalf("strict selection: %+v", job)
		}
		if attempt == 1 && !job.SelectionCached {
			t.Fatal("strict selection cache was not reused")
		}
	}
	for _, pin := range []int64{2, 3, 4, 5} {
		job := run(true, pin)
		if job.State != "failed" || len(job.Errors) == 0 {
			t.Fatalf("prerelease pin %d bypassed exclusion: %+v", pin, job)
		}
		for _, file := range job.Files {
			if file.DownloadPath != "" {
				t.Fatal("blocked batch exposed a download path")
			}
		}
	}
	if job := run(true, 1); job.State != "completed" {
		t.Fatalf("stable pin was blocked: %+v", job)
	}
	if job := run(false, 2); job.State != "completed" {
		t.Fatalf("disabling strict exclusion did not restore pinned prereleases: %+v", job)
	}
}
