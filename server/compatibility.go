package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	maven "github.com/masahiro331/go-mvn-version"
	"github.com/pelletier/go-toml/v2"
)

const metadataLimit = 64 << 10

var filenameTokens = regexp.MustCompile("[^a-z0-9]+")

func filenameFamily(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	var stable []string
	for _, token := range filenameTokens.Split(name, -1) {
		if token == "" || (token[0] >= '0' && token[0] <= '9') || (len(token) > 1 && token[0] == 'v' && token[1] >= '0' && token[1] <= '9') || (strings.HasPrefix(token, "mc") && len(token) > 2 && token[2] >= '0' && token[2] <= '9') {
			continue
		}
		switch token {
		case "minecraft", "forge", "fabric", "neoforge", "alpha", "beta", "snapshot", "release", "final", "universal", "hotfix":
			continue
		}
		stable = append(stable, token)
	}
	return strings.Join(stable, "-")
}

var forgeModID = regexp.MustCompile("^[a-z][a-z0-9_]{1,63}$")

var prereleaseVersion = regexp.MustCompile(`(?i)(?:^|[0-9._+-])(alpha|beta|snapshot|rc|cr|milestone|preview|pre[._-]?release|pre|dev|nightly|canary|experimental|ea|eap)(?:$|[0-9._+-])`)

func prereleaseReason(releaseType, filename string, metadata *JarMetadata) string {
	if releaseType != "" && releaseType != "release" {
		return "API release type is " + releaseType
	}
	if kind := metadataPrerelease(metadata); kind != "" {
		return "JAR declares a " + kind + " version"
	}
	if match := prereleaseVersion.FindStringSubmatch(filename); len(match) > 1 {
		return "filename declares a " + strings.ToLower(match[1]) + " version"
	}
	return ""
}

func metadataPrerelease(metadata *JarMetadata) string {
	if metadata == nil {
		return ""
	}
	for _, mod := range metadata.Mods {
		if !mod.Bundled {
			if match := prereleaseVersion.FindStringSubmatch(mod.Version); len(match) > 1 {
				return strings.ToLower(match[1])
			}
		}
	}
	return ""
}

type ModIdentity struct {
	Bundle  string `json:"bundle,omitempty"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Bundled bool   `json:"bundled,omitempty"`
}
type ModRequirement struct {
	Bundle    string `json:"bundle,omitempty"`
	Owner     string `json:"owner"`
	ID        string `json:"id"`
	Range     string `json:"range"`
	Mandatory bool   `json:"mandatory"`
	Side      string `json:"side"`
}
type JarMetadata struct {
	Bundles      []BundledArtifact `json:"bundles,omitempty"`
	Mods         []ModIdentity     `json:"mods"`
	Requirements []ModRequirement  `json:"requirements,omitempty"`
	LoaderRange  string            `json:"loader_range,omitempty"`
	Loader       string            `json:"loader,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}
type BundledArtifact struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Range   string `json:"range"`
	Parent  string `json:"parent,omitempty"`
}

// InstalledFile carries metadata only. The API never accepts a client path.
type InstalledFile struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	ModsTOML string `json:"mods_toml"`
	Manifest string `json:"manifest"`
}
type ModChange struct {
	Kind       string `json:"kind"`
	ModID      string `json:"mod_id,omitempty"`
	Previous   string `json:"previous,omitempty"`
	Current    string `json:"current,omitempty"`
	OldVersion string `json:"old_version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
}
type CompatibilityReport struct {
	State          string      `json:"state"`
	CheckedFiles   int         `json:"checked_files"`
	ForgeVersion   string      `json:"forge_version,omitempty"`
	Errors         []Issue     `json:"errors"`
	Warnings       []Issue     `json:"warnings"`
	Changes        []ModChange `json:"changes"`
	RequiresReview bool        `json:"requires_review"`
}

func compareModVersions(a, b string) (int, error) {
	for _, v := range []string{a, b} {
		if v == "" || len(v) > 256 || strings.ContainsAny(v, " \t\r\n${}[]()") {
			return 0, fmt.Errorf("unresolved or invalid mod version %q", v)
		}
	}
	left, err := maven.NewVersion(a)
	if err != nil {
		return 0, err
	}
	right, err := maven.NewVersion(b)
	if err != nil {
		return 0, err
	}
	return left.Compare(right), nil
}

func versionInRange(version, spec string) (bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" {
		return true, nil
	}
	if spec[0] != '[' && spec[0] != '(' {
		// Forge/Maven plain versions are soft recommendations, not bounds.
		if strings.ContainsAny(spec, "[]()") {
			return false, errors.New("invalid version range")
		}
		_, err := compareModVersions(spec, spec)
		return err == nil, err
	}
	if len(spec) > 1024 {
		return false, errors.New("version range is too long")
	}
	matched := false
	for spec != "" {
		if spec[0] != '[' && spec[0] != '(' {
			return false, errors.New("expected a bracketed Maven version range")
		}
		end := strings.IndexAny(spec[1:], "])")
		if end < 0 {
			return false, errors.New("unterminated version range")
		}
		end++
		part, rest := spec[:end+1], strings.TrimSpace(spec[end+1:])
		bounds := strings.SplitN(part[1:len(part)-1], ",", 2)
		hit := true
		if len(bounds) == 1 {
			if part[0] != '[' || part[len(part)-1] != ']' || strings.TrimSpace(bounds[0]) == "" {
				return false, errors.New("invalid exact version range")
			}
			cmp, err := compareModVersions(version, strings.TrimSpace(bounds[0]))
			if err != nil {
				return false, err
			}
			hit = cmp == 0
		} else if len(bounds) == 2 {
			lower, upper := strings.TrimSpace(bounds[0]), strings.TrimSpace(bounds[1])
			if lower != "" && upper != "" {
				order, err := compareModVersions(lower, upper)
				if err != nil || order > 0 || (order == 0 && (part[0] != '[' || part[len(part)-1] != ']')) {
					return false, errors.New("empty or reversed version range")
				}
			}
			if lower != "" {
				cmp, err := compareModVersions(version, lower)
				if err != nil {
					return false, err
				}
				hit = hit && (cmp > 0 || (cmp == 0 && part[0] == '['))
			}
			if upper != "" {
				cmp, err := compareModVersions(version, upper)
				if err != nil {
					return false, err
				}
				hit = hit && (cmp < 0 || (cmp == 0 && part[len(part)-1] == ']'))
			}
		} else {
			return false, errors.New("invalid version range")
		}
		matched = matched || hit
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ",") {
			return false, errors.New("invalid version range union")
		}
		spec = strings.TrimSpace(rest[1:])
		if spec == "" {
			return false, errors.New("trailing version range separator")
		}
	}
	return matched, nil
}

func manifestProperties(raw string) map[string]string {
	result := make(map[string]string)
	var previous string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line == "" {
			break
		}
		if strings.HasPrefix(line, " ") && previous != "" {
			result[previous] += line[1:]
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if ok {
			previous = strings.ToLower(key)
			result[previous] = value
		}
	}
	return result
}

func parseForgeMetadata(raw, manifest string) (*JarMetadata, error) {
	result := &JarMetadata{Mods: []ModIdentity{}}
	if raw == "" {
		result.Warnings = []string{"No META-INF/mods.toml; content-pack compatibility cannot be inferred."}
		return result, nil
	}
	if len(raw) > metadataLimit || len(manifest) > metadataLimit {
		return nil, errors.New("mod metadata exceeds the size limit")
	}
	var document struct {
		Loader      string `toml:"modLoader"`
		LoaderRange string `toml:"loaderVersion"`
		Mods        []struct {
			ID      string `toml:"modId"`
			Version string `toml:"version"`
		} `toml:"mods"`
		Dependencies map[string][]struct {
			ID        string `toml:"modId"`
			Mandatory *bool  `toml:"mandatory"`
			Range     string `toml:"versionRange"`
			Side      string `toml:"side"`
		} `toml:"dependencies"`
		Properties map[string]any `toml:"properties"`
	}
	if err := toml.Unmarshal([]byte(raw), &document); err != nil {
		return nil, fmt.Errorf("invalid mods.toml: %w", err)
	}
	jarVersion := manifestProperties(manifest)["implementation-version"]
	result.LoaderRange = document.LoaderRange
	result.Loader = document.Loader
	if document.Loader != "" && document.Loader != "javafml" && document.Loader != "lowcodefml" {
		result.Warnings = append(result.Warnings, "Custom language loader "+document.Loader+" "+document.LoaderRange+" cannot be checked from the Forge build alone.")
	}
	seen := make(map[string]bool)
	for _, mod := range document.Mods {
		if !forgeModID.MatchString(mod.ID) || seen[mod.ID] {
			return nil, errors.New("empty or duplicate mod ID in mods.toml")
		}
		seen[mod.ID] = true
		version := mod.Version
		if version == "" {
			version = "1"
		}
		if jarVersion != "" {
			version = strings.ReplaceAll(version, "${file.jarVersion}", jarVersion)
		}
		for name, value := range document.Properties {
			version = strings.ReplaceAll(version, "${file."+name+"}", fmt.Sprint(value))
		}
		if version == "" || len(version) > 256 || version[0] < '0' || version[0] > '9' || strings.ContainsAny(version, "\r\n\t") {
			return nil, fmt.Errorf("invalid Forge mod version for %s", mod.ID)
		}
		result.Mods = append(result.Mods, ModIdentity{ID: mod.ID, Version: version})
	}
	for owner, dependencies := range document.Dependencies {
		for _, dependency := range dependencies {
			if !seen[owner] || dependency.ID == "" || dependency.Mandatory == nil {
				return nil, errors.New("dependency has no declared owner, mod ID, or mandatory flag")
			}
			side := strings.ToUpper(dependency.Side)
			if side == "" {
				side = "BOTH"
			}
			if side != "BOTH" && side != "CLIENT" && side != "SERVER" {
				return nil, errors.New("invalid dependency side")
			}
			result.Requirements = append(result.Requirements, ModRequirement{Owner: owner, ID: dependency.ID, Range: dependency.Range, Mandatory: *dependency.Mandatory, Side: side})
		}
	}
	sort.Slice(result.Requirements, func(i, j int) bool {
		a, b := result.Requirements[i], result.Requirements[j]
		return a.Owner+"\x00"+a.ID < b.Owner+"\x00"+b.ID
	})
	if len(result.Mods) == 0 {
		result.Warnings = append(result.Warnings, "No Forge mod declarations; version compatibility is unverified.")
	}
	return result, nil
}

func readZipText(reader *zip.Reader, name string) (string, error) {
	file, err := reader.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, metadataLimit+1))
	if err != nil {
		return "", err
	}
	if name == "META-INF/MANIFEST.MF" {
		main := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if end := strings.Index(main, "\n\n"); end >= 0 {
			return main[:end+2], nil
		}
	}
	if len(raw) > metadataLimit {
		return "", fmt.Errorf("%s exceeds the metadata size limit", name)
	}
	return string(raw), nil
}

func inspectJarMetadata(path string) (*JarMetadata, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	budget := int64(64 << 20)
	return inspectZipMetadata(&reader.Reader, 0, &budget)
}

func inspectZipMetadata(reader *zip.Reader, depth int, budget *int64) (*JarMetadata, error) {
	raw, err := readZipText(reader, "META-INF/mods.toml")
	if err != nil {
		return nil, err
	}
	manifest, err := readZipText(reader, "META-INF/MANIFEST.MF")
	if err != nil {
		return nil, err
	}
	result, err := parseForgeMetadata(raw, manifest)
	if err != nil {
		return nil, err
	}
	jarjar, err := readZipText(reader, "META-INF/jarjar/metadata.json")
	if err != nil {
		return nil, err
	}
	if jarjar == "" {
		return result, nil
	}
	var embedded struct {
		Jars []struct {
			Identifier struct {
				Group    string `json:"group"`
				Artifact string `json:"artifact"`
			} `json:"identifier"`
			Version struct {
				Range           string `json:"range"`
				ArtifactVersion string `json:"artifactVersion"`
			} `json:"version"`
			Path string `json:"path"`
		} `json:"jars"`
	}
	if err := json.Unmarshal([]byte(jarjar), &embedded); err != nil {
		return nil, fmt.Errorf("invalid JarJar metadata: %w", err)
	}
	if len(embedded.Jars) > 128 || depth >= 3 {
		return nil, errors.New("embedded JAR safety limit reached")
	}
	for _, jar := range embedded.Jars {
		key := jar.Identifier.Group + ":" + jar.Identifier.Artifact
		if jar.Identifier.Group == "" || jar.Identifier.Artifact == "" || jar.Version.ArtifactVersion == "" {
			return nil, errors.New("incomplete JarJar artifact metadata")
		}
		token := key + "@" + jar.Version.ArtifactVersion
		result.Bundles = append(result.Bundles, BundledArtifact{Key: key, Version: jar.Version.ArtifactVersion, Range: jar.Version.Range})
		file, err := reader.Open(jar.Path)
		if err != nil {
			return nil, fmt.Errorf("embedded JAR missing: %s", jar.Path)
		}
		data, err := io.ReadAll(io.LimitReader(file, *budget+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		*budget -= int64(len(data))
		if *budget < 0 {
			return nil, errors.New("embedded JARs exceed 64 MiB metadata inspection budget")
		}
		nested, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		metadata, err := inspectZipMetadata(nested, depth+1, budget)
		if err != nil {
			return nil, err
		}
		for _, mod := range metadata.Mods {
			if !mod.Bundled {
				mod.Bundle = token
			}
			mod.Bundled = true
			result.Mods = append(result.Mods, mod)
		}
		for _, required := range metadata.Requirements {
			if required.Bundle == "" {
				required.Bundle = token
			}
			result.Requirements = append(result.Requirements, required)
		}
		for _, artifact := range metadata.Bundles {
			if artifact.Parent == "" {
				artifact.Parent = token
			}
			result.Bundles = append(result.Bundles, artifact)
		}
		if metadata.LoaderRange != "" && (metadata.Loader == "javafml" || metadata.Loader == "lowcodefml") {
			result.Requirements = append(result.Requirements, ModRequirement{Bundle: token, Owner: jar.Path, ID: "fml", Range: metadata.LoaderRange, Mandatory: true, Side: "BOTH"})
		}
		for _, warning := range metadata.Warnings {
			// Plain embedded libraries normally have no Forge mod declarations.
			if strings.HasPrefix(warning, "Custom language loader ") {
				result.Warnings = append(result.Warnings, jar.Path+": "+warning)
			}
		}
	}
	return result, nil
}

func loadInstalledFiles(dir string, maxBytes int64) ([]InstalledFile, error) {
	if dir == "" {
		return nil, nil
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []InstalledFile
	for _, item := range items {
		if item.IsDir() || !strings.EqualFold(filepath.Ext(item.Name()), ".jar") {
			continue
		}
		if len(files) >= 500 {
			return nil, errors.New("baseline exceeds 500 JARs")
		}
		path := filepath.Join(dir, item.Name())
		stat, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !stat.Mode().IsRegular() || stat.Size() > maxBytes {
			return nil, fmt.Errorf("invalid baseline file: %s", item.Name())
		}
		reader, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		raw, err1 := readZipText(&reader.Reader, "META-INF/mods.toml")
		manifest, err2 := readZipText(&reader.Reader, "META-INF/MANIFEST.MF")
		reader.Close()
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("cannot inspect baseline JAR %s", item.Name())
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil {
			return nil, err
		}
		files = append(files, InstalledFile{item.Name(), hex.EncodeToString(hash.Sum(nil)), raw, manifest})
	}
	if len(files) == 0 {
		return nil, errors.New("baseline folder contains no JARs")
	}
	return files, nil
}

func validateCompatibility(entries []Entry, cfg Options, baseline []InstalledFile, partial bool) CompatibilityReport {
	report := CompatibilityReport{State: "passed", ForgeVersion: cfg.ForgeVersion, Errors: []Issue{}, Warnings: []Issue{}, Changes: []ModChange{}}
	providers := make(map[string]ModIdentity)
	owners := make(map[string]string)
	bundleTokens, bundleErrors := selectBundleTokens(entries, partial)
	report.Errors = append(report.Errors, bundleErrors...)
	declaredRequired := make(map[string]bool)
	for _, entry := range entries {
		if entry.Metadata != nil {
			for _, required := range entry.Metadata.Requirements {
				if required.Mandatory {
					declaredRequired[required.ID] = true
				}
			}
		}
	}
	for _, entry := range entries {
		if entry.Metadata == nil {
			continue
		}
		report.CheckedFiles++
		if cfg.ExcludePrereleases {
			if reason := prereleaseReason(entry.ReleaseType, entry.Filename, entry.Metadata); reason != "" {
				report.Errors = append(report.Errors, Issue{entry.Slug, "Prerelease excluded: " + reason + ". Disable Exclude prereleases to allow this file."})
			}
		}
		if kind := metadataPrerelease(entry.Metadata); kind != "" {
			report.Warnings = append(report.Warnings, Issue{entry.Slug, "Declared " + kind + " mod version; this is a prerelease build."})
		}
		if entry.ReleaseType != "" && entry.ReleaseType != "release" {
			report.Warnings = append(report.Warnings, Issue{entry.Slug, "Selected " + entry.ReleaseType + " build under the explicit release policy or file pin."})
		}
		for _, warning := range entry.Metadata.Warnings {
			report.Warnings = append(report.Warnings, Issue{entry.Slug, warning})
		}
		if !entry.Requested && len(entry.RequiredBy) > 0 {
			referenced := false
			for _, mod := range entry.Metadata.Mods {
				referenced = referenced || declaredRequired[mod.ID]
			}
			if !referenced {
				report.Warnings = append(report.Warnings, Issue{entry.Slug, "Added by CurseForge required-dependency metadata, without a matching mandatory mods.toml reference. Required by: " + strings.Join(entry.RequiredBy, ", ")})
			}
		}
		for _, mod := range entry.Metadata.Mods {
			if mod.Bundle != "" && !bundleTokens[mod.Bundle] {
				continue
			}
			if old, ok := providers[mod.ID]; ok {
				if !old.Bundled && mod.Bundled {
					continue
				}
				if old.Bundled && !mod.Bundled {
					providers[mod.ID], owners[mod.ID] = mod, entry.Filename
					continue
				}
				if old.Bundled && mod.Bundled && old.Version == mod.Version {
					continue
				}
				report.Errors = append(report.Errors, Issue{entry.Slug, fmt.Sprintf("duplicate mod ID %s in %s and %s", mod.ID, owners[mod.ID], entry.Filename)})
			}
			providers[mod.ID], owners[mod.ID] = mod, entry.Filename
		}
	}
	side := cfg.TargetSide
	if side == "" {
		side = "CLIENT"
	}
	for _, entry := range entries {
		if entry.Metadata == nil {
			continue
		}
		requirements := append([]ModRequirement{}, entry.Metadata.Requirements...)
		if entry.Metadata.LoaderRange != "" && (entry.Metadata.Loader == "javafml" || entry.Metadata.Loader == "lowcodefml") {
			requirements = append(requirements, ModRequirement{Owner: entry.Slug, ID: "fml", Range: entry.Metadata.LoaderRange, Mandatory: true, Side: "BOTH"})
		}
		for _, required := range requirements {
			if required.Bundle != "" && (partial || !bundleTokens[required.Bundle]) {
				continue
			}
			if required.Side != "BOTH" && required.Side != side {
				continue
			}
			version, present := "", false
			switch required.ID {
			case "minecraft":
				version, present = cfg.Version, true
			case "forge", "fml":
				version, present = cfg.ForgeVersion, cfg.ForgeVersion != ""
				if !present {
					report.Warnings = append(report.Warnings, Issue{entry.Slug, "Forge version was not provided; loader requirements were not checked."})
					continue
				}
				if required.ID == "fml" {
					// Forge's built-in language loaders use the major version.
					version = strings.SplitN(version, ".", 2)[0]
				}
			default:
				value, ok := providers[required.ID]
				if partial && value.Bundled {
					continue
				}
				version, present = value.Version, ok
			}
			if !present {
				if required.Mandatory && !partial {
					report.Errors = append(report.Errors, Issue{entry.Slug, fmt.Sprintf("%s requires missing mod %s %s", required.Owner, required.ID, required.Range)})
				}
				continue
			}
			matches, err := versionInRange(version, required.Range)
			if err != nil || !matches {
				detail := fmt.Sprintf("%s requires %s %s; selected version is %s", required.Owner, required.ID, required.Range, version)
				if err != nil {
					detail += " (cannot verify range: " + err.Error() + ")"
				}
				report.Errors = append(report.Errors, Issue{entry.Slug, detail})
			}
		}
	}
	if !partial {
		compareBaseline(&report, entries, baseline, providers, owners)
	}
	if len(report.Errors) > 0 {
		report.State = "failed"
	} else if report.RequiresReview && !cfg.AllowReviewedChanges {
		report.State = "review_required"
	} else if len(report.Warnings) > 0 {
		report.State = "warnings"
	}
	return report
}

func selectBundleTokens(entries []Entry, partial bool) (map[string]bool, []Issue) {
	var all []BundledArtifact
	for _, entry := range entries {
		if entry.Metadata != nil {
			all = append(all, entry.Metadata.Bundles...)
		}
	}
	selected := make(map[string]bool)
	for iteration := 0; iteration < 128; iteration++ {
		groups := make(map[string][]BundledArtifact)
		for _, artifact := range all {
			if artifact.Parent == "" || selected[artifact.Parent] {
				groups[artifact.Key] = append(groups[artifact.Key], artifact)
			}
		}
		next := make(map[string]bool)
		var issues []Issue
		for key, artifacts := range groups {
			best := ""
			for _, candidate := range artifacts {
				valid := true
				for _, requirement := range artifacts {
					matches, err := versionInRange(candidate.Version, requirement.Range)
					if err != nil || !matches {
						valid = false
						break
					}
				}
				if valid {
					if best == "" {
						best = candidate.Version
					} else if order, err := compareModVersions(candidate.Version, best); err == nil && order > 0 {
						best = candidate.Version
					}
				}
			}
			if best == "" {
				if !partial {
					issues = append(issues, Issue{key, "No bundled JarJar artifact version satisfies all requested ranges."})
				}
			} else {
				next[key+"@"+best] = true
			}
		}
		stable := len(next) == len(selected)
		if stable {
			for key := range next {
				if !selected[key] {
					stable = false
					break
				}
			}
		}
		if stable {
			sort.Slice(issues, func(i, j int) bool { return issues[i].Project < issues[j].Project })
			return next, issues
		}
		selected = next
	}
	if partial {
		return selected, nil
	}
	return selected, []Issue{{"JarJar", "Nested artifact selection did not converge; pin a simpler compatible combination."}}
}

func compareBaseline(report *CompatibilityReport, entries []Entry, baseline []InstalledFile, providers map[string]ModIdentity, owners map[string]string) {
	hashes := make(map[string]bool)
	for _, entry := range entries {
		if entry.Verified != nil {
			hashes[entry.Verified.SHA256] = true
		}
	}
	previousIDs := make(map[string]bool)
	for _, file := range baseline {
		metadata, err := parseForgeMetadata(file.ModsTOML, file.Manifest)
		if err != nil {
			report.Errors = append(report.Errors, Issue{file.Filename, "invalid baseline metadata: " + err.Error()})
			continue
		}
		if len(metadata.Mods) == 0 && !hashes[strings.ToLower(file.SHA256)] {
			report.RequiresReview = true
			report.Changes = append(report.Changes, ModChange{Kind: "unidentified_missing", Previous: file.Filename})
			report.Warnings = append(report.Warnings, Issue{file.Filename, "Baseline content pack is not present by SHA-256 and has no mod ID; review its replacement or pin its original CurseForge file."})
		}
		for _, old := range metadata.Mods {
			previousIDs[old.ID] = true
			current, exists := providers[old.ID]
			change := ModChange{ModID: old.ID, Previous: file.Filename, Current: owners[old.ID], OldVersion: old.Version, NewVersion: current.Version}
			if !exists {
				change.Kind = "removed"
				report.RequiresReview = true
				report.Warnings = append(report.Warnings, Issue{file.Filename, "Baseline mod ID " + old.ID + " is missing from the new selection."})
			} else {
				order, err := compareModVersions(current.Version, old.Version)
				switch {
				case err != nil:
					change.Kind = "unverified"
					report.RequiresReview = true
				case order < 0:
					change.Kind = "downgrade"
					report.RequiresReview = true
					report.Warnings = append(report.Warnings, Issue{file.Filename, fmt.Sprintf("%s would downgrade from %s to %s.", old.ID, old.Version, current.Version)})
				case order > 0:
					change.Kind = "updated"
					if filenameFamily(file.Filename) != filenameFamily(owners[old.ID]) {
						change.Kind = "renamed"
						report.RequiresReview = true
						report.Warnings = append(report.Warnings, Issue{file.Filename, "Filename family changed despite a matching mod ID. Review content replacements and world compatibility."})
					}
				case hashes[strings.ToLower(file.SHA256)]:
					change.Kind = "unchanged"
				default:
					change.Kind = "replaced"
					report.RequiresReview = true
					report.Warnings = append(report.Warnings, Issue{file.Filename, "Same declared mod version has different bytes; review the replacement."})
				}
			}
			report.Changes = append(report.Changes, change)
		}
	}
	if len(baseline) > 0 {
		for id, mod := range providers {
			if !mod.Bundled && !previousIDs[id] {
				report.Changes = append(report.Changes, ModChange{Kind: "added", ModID: id, Current: owners[id], NewVersion: mod.Version})
			}
		}
	}
	sort.Slice(report.Changes, func(i, j int) bool {
		return report.Changes[i].ModID+report.Changes[i].Previous < report.Changes[j].ModID+report.Changes[j].Previous
	})
}
