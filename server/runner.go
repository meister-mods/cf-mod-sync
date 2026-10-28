package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Options struct {
	ForgeVersion         string
	TargetSide           string
	PinnedFiles          map[string]int64
	CandidateLimit       int
	Baseline             string
	AllowReviewedChanges bool
	Progress             func(string)
	Version              string
	Channel              string
	Input                string
	Output               string
	Deps                 bool
	Force                bool
	DryRun               bool
	Prune                bool
	Workers              int
	MaxBytes             int64
}

type Issue struct {
	Project string `json:"project"`
	Error   string `json:"error"`
}

type Entry struct {
	Metadata              *JarMetadata `json:"metadata,omitempty"`
	RequiredBy            []string     `json:"required_by,omitempty"`
	Name                  string       `json:"name"`
	Slug                  string       `json:"slug"`
	ProjectURL            string       `json:"project_url"`
	Requested             bool         `json:"requested"`
	ModID                 int64        `json:"mod_id"`
	FileID                int64        `json:"file_id"`
	Filename              string       `json:"filename"`
	FileDate              string       `json:"file_date"`
	ReleaseType           string       `json:"release_type"`
	GameVersions          []string     `json:"game_versions"`
	Hashes                []Hash       `json:"api_hashes"`
	ExpectedLength        int64        `json:"expected_length"`
	RequiredDependencyIDs []int64      `json:"required_dependency_ids,omitempty"`
	FilePageURL           string       `json:"file_page_url"`
	Status                string       `json:"status"`
	Verified              *Verified    `json:"verified,omitempty"`
	Error                 string       `json:"error,omitempty"`
}

type Manifest struct {
	ForgeVersion      string               `json:"forge_version,omitempty"`
	RequestedProjects []string             `json:"requested_projects,omitempty"`
	Warnings          []Issue              `json:"warnings,omitempty"`
	Compatibility     *CompatibilityReport `json:"compatibility,omitempty"`
	SchemaVersion     int                  `json:"schema_version"`
	GeneratedAt       string               `json:"generated_at"`
	MinecraftVersion  string               `json:"minecraft_version"`
	ModLoader         string               `json:"mod_loader"`
	ReleaseChannel    string               `json:"release_channel"`
	Entries           []Entry              `json:"entries"`
	Errors            []Issue              `json:"errors,omitempty"`
}

type selected struct {
	prepared  *Entry
	mod       Mod
	file      File
	requested bool
}
type queueItem struct {
	slug      string
	id        int64
	requested bool
	parent    string
}

func dependencyIDs(f File) []int64 {
	out := make([]int64, 0)
	seen := make(map[int64]bool)
	for _, dep := range f.Dependencies {
		if dep.RelationType == 3 && dep.ModID > 0 && !seen[dep.ModID] {
			out = append(out, dep.ModID)
			seen[dep.ModID] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func resolveMods(ctx context.Context, c *Client, slugs []string, cfg Options) ([]selected, []Issue) {
	queue := make([]queueItem, 0, len(slugs))
	for _, slug := range slugs {
		queue = append(queue, queueItem{slug: slug, requested: true})
	}
	var selections []selected
	var issues []Issue
	doneIDs := make(map[int64]bool)
	doneSlugs := make(map[string]bool)
	owners := make(map[string]int64)
	for index := 0; index < len(queue); index++ {
		if len(queue) > 500 {
			issues = append(issues, Issue{Project: "dependencies", Error: "safety limit: more than 500 related mods"})
			break
		}
		if ctx.Err() != nil {
			issues = append(issues, Issue{Project: "execution", Error: ctx.Err().Error()})
			break
		}
		req := queue[index]
		key := req.slug
		if req.id > 0 {
			key = fmt.Sprintf("mod ID %d (required by %s)", req.id, req.parent)
		}
		var mod Mod
		var err error
		if req.id == 0 {
			if doneSlugs[req.slug] {
				continue
			}
			doneSlugs[req.slug] = true
			mod, err = c.searchMod(ctx, req.slug)
		} else {
			if doneIDs[req.id] {
				continue
			}
			mod, err = c.getMod(ctx, req.id)
		}
		if err != nil {
			issues = append(issues, Issue{Project: key, Error: err.Error()})
			continue
		}
		if doneIDs[mod.ID] {
			continue
		}
		doneIDs[mod.ID] = true
		files, err := c.getFiles(ctx, mod.ID, cfg.Version)
		if err != nil {
			issues = append(issues, Issue{Project: mod.Slug, Error: err.Error()})
			continue
		}
		var f File
		if pin := cfg.PinnedFiles[mod.Slug]; pin > 0 {
			f, err = c.getFile(ctx, mod.ID, pin)
			if err == nil {
				f, err = selectLatest([]File{f}, cfg.Version, "all")
			}
		} else {
			f, err = selectLatest(files, cfg.Version, cfg.Channel)
		}
		if err != nil {
			issues = append(issues, Issue{Project: mod.Slug, Error: err.Error()})
			continue
		}
		if f.ModID != 0 && f.ModID != mod.ID {
			issues = append(issues, Issue{Project: mod.Slug, Error: fmt.Sprintf("API file mod ID mismatch (%d)", f.ModID)})
			continue
		}
		if owner, occupied := owners[strings.ToLower(f.FileName)]; occupied && owner != mod.ID {
			issues = append(issues, Issue{Project: mod.Slug, Error: fmt.Sprintf("filename collision: %q also belongs to mod ID %d", f.FileName, owner)})
			continue
		}
		owners[strings.ToLower(f.FileName)] = mod.ID
		selections = append(selections, selected{mod: mod, file: f, requested: req.requested})
		if cfg.Deps {
			for _, depID := range dependencyIDs(f) {
				if !doneIDs[depID] {
					queue = append(queue, queueItem{id: depID, parent: mod.Slug})
				}
			}
		}
	}
	return selections, issues
}

func releaseName(t int) string {
	switch t {
	case 1:
		return "release"
	case 2:
		return "beta"
	case 3:
		return "alpha"
	}
	return "unknown"
}

func manifestEntry(p selected) Entry {
	return Entry{
		Name: p.mod.Name, Slug: p.mod.Slug, ProjectURL: projectURL(p.mod.Slug),
		Requested: p.requested, ModID: p.mod.ID, FileID: p.file.ID,
		Filename: p.file.FileName, FileDate: p.file.FileDate, ReleaseType: releaseName(p.file.ReleaseType),
		GameVersions: p.file.GameVersions, Hashes: p.file.Hashes, ExpectedLength: p.file.FileLength,
		RequiredDependencyIDs: dependencyIDs(p.file),
		FilePageURL:           fmt.Sprintf("%s/files/%d", projectURL(p.mod.Slug), p.file.ID),
		Status:                "planned",
	}
}

func newManifest(cfg Options) Manifest {
	return Manifest{
		SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		MinecraftVersion: cfg.Version, ModLoader: "Forge", ReleaseChannel: cfg.Channel,
		ForgeVersion: cfg.ForgeVersion,
		Entries:      []Entry{},
	}
}

func runDownload(ctx context.Context, c *Client, hc *http.Client, slugs []string, cfg Options) Manifest {
	manifest := newManifest(cfg)
	manifest.RequestedProjects = append([]string{}, slugs...)
	if cfg.DryRun {
		items, issues := resolveMods(ctx, c, slugs, cfg)
		manifest.Errors = issues
		for _, item := range items {
			manifest.Entries = append(manifest.Entries, manifestEntry(item))
		}
		manifest.Entries = append(manifest.Entries, missingRequested(slugs, manifest.Entries, issues)...)
		manifest.Compatibility = &CompatibilityReport{State: "not_checked", Warnings: []Issue{{"dry-run", "JAR metadata and Forge patch compatibility require downloading candidate files."}}}
		return manifest
	}
	baseline, err := loadInstalledFiles(cfg.Baseline, cfg.MaxBytes)
	if err != nil {
		manifest.Errors = []Issue{{"baseline", err.Error()}}
		return manifest
	}
	cache, err := newModServer(ctx, ServerOptions{CacheDir: filepath.Join(cfg.Output, ".candidate-cache"), Workers: cfg.Workers, MaxBytes: cfg.MaxBytes, MetadataTTL: 15 * time.Minute, JobTTL: time.Hour}, c, hc)
	if err != nil {
		manifest.Errors = []Issue{{"cache", err.Error()}}
		return manifest
	}
	defer cache.cancel()
	items, issues, warnings := resolveCompatible(ctx, c, slugs, cfg, cache.ensureFile)
	manifest.Errors = issues
	manifest.Warnings = warnings
	manifest.Entries = preparedEntries(items)
	report := validateCompatibility(manifest.Entries, cfg, baseline, false)
	if len(issues) > 0 {
		report.State = "failed"
		report.Errors = append(report.Errors, issues...)
	}
	manifest.Compatibility = &report
	manifest.Errors = append([]Issue{}, report.Errors...)
	manifest.Warnings = append(manifest.Warnings, report.Warnings...)
	manifest.Entries = append(manifest.Entries, missingRequested(slugs, manifest.Entries, issues)...)
	if report.RequiresReview && !cfg.AllowReviewedChanges {
		manifest.Errors = append(manifest.Errors, Issue{"baseline", "Review required: missing, replaced, or downgraded baseline mods. Pin files or use -allow-reviewed-changes after reviewing the report."})
	}
	if len(manifest.Errors) > 0 {
		blockManifestEntries(&manifest)
		return manifest
	}
	if err := checkOutputContents(cfg.Output, manifest.Entries, cfg.Prune, cfg.MaxBytes); err != nil {
		manifest.Errors = append(manifest.Errors, Issue{"output", err.Error()})
		blockManifestEntries(&manifest)
		return manifest
	}
	for index, item := range items {
		entry := manifest.Entries[index]
		var verified Verified
		var status string
		if cfg.Force {
			source, sourceErr := c.getDownloadURL(ctx, item.mod.ID, item.file.ID)
			err = sourceErr
			if err == nil {
				status, verified, err = downloadJar(ctx, hc, source, cfg.Output, item.file, c.Key, cfg.MaxBytes, true)
			}
		} else {
			status, verified, err = copyVerifiedJar(ctx, filepath.Join(cache.fileDir(item.mod.ID, item.file.ID), item.file.FileName), cfg.Output, item.file, cfg.MaxBytes)
		}
		if err != nil {
			entry.Status = "failed"
			entry.Error = err.Error()
			entry.Verified = nil
			manifest.Errors = append(manifest.Errors, Issue{entry.Slug, err.Error()})
		} else {
			entry.Status = status
			entry.Verified = &verified
		}
		manifest.Entries[index] = entry
	}
	return manifest
}
func blockManifestEntries(manifest *Manifest) {
	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		if entry.Status == "cached" || entry.Status == "downloaded" {
			entry.Status = "blocked"
			entry.Verified = nil
			entry.Error = "Batch validation or output checks did not pass."
		}
	}
}
func writeManifest(path string, m Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cfmodsync-manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// On Windows Rename does not necessarily replace existing files.
	if _, err := os.Stat(path); err == nil {
		backup := path + ".old"
		_ = os.Remove(backup)
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			_ = os.Rename(backup, path)
			return err
		}
		_ = os.Remove(backup)
		return nil
	}
	return os.Rename(tmp.Name(), path)
}

func readManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&m); err != nil {
		return Manifest{}, err
	}
	if m.SchemaVersion != 1 {
		return Manifest{}, errors.New("unrecognized manifest schema version")
	}
	return m, nil
}

func verifyManifest(output string, m Manifest, maxBytes int64) []Issue {
	issues := append([]Issue(nil), m.Errors...)
	if len(m.Entries) == 0 {
		issues = append(issues, Issue{Project: "manifest", Error: "no files recorded in manifest"})
	}
	count := 0
	var inspected []Entry
	for _, entry := range m.Entries {
		if entry.Verified == nil || (entry.Status != "cached" && entry.Status != "downloaded") {
			issues = append(issues, Issue{Project: entry.Slug, Error: "file was not downloaded/verified"})
			continue
		}
		if !safeJarName(entry.Filename) {
			issues = append(issues, Issue{Project: entry.Slug, Error: "unsafe manifest filename"})
			continue
		}
		f := File{FileLength: entry.ExpectedLength, Hashes: entry.Hashes}
		verified, err := verifyJar(filepath.Join(output, entry.Filename), f, maxBytes)
		if err == nil && (verified.SHA256 != entry.Verified.SHA256 || verified.Size != entry.Verified.Size) {
			err = errors.New("manifest SHA-256/size mismatch")
		}
		if err != nil {
			issues = append(issues, Issue{Project: entry.Slug, Error: err.Error()})
			fmt.Fprintf(os.Stderr, "[FAIL] %s: %v\n", entry.Slug, err)
		} else {
			metadata, err := inspectJarMetadata(filepath.Join(output, entry.Filename))
			if err != nil {
				issues = append(issues, Issue{entry.Slug, "JAR metadata: " + err.Error()})
				continue
			}
			entry.Metadata = metadata
			inspected = append(inspected, entry)
			fmt.Printf("[VERIFIED] %s\n", entry.Filename)
			count++
		}
	}
	report := validateCompatibility(inspected, Options{Version: m.MinecraftVersion, ForgeVersion: m.ForgeVersion}, nil, false)
	issues = append(issues, report.Errors...)
	fmt.Printf("Verified %d files; issues: %d\n", count, len(issues))
	return issues
}

// pruneOld only deletes files referenced in the previous manifest that are not
// in the new manifest, and only after re-verifying those old files.
func pruneOld(output string, oldM, newM Manifest, maxBytes int64) error {
	if len(newM.Errors) != 0 {
		return errors.New("refusing to prune after incomplete download")
	}
	if oldM.MinecraftVersion != newM.MinecraftVersion || oldM.ModLoader != newM.ModLoader {
		return errors.New("refusing to prune across different Minecraft/loader profiles")
	}
	current := make(map[string]bool)
	for _, entry := range newM.Entries {
		current[entry.Filename] = true
	}
	for _, old := range oldM.Entries {
		if old.Verified == nil || current[old.Filename] || !safeJarName(old.Filename) {
			continue
		}
		target := filepath.Join(output, old.Filename)
		result, err := verifyJar(target, File{FileLength: old.ExpectedLength, Hashes: old.Hashes}, maxBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || result.SHA256 != old.Verified.SHA256 { // never remove changed/user-replaced files
			fmt.Fprintf(os.Stderr, "[KEEP] %s (not verified as previously managed)\n", old.Filename)
			continue
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		fmt.Printf("[PRUNE] %s\n", old.Filename)
	}
	return nil
}
