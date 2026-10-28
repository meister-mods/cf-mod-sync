package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// loadDotEnv is intentionally dependency-free. Existing process variables win.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return b
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var mcVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[-.][0-9A-Za-z]+)*$`)
var forgeVersionPattern = regexp.MustCompile(`^[0-9]{1,4}(?:\.[0-9]{1,6}){0,3}$`)

func loadModSlugs(path string) ([]string, error) {
	slugs, _, err := loadProjectList(path)
	return slugs, err
}

func parseProjectInput(raw string) (string, int64, error) {
	if slug, err := parseProjectURL(raw); err == nil {
		return slug, 0, nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, err
	}
	path := strings.TrimSuffix(u.Path, "/")
	prefix, id, ok := strings.Cut(path, "/files/")
	if !ok || strings.Contains(id, "/") {
		return "", 0, fmt.Errorf("expected a project URL or /files/<file-id> URL")
	}
	u.Path = prefix
	u.RawPath = ""
	slug, err := parseProjectURL(u.String())
	if err != nil {
		return "", 0, err
	}
	fileID, err := positiveID(id)
	if err != nil {
		return "", 0, err
	}
	return slug, fileID, nil
}

func loadProjectList(path string) ([]string, map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	var slugs []string
	seen := make(map[string]bool)
	pins := make(map[string]int64)
	for n := 1; sc.Scan(); n++ {
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		slug, pin, err := parseProjectInput(s)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", filepath.Base(path), n, err)
		}
		if pin > 0 {
			if previous := pins[slug]; previous > 0 && previous != pin {
				return nil, nil, fmt.Errorf("%s:%d: conflicting pinned files for %s", filepath.Base(path), n, slug)
			}
			pins[slug] = pin
		}
		if !seen[slug] {
			slugs = append(slugs, slug)
			seen[slug] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(slugs) == 0 {
		return nil, nil, fmt.Errorf("empty project URL list: %s", path)
	}
	if len(slugs) > 500 {
		return nil, nil, fmt.Errorf("project list exceeds 500 projects")
	}
	return slugs, pins, nil
}
