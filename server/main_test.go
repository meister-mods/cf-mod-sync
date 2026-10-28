package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProjectURL(t *testing.T) {
	good := []string{
		"https://www.curseforge.com/minecraft/mc-mods/create",
		"https://curseforge.com/minecraft/mc-mods/just-enough-items/",
	}
	for _, s := range good {
		slug, err := parseProjectURL(s)
		if err != nil || slug == "" {
			t.Errorf("parse %q: %q %v", s, slug, err)
		}
	}
	bad := []string{
		"http://www.curseforge.com/minecraft/mc-mods/create",
		"https://evil.curseforge.com/minecraft/mc-mods/create",
		"https://www.curseforge.com/minecraft/mc-mods/create/files/all",
		"https://www.curseforge.com/minecraft/mc-mods/%2e%2e",
		"https://www.curseforge.com/minecraft/mc-mods/create?ignored=true",
		"https://x@www.curseforge.com/minecraft/mc-mods/create",
		"https://www.curseforge.com:443/minecraft/mc-mods/create",
	}
	for _, s := range bad {
		if slug, err := parseProjectURL(s); err == nil {
			t.Errorf("accepted invalid url %s as %s", s, slug)
		}
	}
}

func TestSelectLatestExactCompatibility(t *testing.T) {
	file := func(id int64, name, date string, vs []string, release int) File {
		return File{ID: id, ModID: 77, FileName: name, FileDate: date, GameVersions: vs, ReleaseType: release, IsAvailable: true}
	}
	files := []File{
		file(1, "older.jar", "2026-01-01T00:00:00Z", []string{"1.20.1", "Forge"}, 1),
		file(2, "new-neoforge.jar", "2026-12-01T00:00:00Z", []string{"1.20.1", "NeoForge"}, 1),
		file(3, "new-fabric.jar", "2026-12-02T00:00:00Z", []string{"1.20.1", "Fabric"}, 1),
		file(4, "wrong-mc.jar", "2026-12-03T00:00:00Z", []string{"1.21.1", "Forge"}, 1),
		file(5, "new-beta.jar", "2026-10-01T00:00:00Z", []string{"1.20.1", "Forge"}, 2),
		file(6, "new-alpha.jar", "2026-10-02T00:00:00Z", []string{"1.20.1", "Forge"}, 3),
		file(7, "source.zip", "2026-12-04T00:00:00Z", []string{"1.20.1", "Forge"}, 1),
	}
	for _, tc := range []struct {
		channel string
		want    int64
	}{{"release", 1}, {"beta", 5}, {"all", 6}} {
		f, err := selectLatest(files, "1.20.1", tc.channel)
		if err != nil || f.ID != tc.want {
			t.Errorf("%s got %d, %v, want %d", tc.channel, f.ID, err, tc.want)
		}
	}
	if _, err := selectLatest(files, "1.17.1", "all"); err == nil {
		t.Error("accepted unsupported minecraft version")
	}
}

func jarData(t *testing.T) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	z := zip.NewWriter(b)
	f, err := z.Create("META-INF/mods.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("modLoader=\"javafml\"\n")); err != nil {
		t.Fatal(err)
	}
	f, err = z.Create("assets/demo/README.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("download verification mock")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVerifyJar(t *testing.T) {
	raw := jarData(t)
	fileName := filepath.Join(t.TempDir(), "sample.jar")
	if err := os.WriteFile(fileName, raw, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(raw)
	f := File{FileLength: int64(len(raw)), Hashes: []Hash{{Value: hex.EncodeToString(sum[:]), Algo: 1}}}
	valid, err := verifyJar(fileName, f, 1<<20)
	if err != nil || valid.SHA256 == "" {
		t.Fatalf("valid JAR failed: %v, %+v", err, valid)
	}
	f.Hashes[0].Value = strings.Repeat("0", 40)
	if _, err := verifyJar(fileName, f, 1<<20); err == nil {
		t.Error("accepted invalid hash")
	}
	if err := os.WriteFile(fileName, []byte("not a jar"), 0644); err != nil {
		t.Fatal(err)
	}
	f.FileLength = int64(len("not a jar"))
	corruptedSum := sha1.Sum([]byte("not a jar"))
	f.Hashes[0].Value = hex.EncodeToString(corruptedSum[:])
	if _, err := verifyJar(fileName, f, 1<<20); err == nil {
		t.Error("accepted non-JAR despite correct hash")
	}
}

func TestDotEnvDoesNotOverrideShell(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	k := "CFMODSYNC_TEST_UNIQUE_ENV"
	t.Setenv(k, "shell")
	if err := os.WriteFile(p, []byte(k+"=file\nTEST_ANOTHER_CFMODSYNC='hello world'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("TEST_ANOTHER_CFMODSYNC")
	if err := loadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(k) != "shell" || os.Getenv("TEST_ANOTHER_CFMODSYNC") != "hello world" {
		t.Error(".env parse/precedence incorrect")
	}
}

func TestAPIResolutionDownloadDepsAndCached(t *testing.T) {
	jar := jarData(t)
	digest := sha1.Sum(jar)
	hash := hex.EncodeToString(digest[:])
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" && !strings.HasPrefix(r.URL.Path, "/blob/") {
			t.Errorf("API request missing x-api-key: %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1/mods/search":
			if r.URL.Query().Get("slug") != "create" || r.URL.Query().Get("classId") != "6" {
				t.Errorf("bad search query: %v", r.URL.Query())
			}
			io.WriteString(w, `{"data":[{"id":10,"gameId":432,"classId":6,"name":"Create","slug":"create","isAvailable":true,"allowModDistribution":true}]}`)
		case "/v1/mods/20":
			io.WriteString(w, `{"data":{"id":20,"gameId":432,"classId":6,"name":"Common Library","slug":"common-lib","isAvailable":true,"allowModDistribution":true}}`)
		case "/v1/mods/10/files", "/v1/mods/20/files":
			if r.URL.Query().Get("gameVersion") != "1.20.1" || r.URL.Query().Get("modLoaderType") != "1" {
				t.Errorf("bad compatibility filter: %v", r.URL.Query())
			}
			id := 10
			fn := "create.jar"
			if strings.Contains(r.URL.Path, "/20/") {
				id = 20
				fn = "common-lib.jar"
			}
			deps := `[]`
			if id == 10 {
				deps = `[{"modId":20,"relationType":3},{"modId":99,"relationType":2}]`
			}
			fmt.Fprintf(w, `{"data":[{"id":%d,"modId":%d,"fileName":%q,"fileDate":"2026-10-21T00:00:00Z","fileLength":%d,"gameVersions":["1.20.1","Forge"],"isAvailable":true,"releaseType":1,"hashes":[{"algo":1,"value":%q}],"dependencies":%s}],"pagination":{"totalCount":1}}`, id*100, id, fn, len(jar), hash, deps)
		case "/v1/mods/10/files/1000/download-url":
			fmt.Fprintf(w, `{"data":%q}`, server.URL+"/blob/create.jar")
		case "/v1/mods/20/files/2000/download-url":
			fmt.Fprintf(w, `{"data":%q}`, server.URL+"/blob/common-lib.jar")
		case "/blob/create.jar", "/blob/common-lib.jar":
			w.Header().Set("Content-Type", "application/java-archive")
			w.Write(jar)
		default:
			http.NotFound(w, r)
		}
	})
	server = httptest.NewTLSServer(handler)
	defer server.Close()
	client := &Client{APIBase: server.URL + "/v1", HTTP: server.Client(), Key: "testing-key"}
	output := t.TempDir()
	cfg := Options{Version: "1.20.1", Channel: "all", Deps: true, Output: output, MaxBytes: 1 << 20, Workers: 2}
	slugs := []string{"create"}
	m := runDownload(context.Background(), client, server.Client(), slugs, cfg)
	if len(m.Errors) != 0 || len(m.Entries) != 2 {
		t.Fatalf("unexpected run result: %+v", m)
	}
	for _, entry := range m.Entries {
		if entry.Status != "downloaded" {
			t.Errorf("first run should download: %+v", entry)
		}
		if entry.Verified == nil {
			t.Errorf("missing verification: %+v", entry)
		}
	}
	if len(verifyManifest(output, m, cfg.MaxBytes)) != 0 {
		t.Error("offline verification failed")
	}
	if err := writeManifest(filepath.Join(output, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	saved, err := readManifest(filepath.Join(output, "manifest.json"))
	if err != nil || len(saved.Entries) != 2 {
		t.Fatalf("cannot reload manifest: %v", err)
	}
	m2 := runDownload(context.Background(), client, server.Client(), slugs, cfg)
	for _, entry := range m2.Entries {
		if entry.Status != "cached" {
			t.Errorf("second run should be cached: %+v", entry)
		}
	}
	if err := os.WriteFile(filepath.Join(output, "create.jar"), []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	m3 := runDownload(context.Background(), client, server.Client(), slugs, cfg)
	if len(m3.Errors) == 0 {
		t.Errorf("corrupt cached file was silently overwritten")
	}
	cfg.Force = true
	m4 := runDownload(context.Background(), client, server.Client(), slugs, cfg)
	if len(m4.Errors) != 0 {
		t.Errorf("force could not safely repair corrupt file: %+v", m4.Errors)
	}
}

func TestDownloadRejectsMissingHashesAndHTTP(t *testing.T) {
	f := File{FileName: "mod.jar", ID: 1, FileLength: 10}
	out := t.TempDir()
	if _, _, err := downloadJar(context.Background(), http.DefaultClient, "http://example.com/x.jar", out, f, "key", 1<<20, false); err == nil {
		t.Error("allowed insecure URL")
	}
	if _, _, err := downloadJar(context.Background(), http.DefaultClient, "https://example.com/x.jar", out, f, "key", 1<<20, false); err == nil {
		t.Error("allowed non-verified download")
	}
}

func TestDownloadHeaderSanitization(t *testing.T) {
	// On cross-domain redirects, a custom x-api-key must not be copied.
	downloader := httpDownloadClient("secret")
	var redirectErr error
	req := httptest.NewRequest("GET", "https://unrelated.org/resource", nil)
	req.Header.Set("x-api-key", "secret")
	original := httptest.NewRequest("GET", "https://edge.forgecdn.net/files/1/2/test.jar", nil)
	redirectErr = downloader.CheckRedirect(req, []*http.Request{original})
	if redirectErr != nil || req.Header.Get("x-api-key") != "" {
		t.Errorf("leaked x-api-key to untrusted redirect: %v", redirectErr)
	}
}

func TestZipCRCDetectsCorruption(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	header := &zip.FileHeader{Name: "stored.dat", Method: zip.Store}
	f, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("verify-crc-needle")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "v.jar")
	if err := os.WriteFile(original, payload.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkZipCRC(original, 1<<20); err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(payload.Bytes(), []byte("verify-crc-needle"), []byte("verify-crc-noodle"), 1)
	if err := os.WriteFile(original, mutated, 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkZipCRC(original, 1<<20); err == nil {
		t.Error("ZIP CRC mismatch not detected")
	}
}

func TestExampleProjectURLList(t *testing.T) {
	slugs, err := loadModSlugs("../client/.mods.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(slugs) != 3 {
		t.Fatalf("expected 3 example project URLs, got %d", len(slugs))
	}
}
