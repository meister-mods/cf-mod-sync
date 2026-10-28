package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := loadDotEnv(".env"); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return runServer(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "audit" {
		return runAudit(os.Args[2:])
	}
	cfg := Options{}
	flag.StringVar(&cfg.ForgeVersion, "forge-version", envDefault("FORGE_VERSION", ""), "installed Forge version, for example 47.3.0")
	flag.StringVar(&cfg.Baseline, "baseline", "", "existing mods folder to audit for removals and downgrades")
	flag.BoolVar(&cfg.AllowReviewedChanges, "allow-reviewed-changes", false, "allow baseline changes after reviewing the compatibility report")
	flag.IntVar(&cfg.CandidateLimit, "candidate-limit", 200, "maximum candidate JARs inspected per project (1-1000)")
	flag.StringVar(&cfg.Version, "version", envDefault("MC_VERSION", "1.20.1"), "Minecraft version")
	flag.StringVar(&cfg.Channel, "channel", envDefault("RELEASE_CHANNEL", "release"), "release, beta, or all (includes alpha)")
	flag.StringVar(&cfg.Input, "input", envDefault("MOD_LIST", "../client/.mods"), "project URLs or pinned /files/<id> URLs")
	flag.StringVar(&cfg.Output, "output", envDefault("OUTPUT_DIR", ""), "directory for downloaded JARs (default: downloads/<version>-forge)")
	flag.BoolVar(&cfg.Deps, "deps", envBool("AUTO_DEPS", true), "automatically include required dependencies")
	flag.BoolVar(&cfg.Force, "force", false, "re-download / safely replace existing filenames")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "show selections without downloading")
	flag.BoolVar(&cfg.Prune, "prune", false, "after a fully successful run remove previous manifest-managed obsolete JARs")
	verifyOnly := flag.Bool("verify-only", false, "offline re-verify manifest-tracked JAR files (no API key needed)")
	flag.IntVar(&cfg.Workers, "workers", 4, "parallel downloads")
	maxMB := flag.Int("max-file-mb", defaultMaxFileMB, "maximum allowed JAR file size in MiB")
	flag.Parse()
	if cfg.ForgeVersion != "" && !forgeVersionPattern.MatchString(cfg.ForgeVersion) {
		return fmt.Errorf("invalid Forge version: %q", cfg.ForgeVersion)
	}
	if cfg.CandidateLimit < 1 || cfg.CandidateLimit > 1000 {
		return fmt.Errorf("candidate-limit must be between 1 and 1000")
	}
	if cfg.Output == "" {
		cfg.Output = filepath.Join("downloads", cfg.Version+"-forge")
	}
	if len(flag.Args()) != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flag.Args(), " "))
	}
	if !mcVersionPattern.MatchString(cfg.Version) {
		return fmt.Errorf("invalid Minecraft version: %q", cfg.Version)
	}
	if cfg.Channel != "release" && cfg.Channel != "beta" && cfg.Channel != "all" {
		return fmt.Errorf("invalid release channel: %q", cfg.Channel)
	}
	if cfg.Workers < 1 || cfg.Workers > 16 {
		return fmt.Errorf("workers must be between 1 and 16")
	}
	if *maxMB < 1 || *maxMB > 4096 {
		return fmt.Errorf("max-file-mb must be between 1 and 4096")
	}
	cfg.MaxBytes = int64(*maxMB) << 20
	if cfg.DryRun && cfg.Prune {
		return fmt.Errorf("-dry-run and -prune cannot be used together")
	}
	if cfg.Prune && cfg.Force {
		return fmt.Errorf("-prune and -force cannot be used together")
	}
	if cfg.Output == "." || filepath.Clean(cfg.Output) == string(filepath.Separator) {
		return fmt.Errorf("refusing a working directory or filesystem root as output")
	}

	manifestPath := filepath.Join(cfg.Output, "manifest.json")
	if *verifyOnly {
		m, err := readManifest(manifestPath)
		if err != nil {
			return err
		}
		if cfg.ForgeVersion != "" {
			m.ForgeVersion = cfg.ForgeVersion
		}
		issues := verifyManifest(cfg.Output, m, cfg.MaxBytes)
		if len(issues) > 0 {
			return fmt.Errorf("%d verification failure(s)", len(issues))
		}
		return nil
	}
	key := strings.TrimSpace(os.Getenv("CURSEFORGE_API_KEY"))
	if key == "" || key == "YOUR_CURSEFORGE_API_KEY" {
		return fmt.Errorf("CURSEFORGE_API_KEY is required (official CurseForge API); see README.md")
	}
	slugs, pins, err := loadProjectList(cfg.Input)
	if err != nil {
		return err
	}
	cfg.PinnedFiles = pins
	var previous Manifest
	if cfg.Prune {
		previous, err = readManifest(manifestPath)
		if err != nil {
			return fmt.Errorf("-prune requires an existing manifest: %w", err)
		}
	}
	fmt.Printf("Minecraft %s / Forge | %d projects | channel=%s | deps=%v | Go %s\n", cfg.Version, len(slugs), cfg.Channel, cfg.Deps, runtime.Version())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := newClient(key)
	m := runDownload(ctx, client, httpDownloadClient(key), slugs, cfg)
	selected := "manifest.json"
	if cfg.DryRun {
		selected = "plan.json"
	} else if len(m.Errors) > 0 {
		selected = "failed-run-" + time.Now().UTC().Format("20060102T150405.000000000") + ".json"
	}
	savedPath := filepath.Join(cfg.Output, selected)
	if err := writeManifest(savedPath, m); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d selected, %d issues)\n", savedPath, len(m.Entries), len(m.Errors))
	if len(m.Errors) > 0 {
		return fmt.Errorf("%d project(s) failed; inspect manifest errors", len(m.Errors))
	}
	if cfg.Prune {
		if err := pruneOld(cfg.Output, previous, m, cfg.MaxBytes); err != nil {
			return err
		}
	}
	return nil
}
