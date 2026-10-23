package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type cachedSelection struct {
	Mod       Mod  `json:"mod"`
	File      File `json:"file"`
	Requested bool `json:"requested"`
}

type selectionRecord struct {
	CreatedAt time.Time         `json:"created_at"`
	Items     []cachedSelection `json:"items"`
}

type cacheFlight struct {
	done  chan struct{}
	entry Entry
	err   error
}

func (s *modServer) resolveCached(ctx context.Context, slugs []string, options Options, refresh bool) ([]selected, []Issue, bool) {
	select {
	case s.planSlot <- struct{}{}:
		defer func() { <-s.planSlot }()
	case <-ctx.Done():
		return nil, []Issue{{Project: "execution", Error: ctx.Err().Error()}}, false
	}
	sorted := append([]string{}, slugs...)
	sort.Strings(sorted)
	keyData, _ := json.Marshal(struct {
		Slugs   []string
		Version string
		Channel string
		Deps    bool
	}{sorted, options.Version, options.Channel, options.Deps})
	sum := sha256.Sum256(keyData)
	path := filepath.Join(s.cfg.CacheDir, "selections", hex.EncodeToString(sum[:])+".json")
	var saved selectionRecord
	if !refresh && readCacheJSON(path, &saved) == nil && len(saved.Items) > 0 &&
		time.Since(saved.CreatedAt) >= 0 && time.Since(saved.CreatedAt) < s.cfg.MetadataTTL {
		var result []selected
		valid := len(saved.Items) <= 500
		for _, item := range saved.Items {
			if item.Mod.ID <= 0 || item.File.ID <= 0 || !safeJarName(item.File.FileName) ||
				!item.Mod.IsAvailable || !item.Mod.AllowModDistribution ||
				!hasGameVersion(item.File, options.Version) || !loaderCompatible(item.File) ||
				!releaseAllowed(item.File.ReleaseType, options.Channel) {
				valid = false
			}
			result = append(result, selected{mod: item.Mod, file: item.File, requested: item.Requested})
		}
		if valid {
			return result, nil, true
		}
	}
	result, issues := resolveMods(ctx, s.client, slugs, options)
	if len(issues) == 0 && len(result) > 0 {
		record := selectionRecord{CreatedAt: time.Now().UTC()}
		for _, item := range result {
			// Persist metadata only, never signed CDN URLs or API credentials.
			item.file.DownloadURL = ""
			record.Items = append(record.Items, cachedSelection{Mod: item.mod, File: item.file, Requested: item.requested})
		}
		if err := writeCacheJSON(path, record); err != nil {
			issues = append(issues, Issue{Project: "selection cache", Error: err.Error()})
		}
	}
	return result, issues, false
}

func (s *modServer) fileDir(modID, fileID int64) string {
	return filepath.Join(s.cfg.CacheDir, "files", fmt.Sprint(modID), fmt.Sprint(fileID))
}

func (s *modServer) ensureFile(ctx context.Context, selection selected) (Entry, error) {
	key := fmt.Sprintf("%d/%d", selection.mod.ID, selection.file.ID)
	s.mu.Lock()
	flight := s.flights[key]
	if flight == nil {
		flight = &cacheFlight{done: make(chan struct{})}
		s.flights[key] = flight
		// Shared cache work belongs to the server, not to the first requesting
		// client. Cancelling one job must not abort another client's download.
		go func() {
			workCtx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
			defer cancel()
			select {
			case s.slots <- struct{}{}:
				flight.entry, flight.err = s.fillFile(workCtx, selection)
				<-s.slots
			case <-workCtx.Done():
				flight.err = workCtx.Err()
			}
			s.mu.Lock()
			delete(s.flights, key)
			close(flight.done)
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return Entry{}, ctx.Err()
	case <-flight.done:
		entry := flight.entry
		entry.Requested = selection.requested
		return entry, flight.err
	}
}

func (s *modServer) fillFile(ctx context.Context, selection selected) (Entry, error) {
	entry := manifestEntry(selection)
	if selection.mod.ID <= 0 || selection.file.ID <= 0 || !safeJarName(entry.Filename) {
		return entry, errors.New("invalid cache file identity")
	}
	dir := s.fileDir(entry.ModID, entry.FileID)
	path := filepath.Join(dir, entry.Filename)
	verified, err := verifyJar(path, selection.file, s.cfg.MaxBytes)
	if err == nil {
		entry.Status = "cached"
		entry.Verified = &verified
	} else {
		if ctx.Err() != nil {
			return entry, ctx.Err()
		}
		source, err := s.client.getDownloadURL(ctx, entry.ModID, entry.FileID)
		if err != nil {
			return entry, err
		}
		// A server cache is managed exclusively by this application. Corrupted
		// cache entries can safely be repaired after full validation.
		status, verified, err := downloadJar(ctx, s.downloads, source, dir, selection.file, s.client.Key, s.cfg.MaxBytes, true)
		if err != nil {
			return entry, err
		}
		entry.Status = status
		entry.Verified = &verified
	}
	if err := writeCacheJSON(filepath.Join(dir, "entry.json"), entry); err != nil {
		return entry, err
	}
	return entry, nil
}

func (s *modServer) cachedEntry(modID, fileID int64) (Entry, string, error) {
	dir := s.fileDir(modID, fileID)
	var entry Entry
	if err := readCacheJSON(filepath.Join(dir, "entry.json"), &entry); err != nil {
		return entry, "", err
	}
	if entry.ModID != modID || entry.FileID != fileID || entry.Verified == nil || !safeJarName(entry.Filename) {
		return entry, "", errors.New("invalid cached metadata")
	}
	path := filepath.Join(dir, entry.Filename)
	verified, err := verifyJar(path, File{Hashes: entry.Hashes, FileLength: entry.ExpectedLength}, s.cfg.MaxBytes)
	if err != nil {
		return entry, "", err
	}
	if verified.SHA256 != entry.Verified.SHA256 || verified.Size != entry.Verified.Size {
		return entry, "", errors.New("cache SHA-256 mismatch")
	}
	return entry, path, nil
}

func readCacheJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid cache JSON")
	}
	return nil
}

func writeCacheJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cache-*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Windows may require moving an existing destination out of the way.
	backup := path + ".old"
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(backup)
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		if err := os.Rename(file.Name(), path); err != nil {
			_ = os.Rename(backup, path)
			return err
		}
		_ = os.Remove(backup)
		return nil
	}
	return os.Rename(file.Name(), path)
}
