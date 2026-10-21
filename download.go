package main

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultMaxFileMB = 1024

type Verified struct {
	Size      int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Integrity string `json:"integrity"`
}

func knownDigest(hashes []Hash) bool {
	for _, h := range hashes {
		if h.Algo == 1 || h.Algo == 2 {
			return true
		}
	}
	return false
}

func verifyJar(path string, expected File, maxBytes int64) (Verified, error) {
	if !knownDigest(expected.Hashes) {
		return Verified{}, errors.New("no trusted SHA-1/MD5 checksum provided by CurseForge")
	}
	f, err := os.Open(path)
	if err != nil {
		return Verified{}, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return Verified{}, err
	}
	if !stat.Mode().IsRegular() {
		f.Close()
		return Verified{}, errors.New("not a regular file")
	}
	if stat.Size() <= 0 || stat.Size() > maxBytes {
		f.Close()
		return Verified{}, fmt.Errorf("unexpected file size %d", stat.Size())
	}
	if expected.FileLength > 0 && stat.Size() != expected.FileLength {
		f.Close()
		return Verified{}, fmt.Errorf("size mismatch: got %d, API says %d", stat.Size(), expected.FileLength)
	}
	sh1, m5, sh256 := sha1.New(), md5.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(sh1, m5, sh256), io.LimitReader(f, maxBytes+1))
	f.Close()
	if err != nil {
		return Verified{}, err
	}
	if n != stat.Size() {
		return Verified{}, errors.New("file changed while reading")
	}
	validAlgorithms := make(map[int]bool)
	for _, h := range expected.Hashes {
		var actual string
		switch h.Algo {
		case 1:
			actual = hex.EncodeToString(sh1.Sum(nil))
		case 2:
			actual = hex.EncodeToString(m5.Sum(nil))
		default:
			continue
		}
		// Reject corrupted API metadata as well as corrupted bytes.
		expectedHex := strings.TrimSpace(h.Value)
		if len(expectedHex) != len(actual) {
			return Verified{}, fmt.Errorf("invalid checksum length for algorithm %d", h.Algo)
		}
		if !strings.EqualFold(expectedHex, actual) {
			return Verified{}, fmt.Errorf("%s hash mismatch", map[int]string{1: "SHA-1", 2: "MD5"}[h.Algo])
		}
		validAlgorithms[h.Algo] = true
	}
	if len(validAlgorithms) == 0 {
		return Verified{}, errors.New("no supported checksum")
	}
	if err := checkZipCRC(path, maxBytes); err != nil {
		return Verified{}, fmt.Errorf("JAR archive verification: %w", err)
	}
	integrity := "MD5 + ZIP/CRC"
	if validAlgorithms[1] {
		integrity = "SHA-1 + ZIP/CRC"
	}
	if validAlgorithms[1] && validAlgorithms[2] {
		integrity = "SHA-1 + MD5 + ZIP/CRC"
	}
	return Verified{Size: n, SHA256: hex.EncodeToString(sh256.Sum(nil)), Integrity: integrity}, nil
}

func checkZipCRC(path string, maxBytes int64) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) == 0 {
		return errors.New("empty ZIP/JAR archive")
	}
	// ZIP integrity is separate from cryptographic verification. Bound decoding.
	const maxUncompressed = int64(8) << 30
	var total int64
	for _, entry := range r.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.UncompressedSize64 > uint64(maxUncompressed) || total > maxUncompressed-int64(entry.UncompressedSize64) {
			return errors.New("archive expands beyond 8 GiB safety limit")
		}
		total += int64(entry.UncompressedSize64)
		f, err := entry.Open()
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(io.Discard, io.LimitReader(f, int64(entry.UncompressedSize64)+1))
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("%q: %w", entry.Name, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(entry.UncompressedSize64) {
			return fmt.Errorf("%q: decompressed size mismatch", entry.Name)
		}
	}
	return nil
}

func isCurseForgeCDN(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "forgecdn.net" || strings.HasSuffix(h, ".forgecdn.net")
}

func httpDownloadClient(key string) *http.Client {
	return &http.Client{
		Timeout: 25 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many download redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("refusing insecure download redirect")
			}
			// net/http may copy custom headers across origins: explicitly reset.
			req.Header.Del("x-api-key")
			if isCurseForgeCDN(req.URL.Hostname()) {
				req.Header.Set("x-api-key", key)
			}
			return nil
		},
	}
}

// downloadJar always writes to a temporary path and moves the validated file
// into place only after size, checksum and ZIP CRC checks pass.
func downloadJar(ctx context.Context, hc *http.Client, sourceURL, output string, f File, key string, maxBytes int64, force bool) (string, Verified, error) {
	u, err := url.Parse(sourceURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", Verified{}, errors.New("invalid or insecure download URL")
	}
	if !safeJarName(f.FileName) {
		return "", Verified{}, errors.New("unsafe output filename")
	}
	if f.FileLength > maxBytes && f.FileLength > 0 {
		return "", Verified{}, fmt.Errorf("API file length exceeds configured maximum: %d bytes", f.FileLength)
	}
	if !knownDigest(f.Hashes) {
		return "", Verified{}, errors.New("CurseForge did not publish a supported checksum; refusing unverified download")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return "", Verified{}, err
	}
	target := filepath.Join(output, f.FileName)
	if _, err := os.Stat(target); err == nil && !force {
		valid, err := verifyJar(target, f, maxBytes)
		if err != nil {
			return "", Verified{}, fmt.Errorf("existing %s failed verification (%w); remove it or use -force", f.FileName, err)
		}
		return "cached", valid, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", Verified{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		return "", Verified{}, err
	}
	if isCurseForgeCDN(u.Hostname()) {
		req.Header.Set("x-api-key", key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", Verified{}, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return "", Verified{}, fmt.Errorf("CDN denied download (HTTP %d): check API key and distribution permissions", resp.StatusCode)
		}
		return "", Verified{}, fmt.Errorf("CDN returned HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(output, ".cfmodsync-*.partial")
	if err != nil {
		return "", Verified{}, err
	}
	tempName := tmp.Name()
	defer os.Remove(tempName)
	count, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxBytes+1))
	if copyErr != nil {
		tmp.Close()
		return "", Verified{}, copyErr
	}
	if count > maxBytes {
		tmp.Close()
		return "", Verified{}, fmt.Errorf("file exceeds %d-byte download limit", maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", Verified{}, err
	}
	if err := tmp.Close(); err != nil {
		return "", Verified{}, err
	}
	validated, err := verifyJar(tempName, f, maxBytes)
	if err != nil {
		return "", Verified{}, err
	}

	if _, err := os.Stat(target); err == nil {
		if !force {
			return "", Verified{}, errors.New("target appeared while downloading; refusing overwrite")
		}
		// Windows also needs the target to be moved away first.
		backup, err := os.CreateTemp(output, ".cfmodsync-backup-*")
		if err != nil {
			return "", Verified{}, err
		}
		backupName := backup.Name()
		backup.Close()
		os.Remove(backupName)
		if err := os.Rename(target, backupName); err != nil {
			return "", Verified{}, err
		}
		if err := os.Rename(tempName, target); err != nil {
			_ = os.Rename(backupName, target)
			return "", Verified{}, err
		}
		_ = os.Remove(backupName)
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return "", Verified{}, err
		}
		if err := os.Rename(tempName, target); err != nil {
			return "", Verified{}, err
		}
	}
	return "downloaded", validated, nil
}
