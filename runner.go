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
	"sync"
	"time"
)

type Options struct {
	Version  string
	Channel  string
	Input    string
	Output   string
	Deps     bool
	Force    bool
	DryRun   bool
	Prune    bool
	Workers  int
	MaxBytes int64
}

type Issue struct {
	Project string `json:"project"`
	Error   string `json:"error"`
}

type Entry struct {
	Name                  string    `json:"name"`
	Slug                  string    `json:"slug"`
	ProjectURL            string    `json:"project_url"`
	Requested             bool      `json:"requested"`
	ModID                 int64     `json:"mod_id"`
	FileID                int64     `json:"file_id"`
	Filename              string    `json:"filename"`
	FileDate              string    `json:"file_date"`
	ReleaseType           string    `json:"release_type"`
	GameVersions          []string  `json:"game_versions"`
	Hashes                []Hash    `json:"api_hashes"`
	ExpectedLength        int64     `json:"expected_length"`
	RequiredDependencyIDs []int64   `json:"required_dependency_ids,omitempty"`
	FilePageURL           string    `json:"file_page_url"`
	Status                string    `json:"status"`
	Verified              *Verified `json:"verified,omitempty"`
	Error                 string    `json:"error,omitempty"`
}

type Manifest struct {
	SchemaVersion    int     `json:"schema_version"`
	GeneratedAt      string  `json:"generated_at"`
	MinecraftVersion string  `json:"minecraft_version"`
	ModLoader        string  `json:"mod_loader"`
	ReleaseChannel   string  `json:"release_channel"`
	Entries          []Entry `json:"entries"`
	Errors           []Issue `json:"errors,omitempty"`
}

type selected struct {
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
		f, err := selectLatest(files, cfg.Version, cfg.Channel)
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
		Entries: []Entry{},
	}
}

func runDownload(ctx context.Context, c *Client, hc *http.Client, slugs []string, cfg Options) Manifest {
	selectedMods, issues := resolveMods(ctx, c, slugs, cfg)
	manifest := newManifest(cfg)
	manifest.Errors = append(manifest.Errors, issues...)
	manifest.Entries = make([]Entry, len(selectedMods))
	for i, p := range selectedMods {
		manifest.Entries[i] = manifestEntry(p)
	}
	if cfg.DryRun {
		return manifest
	}

	// Download in parallel. Results are written to unique indices, so output
	// manifest ordering remains deterministic regardless of completion order.
	jobs := make(chan int)
	var wg sync.WaitGroup
	var lock sync.Mutex
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				p := selectedMods[idx]
				entry := manifestEntry(p)
				sourceURL, err := c.getDownloadURL(ctx, p.mod.ID, p.file.ID)
				if err == nil {
					var result Verified
					entry.Status, result, err = downloadJar(ctx, hc, sourceURL, cfg.Output, p.file, c.Key, cfg.MaxBytes, cfg.Force)
					if err == nil {
						entry.Verified = &result
					}
				}
				if err != nil {
					entry.Status = "failed"
					entry.Error = err.Error()
				}
				lock.Lock()
				manifest.Entries[idx] = entry
				if err != nil {
					manifest.Errors = append(manifest.Errors, Issue{Project: p.mod.Slug, Error: err.Error()})
					fmt.Fprintf(os.Stderr, "[FAIL] %s: %v\n", p.mod.Slug, err)
				} else {
					fmt.Printf("[OK] %-38s %-10s %s\n", p.mod.Slug, entry.Status, entry.Filename)
				}
				lock.Unlock()
			}
		}()
	}
	for i := range selectedMods {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return manifest
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
	for _, entry := range m.Entries {
		if entry.Verified == nil || (entry.Status != "cached" && entry.Status != "downloaded") {
			if entry.Status != "failed" {
				issues = append(issues, Issue{Project: entry.Slug, Error: "file was not downloaded/verified"})
			}
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
			fmt.Printf("[VERIFIED] %s\n", entry.Filename)
			count++
		}
	}
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
