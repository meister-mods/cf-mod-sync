package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type candidateFetcher func(context.Context, selected) (Entry, error)
type compatibleResolver struct {
	ctx            context.Context
	client         *Client
	cfg            Options
	fetch          candidateFetcher
	mods           map[int64]Mod
	roots          map[int64]bool
	catalog        map[int64][]selected
	loaded         map[int64]bool
	warnings       []Issue
	lastErrors     []Issue
	steps          int
	nativeProjects map[string]int64
}

func resolveCompatible(ctx context.Context, c *Client, slugs []string, cfg Options, fetch candidateFetcher) ([]selected, []Issue, []Issue) {
	r := &compatibleResolver{ctx: ctx, client: c, cfg: cfg, fetch: fetch, mods: map[int64]Mod{}, roots: map[int64]bool{}, catalog: map[int64][]selected{}, loaded: map[int64]bool{}, nativeProjects: map[string]int64{}}
	var pending []int64
	var issues []Issue
	for _, slug := range slugs {
		mod, err := c.searchMod(ctx, slug)
		if err != nil {
			issues = append(issues, Issue{slug, err.Error()})
			continue
		}
		r.mods[mod.ID] = mod
		if !r.roots[mod.ID] {
			pending = append(pending, mod.ID)
			r.roots[mod.ID] = true
		}
	}
	if len(issues) > 0 {
		return nil, issues, nil
	}
	result, ok := r.solve(nil, pending)
	if !ok {
		if ctx.Err() != nil {
			return nil, []Issue{{"execution", ctx.Err().Error()}}, r.warnings
		}
		if r.steps > 10000 {
			return nil, []Issue{{"resolver", "Compatibility search exceeded 10000 steps; pin project files to narrow the search."}}, r.warnings
		}
		issues = append(issues, Issue{"resolver", "No compatible combination satisfies the requested Minecraft, Forge, and mod dependency versions."})
		issues = append(issues, r.lastErrors...)
		return nil, issues, r.warnings
	}
	return result, nil, r.warnings
}

func preparedEntries(items []selected) []Entry {
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		entry := manifestEntry(item)
		if item.prepared != nil {
			entry = *item.prepared
			entry.Requested = item.requested
		}
		for _, parent := range items {
			for _, id := range dependencyIDs(parent.file) {
				if id == item.mod.ID {
					entry.RequiredBy = append(entry.RequiredBy, parent.mod.Slug)
				}
			}
			if item.prepared != nil && parent.prepared != nil {
				for _, requirement := range parent.prepared.Metadata.Requirements {
					if !requirement.Mandatory || requirement.Side == "SERVER" {
						continue
					}
					for _, identity := range item.prepared.Metadata.Mods {
						if !identity.Bundled && identity.ID == requirement.ID && parent.mod.ID != item.mod.ID {
							entry.RequiredBy = append(entry.RequiredBy, parent.mod.Slug)
						}
					}
				}
			}
		}
		sort.Strings(entry.RequiredBy)
		unique := entry.RequiredBy[:0]
		for _, parent := range entry.RequiredBy {
			if len(unique) == 0 || unique[len(unique)-1] != parent {
				unique = append(unique, parent)
			}
		}
		entry.RequiredBy = unique
		entries = append(entries, entry)
	}
	return entries
}

func (r *compatibleResolver) solve(chosen []selected, pending []int64) ([]selected, bool) {
	r.steps++
	if r.ctx.Err() != nil || r.steps > 10000 {
		return nil, false
	}
	if len(chosen) > 500 {
		r.lastErrors = []Issue{{"dependencies", "More than 500 related projects."}}
		return nil, false
	}
	assigned := make(map[int64]bool)
	for _, item := range chosen {
		assigned[item.mod.ID] = true
	}
	for len(pending) > 0 && assigned[pending[0]] {
		pending = pending[1:]
	}
	if len(pending) == 0 {
		entries := preparedEntries(chosen)
		if r.cfg.Deps {
			// CurseForge's required relations can omit a mods.toml dependency.
			// Try exact project slugs, then verify the provider's actual mod ID.
			for _, modID := range missingNativeRequirements(entries) {
				if projectID := r.nativeProject(modID); projectID > 0 && !assigned[projectID] {
					return r.solve(chosen, []int64{projectID})
				}
			}
		}
		report := validateCompatibility(entries, r.cfg, nil, false)
		if len(report.Errors) > 0 {
			r.lastErrors = report.Errors
			return nil, false
		}
		return chosen, true
	}
	id := pending[0]
	for rank, candidate := range r.candidates(id) {
		next := append(append([]selected{}, chosen...), candidate)
		entries := preparedEntries(next)
		names := make(map[string]int64)
		collision := false
		for _, entry := range entries {
			key := strings.ToLower(entry.Filename)
			if owner, ok := names[key]; ok && owner != entry.ModID {
				collision = true
			}
			names[key] = entry.ModID
		}
		if collision {
			r.lastErrors = []Issue{{candidate.mod.Slug, "Selected files have colliding filenames."}}
			continue
		}
		report := validateCompatibility(entries, r.cfg, nil, true)
		if len(report.Errors) > 0 {
			r.lastErrors = report.Errors
			continue
		}
		todo := append([]int64{}, pending[1:]...)
		if r.cfg.Deps {
			todo = append(todo, dependencyIDs(candidate.file)...)
		}
		unique := make([]int64, 0, len(todo))
		seen := make(map[int64]bool)
		for _, pendingID := range todo {
			if pendingID != candidate.mod.ID && !assigned[pendingID] && !seen[pendingID] {
				seen[pendingID] = true
				unique = append(unique, pendingID)
			}
		}
		todo = unique
		if result, ok := r.solve(next, todo); ok {
			if rank > 0 {
				r.warnings = append(r.warnings, Issue{candidate.mod.Slug, fmt.Sprintf("Selected %s after %d higher-ranked candidate(s) could not satisfy the complete dependency combination.", candidate.file.FileName, rank)})
			}
			return result, true
		}
	}
	return nil, false
}

func missingNativeRequirements(entries []Entry) []string {
	active, _ := selectBundleTokens(entries, false)
	provided := map[string]bool{"minecraft": true, "forge": true, "fml": true}
	for _, entry := range entries {
		if entry.Metadata == nil {
			continue
		}
		for _, mod := range entry.Metadata.Mods {
			if mod.Bundle == "" || active[mod.Bundle] {
				provided[mod.ID] = true
			}
		}
	}
	missing := map[string]bool{}
	for _, entry := range entries {
		if entry.Metadata == nil {
			continue
		}
		for _, requirement := range entry.Metadata.Requirements {
			if requirement.Mandatory && requirement.Side != "SERVER" && !provided[requirement.ID] &&
				(requirement.Bundle == "" || active[requirement.Bundle]) {
				missing[requirement.ID] = true
			}
		}
	}
	var ids []string
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r *compatibleResolver) nativeProject(modID string) int64 {
	if id, known := r.nativeProjects[modID]; known {
		return id
	}
	r.nativeProjects[modID] = 0
	if len(r.nativeProjects) > 500 {
		return 0
	}
	for index, slug := range []string{modID, strings.ReplaceAll(modID, "_", "-")} {
		if index > 0 && slug == modID {
			continue
		}
		mod, err := r.client.searchMod(r.ctx, slug)
		if err != nil {
			continue
		}
		r.mods[mod.ID] = mod
		for _, candidate := range r.candidates(mod.ID) {
			for _, identity := range candidate.prepared.Metadata.Mods {
				if !identity.Bundled && identity.ID == modID {
					r.nativeProjects[modID] = mod.ID
					r.warnings = append(r.warnings, Issue{mod.Slug, "Resolved missing mandatory JAR dependency " + modID + " through an exact CurseForge slug and verified mod ID."})
					return mod.ID
				}
			}
		}
	}
	return 0
}

func (r *compatibleResolver) candidates(id int64) []selected {
	if r.loaded[id] {
		return r.catalog[id]
	}
	r.loaded[id] = true
	mod, exists := r.mods[id]
	if !exists {
		var err error
		mod, err = r.client.getMod(r.ctx, id)
		if err != nil {
			r.lastErrors = []Issue{{fmt.Sprint(id), err.Error()}}
			return nil
		}
		r.mods[id] = mod
	}
	pin := r.cfg.PinnedFiles[mod.Slug]
	var files []File
	var err error
	if pin > 0 {
		var file File
		file, err = r.client.getFile(r.ctx, id, pin)
		files = []File{file}
	} else {
		files, err = r.client.getFiles(r.ctx, id, r.cfg.Version)
	}
	if err != nil {
		r.lastErrors = []Issue{{mod.Slug, err.Error()}}
		return nil
	}
	var eligible []File
	seen := make(map[int64]bool)
	for _, file := range files {
		channel := r.cfg.Channel
		if pin > 0 {
			channel = "all"
		}
		_, err := selectLatest([]File{file}, r.cfg.Version, channel)
		if err == nil && (file.ModID == 0 || file.ModID == id) && !seen[file.ID] {
			eligible = append(eligible, file)
			seen[file.ID] = true
		}
	}
	if len(eligible) == 0 {
		r.lastErrors = []Issue{{mod.Slug, fmt.Sprintf("No available Forge %s JAR matches the release policy or pinned file.", r.cfg.Version)}}
		return nil
	}
	limit := r.cfg.CandidateLimit
	if limit <= 0 {
		limit = 200
	}
	if len(eligible) > limit {
		r.lastErrors = []Issue{{mod.Slug, fmt.Sprintf("%d candidate files exceed the %d-file safety limit. Pin a file or raise -candidate-limit.", len(eligible), limit)}}
		return nil
	}
	sort.Slice(eligible, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339, eligible[i].FileDate)
		right, _ := time.Parse(time.RFC3339, eligible[j].FileDate)
		if left.Equal(right) {
			return eligible[i].ID > eligible[j].ID
		}
		return left.After(right)
	})
	type outcome struct {
		selection selected
		error     error
	}
	results := make([]outcome, len(eligible))
	jobs := make(chan int)
	for i := range results {
		results[i].error = context.Canceled
	}
	workers := r.cfg.Workers
	if workers <= 0 {
		workers = 4
	}
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if r.cfg.Progress != nil {
					r.cfg.Progress(fmt.Sprintf("Inspecting %s: candidate %d/%d", mod.Slug, index+1, len(eligible)))
				}
				candidate := selected{mod: mod, file: eligible[index], requested: r.roots[id]}
				entry, err := r.fetch(r.ctx, candidate)
				if err == nil {
					kind := metadataPrerelease(entry.Metadata)
					if pin == 0 && (r.cfg.Channel == "release" && kind != "" || r.cfg.Channel == "beta" && kind == "alpha") {
						err = fmt.Errorf("declared %s version is excluded by the release policy", kind)
					}
				}
				if err == nil {
					candidate.prepared = &entry
					report := validateCompatibility([]Entry{entry}, r.cfg, nil, true)
					if len(report.Errors) > 0 {
						err = fmt.Errorf("%s", report.Errors[0].Error)
					}
				}
				results[index] = outcome{candidate, err}
			}
		}()
	}
	for index := range eligible {
		if r.ctx.Err() != nil {
			break
		}
		select {
		case jobs <- index:
		case <-r.ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	var candidates []selected
	rejected := 0
	var lastError error
	for _, result := range results {
		if result.error != nil {
			rejected++
			lastError = result.error
		} else {
			candidates = append(candidates, result.selection)
		}
	}
	if len(candidates) == 0 {
		message := "No candidate passes JAR metadata and Forge version validation."
		if lastError != nil {
			message += " " + lastError.Error()
		}
		r.lastErrors = []Issue{{mod.Slug, message}}
		return nil
	}
	if rejected > 0 {
		r.warnings = append(r.warnings, Issue{mod.Slug, fmt.Sprintf("%d candidate(s) rejected by download, integrity, or runtime requirements.", rejected)})
	}
	primary := ""
	for _, identity := range candidates[0].prepared.Metadata.Mods {
		if identity.Bundled {
			continue
		}
		all := true
		for _, candidate := range candidates {
			if candidateVersion(candidate, identity.ID) == "" {
				all = false
				break
			}
		}
		if all {
			primary = identity.ID
			break
		}
	}
	rankable := primary != ""
	if rankable {
		for _, candidate := range candidates {
			if _, err := compareModVersions(candidateVersion(candidate, primary), candidateVersion(candidate, primary)); err != nil {
				rankable = false
			}
		}
	}
	if rankable {
		sort.SliceStable(candidates, func(i, j int) bool {
			order, _ := compareModVersions(candidateVersion(candidates[i], primary), candidateVersion(candidates[j], primary))
			return order > 0
		})
	} else {
		r.warnings = append(r.warnings, Issue{mod.Slug, "No common comparable mod version across this project's files; publication date is used. Pin a file to control content-family changes."})
	}
	r.catalog[id] = candidates
	return candidates
}

func candidateVersion(candidate selected, id string) string {
	for _, mod := range candidate.prepared.Metadata.Mods {
		if mod.ID == id {
			return mod.Version
		}
	}
	return ""
}

func missingRequested(slugs []string, entries []Entry, issues []Issue) []Entry {
	found := make(map[string]bool)
	for _, entry := range entries {
		if entry.Requested {
			found[entry.Slug] = true
		}
	}
	var missing []Entry
	for _, slug := range slugs {
		if !found[slug] {
			message := "Requested project was not resolved; no partial update will be published."
			for _, issue := range issues {
				if issue.Project == slug {
					message = issue.Error
					break
				}
			}
			missing = append(missing, Entry{Slug: slug, Name: slug, ProjectURL: projectURL(slug), Requested: true, Status: "failed", Error: message})
		}
	}
	return missing
}
