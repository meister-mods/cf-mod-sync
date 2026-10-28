package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// runAudit inspects existing files without an API key or any network traffic.
func runAudit(args []string) error {
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	cfg := Options{}
	folder := flags.String("mods", "", "folder containing actual JAR files")
	output := flags.String("report", "reports/audit.json", "JSON report destination")
	flags.StringVar(&cfg.Baseline, "baseline", "", "optional previous JAR folder")
	flags.StringVar(&cfg.Version, "version", "1.20.1", "Minecraft version")
	flags.StringVar(&cfg.ForgeVersion, "forge-version", "", "installed Forge version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *folder == "" {
		return fmt.Errorf("usage: audit -mods <JAR folder> -forge-version <version> -report <path>")
	}
	if !mcVersionPattern.MatchString(cfg.Version) || (cfg.ForgeVersion != "" && !forgeVersionPattern.MatchString(cfg.ForgeVersion)) {
		return fmt.Errorf("invalid Minecraft or Forge version")
	}
	cfg.MaxBytes = int64(defaultMaxFileMB) << 20
	baseline, err := loadInstalledFiles(cfg.Baseline, cfg.MaxBytes)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(*folder)
	if err != nil {
		return err
	}
	manifest := newManifest(cfg)
	for _, file := range files {
		if file.IsDir() || !strings.EqualFold(filepath.Ext(file.Name()), ".jar") {
			continue
		}
		if len(manifest.Entries) >= 500 {
			return fmt.Errorf("audit exceeds 500 JARs")
		}
		entry := Entry{Name: file.Name(), Slug: file.Name(), Filename: file.Name(), Requested: true, Status: "audited"}
		path := filepath.Join(*folder, file.Name())
		stat, err := os.Lstat(path)
		if err == nil && (!stat.Mode().IsRegular() || stat.Size() > cfg.MaxBytes) {
			err = fmt.Errorf("file is not regular or exceeds the size limit")
		}
		if err == nil {
			err = checkZipCRC(path, cfg.MaxBytes)
		}
		if err == nil {
			entry.Metadata, err = inspectJarMetadata(path)
		}
		if err == nil {
			source, openErr := os.Open(path)
			err = openErr
			if err == nil {
				hash := sha256.New()
				size, copyErr := io.Copy(hash, source)
				source.Close()
				err = copyErr
				if err == nil {
					entry.Verified = &Verified{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil)), Integrity: "local SHA-256 + ZIP/CRC; no API checksum available"}
				}
			}
		}
		if err != nil {
			entry.Status = "failed"
			entry.Error = err.Error()
			manifest.Errors = append(manifest.Errors, Issue{entry.Slug, entry.Error})
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	if len(manifest.Entries) == 0 {
		return fmt.Errorf("folder contains no JARs; a directory listing cannot provide mods.toml")
	}
	report := validateCompatibility(manifest.Entries, cfg, baseline, false)
	manifest.Errors = append(manifest.Errors, report.Errors...)
	manifest.Warnings = report.Warnings
	if len(manifest.Errors) > 0 {
		report.State = "failed"
	}
	manifest.Compatibility = &report
	if err := writeManifest(*output, manifest); err != nil {
		return err
	}
	fmt.Printf("Audited %d JARs | Minecraft %s | Forge %s | errors=%d | warnings=%d | report=%s\n", len(manifest.Entries), cfg.Version, cfg.ForgeVersion, len(manifest.Errors), len(manifest.Warnings), *output)
	if len(manifest.Errors) > 0 || report.RequiresReview {
		return fmt.Errorf("audit requires attention; inspect the JSON report")
	}
	return nil
}
