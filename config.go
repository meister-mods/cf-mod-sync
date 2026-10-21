package main

import (
	"bufio"
	"errors"
	"fmt"
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

func loadModSlugs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	var slugs []string
	seen := make(map[string]bool)
	for n := 1; sc.Scan(); n++ {
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		slug, err := parseProjectURL(s)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", filepath.Base(path), n, err)
		}
		if !seen[slug] {
			slugs = append(slugs, slug)
			seen[slug] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(slugs) == 0 {
		return nil, fmt.Errorf("empty project URL list: %s", path)
	}
	return slugs, nil
}
