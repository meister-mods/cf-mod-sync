package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServerOptions struct {
	Listen      string
	CacheDir    string
	Token       string
	Workers     int
	MaxBytes    int64
	MetadataTTL time.Duration
	JobTTL      time.Duration
	TLSCert     string
	TLSKey      string
}

var upstreamURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

type JobRequest struct {
	ProjectURLs         []string `json:"project_urls"`
	MinecraftVersion    string   `json:"minecraft_version"`
	ReleaseChannel      string   `json:"release_channel"`
	IncludeDependencies *bool    `json:"include_dependencies,omitempty"`
	Refresh             bool     `json:"refresh"`
}

type JobFile struct {
	Entry
	DownloadPath string `json:"download_path,omitempty"`
}

type JobSnapshot struct {
	ID               string    `json:"id"`
	State            string    `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	MinecraftVersion string    `json:"minecraft_version"`
	ReleaseChannel   string    `json:"release_channel"`
	Total            int       `json:"total"`
	Completed        int       `json:"completed"`
	CacheHits        int       `json:"cache_hits"`
	SelectionCached  bool      `json:"selection_cached"`
	Files            []JobFile `json:"files"`
	Errors           []Issue   `json:"errors"`
}

type serverJob struct {
	mu     sync.Mutex
	data   JobSnapshot
	cancel context.CancelFunc
}

func (j *serverJob) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.data
	s.Files = append([]JobFile{}, s.Files...)
	s.Errors = append([]Issue{}, s.Errors...)
	return s
}

type modServer struct {
	cfg       ServerOptions
	client    *Client
	downloads *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	jobs      map[string]*serverJob
	active    int
	flights   map[string]*cacheFlight
	slots     chan struct{}
	planSlot  chan struct{}
}

func newModServer(ctx context.Context, cfg ServerOptions, c *Client, downloads *http.Client) (*modServer, error) {
	if cfg.Workers < 1 || cfg.Workers > 16 || cfg.MaxBytes < 1 || cfg.MetadataTTL <= 0 || cfg.JobTTL <= 0 {
		return nil, errors.New("invalid server limits")
	}
	cacheDir, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	cfg.CacheDir = cacheDir
	for _, dir := range []string{"files", "selections"} {
		if err := os.MkdirAll(filepath.Join(cacheDir, dir), 0700); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	return &modServer{
		cfg: cfg, client: c, downloads: downloads, ctx: ctx, cancel: cancel,
		jobs: make(map[string]*serverJob), flights: make(map[string]*cacheFlight),
		slots: make(chan struct{}, cfg.Workers), planSlot: make(chan struct{}, 1),
	}, nil
}

func runServer(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg := ServerOptions{}
	flags.StringVar(&cfg.Listen, "listen", envDefault("SERVER_LISTEN", "127.0.0.1:8080"), "HTTP listen address")
	flags.StringVar(&cfg.CacheDir, "cache", envDefault("SERVER_CACHE_DIR", "cache"), "persistent server cache directory")
	flags.StringVar(&cfg.Token, "token", envDefault("SERVER_TOKEN", ""), "client bearer token (use SERVER_TOKEN to avoid shell history)")
	flags.IntVar(&cfg.Workers, "workers", 4, "maximum simultaneous upstream downloads (1-16)")
	maxMB := flags.Int("max-file-mb", defaultMaxFileMB, "maximum JAR size in MiB")
	flags.DurationVar(&cfg.MetadataTTL, "metadata-ttl", 15*time.Minute, "reuse latest-file selections for this duration")
	flags.DurationVar(&cfg.JobTTL, "job-ttl", time.Hour, "retain completed jobs in memory for this duration")
	flags.StringVar(&cfg.TLSCert, "tls-cert", "", "optional TLS certificate PEM")
	flags.StringVar(&cfg.TLSKey, "tls-key", "", "optional TLS private key PEM")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected server arguments")
	}
	if *maxMB < 1 || *maxMB > 4096 {
		return errors.New("max-file-mb must be between 1 and 4096")
	}
	cfg.MaxBytes = int64(*maxMB) << 20
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && len(cfg.Token) < 32 {
		return errors.New("listening outside loopback requires SERVER_TOKEN with at least 32 characters")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return errors.New("tls-cert and tls-key must be supplied together")
	}
	key := strings.TrimSpace(os.Getenv("CURSEFORGE_API_KEY"))
	if key == "" || strings.HasPrefix(key, "YOUR_") {
		return errors.New("CURSEFORGE_API_KEY must be configured on the server")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	service, err := newModServer(ctx, cfg, newClient(key), httpDownloadClient(key))
	if err != nil {
		return err
	}
	defer service.cancel()
	server := &http.Server{
		Addr: cfg.Listen, Handler: service.handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  90 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	log.Printf("cfmodsync server: %s://%s | cache=%s | workers=%d | auth=%t",
		scheme, cfg.Listen, service.cfg.CacheDir, cfg.Workers, cfg.Token != "")
	if cfg.TLSCert != "" {
		err = server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *modServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, map[string]any{"status": "ok", "protocol_version": 1, "mod_loader": "Forge"})
	})
	mux.HandleFunc("POST /api/v1/jobs", s.createJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.cancelJob)
	mux.HandleFunc("GET /api/v1/files/{mod}/{file}", s.serveFile)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		// The desktop API has no browser caller or cookie authentication.
		if r.Header.Get("Origin") != "" {
			s.apiError(w, http.StatusForbidden, "browser origins are not allowed")
			return
		}
		if r.URL.Path != "/healthz" && s.cfg.Token != "" {
			want := "Bearer " + s.cfg.Token
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				s.apiError(w, http.StatusUnauthorized, "invalid server access token")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *modServer) cleanError(err error) string {
	message := err.Error()
	for _, secret := range []string{s.client.Key, s.cfg.Token} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return upstreamURLPattern.ReplaceAllString(message, "[upstream URL]")
}

func (s *modServer) json(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *modServer) apiError(w http.ResponseWriter, status int, message string) {
	s.json(w, status, map[string]string{"error": message})
}

func (s *modServer) createJob(w http.ResponseWriter, r *http.Request) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		s.apiError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request JobRequest
	if err := decoder.Decode(&request); err != nil {
		s.apiError(w, http.StatusBadRequest, "invalid or oversized job JSON")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.apiError(w, http.StatusBadRequest, "expected a single JSON object")
		return
	}
	if request.MinecraftVersion == "" {
		request.MinecraftVersion = "1.20.1"
	}
	if request.ReleaseChannel == "" {
		request.ReleaseChannel = "all"
	}
	if !mcVersionPattern.MatchString(request.MinecraftVersion) || len(request.MinecraftVersion) > 64 ||
		(request.ReleaseChannel != "all" && request.ReleaseChannel != "beta" && request.ReleaseChannel != "release") {
		s.apiError(w, http.StatusBadRequest, "invalid Minecraft version or release channel")
		return
	}
	if len(request.ProjectURLs) == 0 || len(request.ProjectURLs) > 500 {
		s.apiError(w, http.StatusBadRequest, "provide between 1 and 500 project URLs")
		return
	}
	seen := make(map[string]bool)
	var slugs []string
	for _, raw := range request.ProjectURLs {
		slug, err := parseProjectURL(raw)
		if err != nil {
			s.apiError(w, http.StatusBadRequest, "every project must be a canonical CurseForge Minecraft mod URL")
			return
		}
		if !seen[slug] {
			slugs = append(slugs, slug)
			seen[slug] = true
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		s.apiError(w, http.StatusInternalServerError, "could not create a job ID")
		return
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(s.ctx, time.Hour)
	job := &serverJob{cancel: cancel, data: JobSnapshot{
		ID: hex.EncodeToString(random[:]), State: "queued", CreatedAt: now, UpdatedAt: now,
		MinecraftVersion: request.MinecraftVersion, ReleaseChannel: request.ReleaseChannel,
		Files: []JobFile{}, Errors: []Issue{},
	}}
	s.mu.Lock()
	for id, old := range s.jobs {
		snapshot := old.snapshot()
		if terminalJob(snapshot.State) && now.Sub(snapshot.UpdatedAt) > s.cfg.JobTTL {
			delete(s.jobs, id)
		}
	}
	if s.active >= 8 || len(s.jobs) >= 128 {
		s.mu.Unlock()
		cancel()
		w.Header().Set("Retry-After", "5")
		s.apiError(w, http.StatusTooManyRequests, "server job limit reached; retry later")
		return
	}
	s.jobs[job.data.ID] = job
	s.active++
	s.mu.Unlock()
	w.Header().Set("Location", "/api/v1/jobs/"+job.data.ID)
	// Capture before starting the worker, so this response has no data race.
	initial := job.snapshot()
	go s.runJob(ctx, job, request, slugs)
	s.json(w, http.StatusAccepted, initial)
}

func terminalJob(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func (s *modServer) findJob(w http.ResponseWriter, r *http.Request) *serverJob {
	s.mu.Lock()
	job := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if job == nil {
		s.apiError(w, http.StatusNotFound, "job not found or expired")
	}
	return job
}

func (s *modServer) getJob(w http.ResponseWriter, r *http.Request) {
	if job := s.findJob(w, r); job != nil {
		s.json(w, http.StatusOK, job.snapshot())
	}
}

func (s *modServer) cancelJob(w http.ResponseWriter, r *http.Request) {
	if job := s.findJob(w, r); job != nil {
		job.cancel()
		s.json(w, http.StatusAccepted, job.snapshot())
	}
}

func (s *modServer) runJob(ctx context.Context, job *serverJob, request JobRequest, slugs []string) {
	defer func() {
		job.cancel()
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	job.mu.Lock()
	job.data.State = "resolving"
	job.data.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()
	deps := request.IncludeDependencies == nil || *request.IncludeDependencies
	options := Options{Version: request.MinecraftVersion, Channel: request.ReleaseChannel, Deps: deps}
	selections, issues, cached := s.resolveCached(ctx, slugs, options, request.Refresh)
	job.mu.Lock()
	job.data.SelectionCached = cached
	job.data.Total = len(selections)
	for _, issue := range issues {
		issue.Error = s.cleanError(errors.New(issue.Error))
		job.data.Errors = append(job.data.Errors, issue)
	}
	for _, selection := range selections {
		job.data.Files = append(job.data.Files, JobFile{Entry: manifestEntry(selection)})
	}
	job.data.State = "caching"
	job.data.UpdatedAt = time.Now().UTC()
	job.mu.Unlock()
	var wg sync.WaitGroup
	work := make(chan int)
	for worker := 0; worker < s.cfg.Workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				entry, err := s.ensureFile(ctx, selections[index])
				file := JobFile{Entry: entry}
				if err != nil {
					file.Entry = manifestEntry(selections[index])
					file.Status = "failed"
					file.Error = s.cleanError(err)
				} else {
					file.DownloadPath = fmt.Sprintf("/api/v1/files/%d/%d", entry.ModID, entry.FileID)
				}
				job.mu.Lock()
				job.data.Files[index] = file
				job.data.Completed++
				if err != nil {
					job.data.Errors = append(job.data.Errors, Issue{Project: file.Slug, Error: file.Error})
				} else if entry.Status == "cached" {
					job.data.CacheHits++
				}
				job.data.UpdatedAt = time.Now().UTC()
				job.mu.Unlock()
			}
		}()
	}
	for index := range selections {
		select {
		case work <- index:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	job.mu.Lock()
	defer job.mu.Unlock()
	job.data.State = "completed"
	if ctx.Err() != nil {
		job.data.State = "cancelled"
	} else if len(job.data.Errors) != 0 {
		job.data.State = "failed"
	}
	job.data.UpdatedAt = time.Now().UTC()
}

func positiveID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, errors.New("invalid file ID")
	}
	return id, nil
}

func (s *modServer) serveFile(w http.ResponseWriter, r *http.Request) {
	modID, err1 := positiveID(r.PathValue("mod"))
	fileID, err2 := positiveID(r.PathValue("file"))
	if err1 != nil || err2 != nil {
		s.apiError(w, http.StatusNotFound, "cached file not found")
		return
	}
	entry, path, err := s.cachedEntry(modID, fileID)
	if err != nil {
		s.apiError(w, http.StatusNotFound, "cached file missing or invalid; submit a new job")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.apiError(w, http.StatusNotFound, "cached file unavailable")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		s.apiError(w, http.StatusInternalServerError, "could not read cached file")
		return
	}
	w.Header().Set("Content-Type", "application/java-archive")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Filename}))
	w.Header().Set("ETag", `"`+entry.Verified.SHA256+`"`)
	w.Header().Set("X-Checksum-SHA256", entry.Verified.SHA256)
	w.Header().Set("Cache-Control", "private, max-age=3600, immutable")
	http.ServeContent(w, r, entry.Filename, stat.ModTime(), file)
}
