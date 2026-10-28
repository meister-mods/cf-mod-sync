package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func modJar(t *testing.T, id, version, forge, extra string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("META-INF/mods.toml")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(file, "modLoader=\"javafml\"\nloaderVersion=\"[1,)\"\n[[mods]]\nmodId=%q\nversion=%q\n[[dependencies.%s]]\nmodId=\"forge\"\nmandatory=true\nversionRange=%q\n%s", id, version, id, forge, extra)
	file, err = writer.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(file, "Manifest-Version: 1.0\r\nImplementation-Version: 1.2.43-RELEASE\r\n\r\n")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestMavenRequirementsRegression(t *testing.T) {
	for _, test := range []struct {
		version, spec string
		want          bool
	}{
		{"0.0.47-snapshot", "[1.20-1.2.30-ALPHA,)", false},
		{"1.20-1.2.43-RELEASE", "[1.20-1.2.30-ALPHA,)", true},
		{"47.3.0", "[47.2,48)", true}, {"48.0", "[47,48)", false},
		{"1.20.1", "[1.20,1.20.5)", true}, {"1.0-final", "[1.0]", true},
		{"2.0", "(,1.0],[2.0,)", true}, {"1.5", "(,1.0],[2.0,)", false},
		{"1.0-rc1", "[1.0,)", false},
		{"47.4.28", "*", true}, {"1.20.2", "1.20.1", true}, {"1.20.1", "[1.20.1,1.21,0)", true},
	} {
		got, err := versionInRange(test.version, test.spec)
		if err != nil || got != test.want {
			t.Errorf("%s in %s: got %v, %v", test.version, test.spec, got, err)
		}
	}
	for _, spec := range []string{"[2,1)", "[1,1)", "[1,)junk", "[1,),", "[1"} {
		if _, err := versionInRange("1", spec); err == nil {
			t.Errorf("accepted invalid range %s", spec)
		}
	}
	if _, err := versionInRange("${file.jarVersion}", "[1,)"); err == nil {
		t.Fatal("accepted unresolved version")
	}
}

func TestActualJarMetadataAndBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "name-says-1.20.4.jar")
	raw := modJar(t, "multipiston", "${file.jarVersion}", "[47,48)", `
[[dependencies.multipiston]]
modId="minecraft"
mandatory=true
versionRange="[1.20,1.20.5)"
side="BOTH"
`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := inspectJarMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Mods[0].Version != "1.2.43-RELEASE" {
		t.Fatalf("manifest substitution failed: %+v", metadata)
	}
	hash := sha256.Sum256(raw)
	entry := Entry{Slug: "multi-piston", Filename: filepath.Base(path), Metadata: metadata, Verified: &Verified{SHA256: hex.EncodeToString(hash[:])}}
	cfg := Options{Version: "1.20.1", ForgeVersion: "47.3.0"}
	report := validateCompatibility([]Entry{entry}, cfg, nil, false)
	if len(report.Errors) != 0 {
		t.Fatalf("wrongly used filename instead of metadata: %+v", report)
	}
	installed, err := loadInstalledFiles(filepath.Dir(path), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	report = validateCompatibility(nil, cfg, installed, false)
	if !report.RequiresReview || len(report.Changes) != 1 || report.Changes[0].Kind != "removed" {
		t.Fatalf("missing baseline mod not reported: %+v", report)
	}
	old := *metadata
	old.Mods = []ModIdentity{{ID: "multipiston", Version: "2.0"}}
	olderEntry := entry
	olderEntry.Metadata = &old
	reader, _ := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	tomlText, _ := readZipText(reader, "META-INF/mods.toml")
	baseline := []InstalledFile{{Filename: "old.jar", ModsTOML: strings.ReplaceAll(tomlText, "${file.jarVersion}", "3.0"), SHA256: strings.Repeat("0", 64)}}
	report = validateCompatibility([]Entry{olderEntry}, cfg, baseline, false)
	if report.State != "review_required" || report.Changes[0].Kind != "downgrade" {
		t.Fatalf("downgrade not gated: %+v", report)
	}
}

func TestSignedManifestMainSectionLimit(t *testing.T) {
	for _, largeMain := range []bool{false, true} {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		file, _ := writer.Create("META-INF/MANIFEST.MF")
		main := "Manifest-Version: 1.0\r\nImplementation-Version: 2.0\r\n\r\n"
		if largeMain {
			main = "Manifest-Version: 1.0\r\nLarge: " + strings.Repeat("x", metadataLimit) + "\r\n\r\n"
		}
		fmt.Fprint(file, main+strings.Repeat("Name: signed.class\r\nSHA-256-Digest: data\r\n\r\n", 4000))
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := readZipText(reader, "META-INF/MANIFEST.MF")
		if largeMain {
			if err == nil {
				t.Fatal("accepted oversized main attributes")
			}
		} else if err != nil || manifestProperties(raw)["implementation-version"] != "2.0" {
			t.Fatalf("signed manifest's main section was rejected: %v", err)
		}
	}
}

func TestLanguageLoaderUsesForgeMajorVersion(t *testing.T) {
	metadata, err := parseForgeMetadata("modLoader=\"javafml\"\nloaderVersion=\"[47]\"\n[[mods]]\nmodId=\"example\"\nversion=\"1.0\"\n", "")
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{Filename: "example.jar", Metadata: metadata}
	if report := validateCompatibility([]Entry{entry}, Options{Version: "1.20.1", ForgeVersion: "47.4.28"}, nil, false); len(report.Errors) != 0 {
		t.Fatalf("FML major version mistaken for Forge build: %+v", report.Errors)
	}
	if report := validateCompatibility([]Entry{entry}, Options{Version: "1.20.1", ForgeVersion: "48.0.1"}, nil, false); len(report.Errors) == 0 {
		t.Fatal("incompatible FML major accepted")
	}
	metadata.Loader = "kotlinforforge"
	if report := validateCompatibility([]Entry{entry}, Options{Version: "1.20.1", ForgeVersion: "48.0.1"}, nil, false); len(report.Errors) != 0 {
		t.Fatal("custom loader version was confused with Forge version")
	}
}

func TestJarJarNegotiationAndDuplicateRootProtection(t *testing.T) {
	cfg := Options{Version: "1.20.1", ForgeVersion: "47.4.28"}
	bundle := func(version, spec string) Entry {
		return Entry{Filename: version + ".jar", Metadata: &JarMetadata{
			Bundles: []BundledArtifact{{Key: "example:lib", Version: version, Range: spec}},
			Mods:    []ModIdentity{{ID: "shared_lib", Version: version, Bundled: true, Bundle: "example:lib@" + version}},
		}}
	}
	a, b := bundle("1.0", "[1,3)"), bundle("2.0", "[2,3)")
	if report := validateCompatibility([]Entry{a, b}, cfg, nil, false); len(report.Errors) != 0 {
		t.Fatalf("compatible shared artifact rejected: %+v", report.Errors)
	}
	a.Metadata.Bundles[0].Range = "[1,2)"
	if report := validateCompatibility([]Entry{a, b}, cfg, nil, false); len(report.Errors) == 0 {
		t.Fatal("conflicting embedded artifact requirements accepted")
	}
	root := Entry{Filename: "standalone.jar", Metadata: &JarMetadata{Mods: []ModIdentity{{ID: "shared_lib", Version: "2.0"}}}}
	if report := validateCompatibility([]Entry{b, root}, cfg, nil, false); len(report.Errors) != 0 {
		t.Fatalf("standalone provider plus embedded copy rejected: %+v", report.Errors)
	}
	other := root
	other.Filename = "duplicate.jar"
	if report := validateCompatibility([]Entry{root, other}, cfg, nil, false); len(report.Errors) == 0 {
		t.Fatal("two standalone roots with the same ID accepted")
	}
}

func TestFilenameFamilyReviewAndFilePinConflicts(t *testing.T) {
	if filenameFamily("pamhc2foodcore-1.20.4-1.0.5.jar") != filenameFamily("pamhc2foodcore-1.20.1-1.0.0.jar") {
		t.Fatal("Minecraft version was mistaken for a content-family change")
	}
	if filenameFamily("blueseoul_garam_v4.4.1_1.20.1.jar") == filenameFamily("blueseoul_narae_v6.1.jar") {
		t.Fatal("content family change was not detected")
	}
	path := filepath.Join(t.TempDir(), ".mods")
	if err := os.WriteFile(path, []byte(projectURL("demo")+"/files/123\n"+projectURL("demo")+"/files/456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadProjectList(path); err == nil {
		t.Fatal("conflicting file pins accepted")
	}
	slug, pin, err := parseProjectInput(projectURL("demo") + "/files/123")
	if err != nil || slug != "demo" || pin != 123 {
		t.Fatalf("file pin lost: %s %d %v", slug, pin, err)
	}
}

func TestHighestVersionForExactForgeAndDependencyCombination(t *testing.T) {
	parentDependency := func(spec string) string {
		return fmt.Sprintf("\n[[dependencies.colony]]\nmodId=\"helper\"\nmandatory=true\nversionRange=%q\nside=\"BOTH\"\n", spec)
	}
	payloads := map[int64][]byte{
		101: modJar(t, "colony", "3.0", "[48,)", parentDependency("[1,)")),
		102: modJar(t, "colony", "2.0", "[47.4,48)", parentDependency("[1.2.30,2)")),
		103: modJar(t, "colony", "1.0", "[47,48)", parentDependency("[2,3)")),
		201: modJar(t, "helper", "0.0.47-snapshot", "[47,48)", ""),
		202: modJar(t, "helper", "1.2.43-RELEASE", "[47,48)", ""),
		203: modJar(t, "helper", "2.1.0", "[47,48)", ""),
	}
	files := make(map[int64]File)
	for id, raw := range payloads {
		hash := sha1.Sum(raw)
		modID := int64(10)
		if id >= 200 {
			modID = 20
		}
		file := File{ID: id, ModID: modID, FileName: fmt.Sprintf("mod-%d.jar", id), FileDate: fmt.Sprintf("2026-10-%02dT00:00:00Z", 30-int(id%100)), FileLength: int64(len(raw)), Hashes: []Hash{{Algo: 1, Value: hex.EncodeToString(hash[:])}}, IsAvailable: true, ReleaseType: 1, GameVersions: []string{"1.20.1", "Forge"}}
		if modID == 10 {
			file.Dependencies = []Dependency{{ModID: 20, RelationType: 3}}
		}
		if id == 102 {
			// Newer files sometimes omit a mandatory native dependency in the API.
			file.Dependencies = nil
		}
		files[id] = file
	}
	var upstream *httptest.Server
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/blob/") {
			var id int64
			fmt.Sscanf(r.URL.Path, "/blob/%d", &id)
			w.Write(payloads[id])
			return
		}
		mod := Mod{ID: 10, GameID: 432, ClassID: 6, Slug: "colony", Name: "Colony", IsAvailable: true, AllowModDistribution: true}
		switch {
		case r.URL.Path == "/v1/mods/search":
			if r.URL.Query().Get("slug") == "helper" {
				mod.ID, mod.Slug = 20, "helper"
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []Mod{mod}})
		case r.URL.Path == "/v1/mods/20":
			mod.ID = 20
			mod.Slug = "helper"
			json.NewEncoder(w).Encode(map[string]any{"data": mod})
		case strings.HasSuffix(r.URL.Path, "/download-url"):
			var modID, id int64
			fmt.Sscanf(r.URL.Path, "/v1/mods/%d/files/%d/download-url", &modID, &id)
			json.NewEncoder(w).Encode(map[string]any{"data": fmt.Sprintf("%s/blob/%d", upstream.URL, id)})
		case r.URL.Path == "/v1/mods/10/files", r.URL.Path == "/v1/mods/20/files":
			id := int64(10)
			if strings.Contains(r.URL.Path, "/20/") {
				id = 20
			}
			var result []File
			for _, file := range files {
				if file.ModID == id {
					result = append(result, file)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": result})
		default:
			var modID, id int64
			if n, _ := fmt.Sscanf(r.URL.Path, "/v1/mods/%d/files/%d", &modID, &id); n == 2 {
				json.NewEncoder(w).Encode(map[string]any{"data": files[id]})
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	c := &Client{APIBase: upstream.URL + "/v1", Key: "test", HTTP: upstream.Client()}
	service, err := newModServer(context.Background(), ServerOptions{CacheDir: t.TempDir(), Workers: 3, MaxBytes: 1 << 20, MetadataTTL: time.Hour, JobTTL: time.Hour}, c, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer service.cancel()
	for _, test := range []struct {
		forge          string
		parent, helper int64
	}{{"47.3.0", 103, 203}, {"47.4.0", 102, 202}} {
		cfg := Options{Version: "1.20.1", ForgeVersion: test.forge, Channel: "all", Deps: true, Workers: 3}
		items, issues, _ := resolveCompatible(context.Background(), c, []string{"colony"}, cfg, service.ensureFile)
		if len(issues) != 0 {
			t.Fatalf("Forge %s: %+v", test.forge, issues)
		}
		selectedIDs := map[int64]bool{}
		for _, item := range items {
			selectedIDs[item.file.ID] = true
		}
		if len(items) != 2 || !selectedIDs[test.parent] || !selectedIDs[test.helper] {
			t.Fatalf("Forge %s selected wrong versions: %+v", test.forge, items)
		}
	}
	cfg := Options{Version: "1.20.1", ForgeVersion: "47.4.0", Channel: "all", Deps: true, PinnedFiles: map[string]int64{"colony": 101}}
	items, issues, _ := resolveCompatible(context.Background(), c, []string{"colony"}, cfg, service.ensureFile)
	if len(items) != 0 || len(issues) == 0 {
		t.Fatal("accepted Forge-incompatible pinned file")
	}
	missing := missingRequested([]string{"colony"}, preparedEntries(items), issues)
	if len(missing) != 1 || missing[0].Status != "failed" {
		t.Fatal("requested project disappeared from failure report")
	}
}
