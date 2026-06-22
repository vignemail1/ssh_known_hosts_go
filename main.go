package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultURL            = "https://raw.githubusercontent.com/vignemail1/ssh_known_hosts_go/refs/heads/main/db.txt"
	defaultCacheDir       = ".cache/ssh-knownhosts"
	defaultCacheTTL       = 86400
	defaultConnectTimeout = 5
	defaultMaxTime        = 20
)

func progname() string {
	return filepath.Base(os.Args[0])
}

func logErr(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", progname(), fmt.Sprintf(format, a...))
}

// config holds resolved runtime configuration.
type config struct {
	host           string
	port           string
	url            string
	cacheDir       string
	cacheTTL       time.Duration
	connectTimeout time.Duration
	maxTime        time.Duration
	resolveIPs     bool
	cacheFile      string
	metaFile       string
	etagFile       string
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, defSec int) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(defSec) * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		logErr("%s must be a positive integer", key)
		os.Exit(1)
	}
	return time.Duration(n) * time.Second
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v != "0"
}

func newConfig(host, port string) *config {
	home, _ := os.UserHomeDir()
	cacheDir := envOrDefault("KNOWNHOSTS_CACHE_DIR", filepath.Join(home, defaultCacheDir))

	return &config{
		host:           host,
		port:           port,
		url:            envOrDefault("KNOWNHOSTS_URL", defaultURL),
		cacheDir:       cacheDir,
		cacheTTL:       envDuration("KNOWNHOSTS_CACHE_TTL", defaultCacheTTL),
		connectTimeout: envDuration("KNOWNHOSTS_CONNECT_TIMEOUT", defaultConnectTimeout),
		maxTime:        envDuration("KNOWNHOSTS_MAX_TIME", defaultMaxTime),
		resolveIPs:     envBool("KNOWNHOSTS_RESOLVE_IPS", true),
		cacheFile:      filepath.Join(cacheDir, "known_hosts.cache"),
		metaFile:       filepath.Join(cacheDir, "known_hosts.meta"),
		etagFile:       filepath.Join(cacheDir, "known_hosts.etag"),
	}
}

// meta holds persistent cache metadata.
type meta struct {
	fetchedAt    time.Time
	sourceURL    string
	lastModified string
	etag         string
}

func readMeta(path string) *meta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	m := &meta{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "fetched_at":
			ts, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				m.fetchedAt = time.Unix(ts, 0)
			}
		case "source_url":
			m.sourceURL = v
		case "last_modified":
			m.lastModified = v
		case "etag":
			m.etag = v
		}
	}
	return m
}

func writeMeta(path string, m *meta) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "fetched_at=%d\n", m.fetchedAt.Unix())
	fmt.Fprintf(f, "source_url=%s\n", m.sourceURL)
	fmt.Fprintf(f, "last_modified=%s\n", m.lastModified)
	fmt.Fprintf(f, "etag=%s\n", m.etag)
	return nil
}

func cacheIsFresh(cfg *config) bool {
	m := readMeta(cfg.metaFile)
	if m == nil || m.fetchedAt.IsZero() {
		return false
	}
	return time.Since(m.fetchedAt) < cfg.cacheTTL
}

func cacheExists(cfg *config) bool {
	fi, err := os.Stat(cfg.cacheFile)
	return err == nil && fi.Size() > 0
}

// tryLock uses a directory as a mutex to prevent concurrent refreshes.
func tryLock(cfg *config) (unlock func(), ok bool) {
	lockPath := filepath.Join(cfg.cacheDir, ".lock")
	err := os.Mkdir(lockPath, 0700)
	if err != nil {
		return nil, false
	}
	return func() { os.Remove(lockPath) }, true
}

func validateDownload(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return true
	}
	return false
}

func downloadCache(cfg *config) error {
	m := readMeta(cfg.metaFile)

	client := &http.Client{
		Timeout: cfg.maxTime,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: cfg.connectTimeout,
			}).DialContext,
		},
	}

	req, err := http.NewRequest(http.MethodGet, cfg.url, nil)
	if err != nil {
		return err
	}

	if m != nil {
		if m.etag != "" {
			req.Header.Set("If-None-Match", m.etag)
		}
		if m.lastModified != "" {
			req.Header.Set("If-Modified-Since", m.lastModified)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	newMeta := &meta{
		fetchedAt: time.Now(),
		sourceURL: cfg.url,
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		if m != nil {
			newMeta.lastModified = m.lastModified
			newMeta.etag = m.etag
		}
		return writeMeta(cfg.metaFile, newMeta)

	case http.StatusOK:
		tmpFile := cfg.cacheFile + ".tmp." + strconv.Itoa(os.Getpid())
		f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}

		_, copyErr := io.Copy(f, resp.Body)
		f.Close()
		if copyErr != nil {
			os.Remove(tmpFile)
			return copyErr
		}

		if !validateDownload(tmpFile) {
			os.Remove(tmpFile)
			return fmt.Errorf("downloaded file contains no usable known_hosts entries")
		}

		if err := os.Rename(tmpFile, cfg.cacheFile); err != nil {
			os.Remove(tmpFile)
			return err
		}
		os.Chmod(cfg.cacheFile, 0600)

		newMeta.lastModified = resp.Header.Get("Last-Modified")
		newMeta.etag = resp.Header.Get("ETag")
		return writeMeta(cfg.metaFile, newMeta)

	default:
		return fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}
}

func ensureCache(cfg *config) error {
	if cacheExists(cfg) && cacheIsFresh(cfg) {
		return nil
	}

	unlock, locked := tryLock(cfg)
	if locked {
		defer unlock()

		if cacheExists(cfg) && cacheIsFresh(cfg) {
			return nil
		}

		if err := downloadCache(cfg); err != nil {
			if cacheExists(cfg) {
				logErr("remote refresh failed, using existing cache: %v", err)
				return nil
			}
			return fmt.Errorf("remote refresh failed and no cache available: %w", err)
		}
		return nil
	}

	if cacheExists(cfg) {
		logErr("could not acquire lock, using existing cache")
		return nil
	}

	return fmt.Errorf("could not acquire lock and no cache available")
}

func resolveIPs(host string) []string {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil
	}
	return addrs
}

func candidateNames(cfg *config) []string {
	seen := map[string]struct{}{}
	var names []string

	add := func(s string) {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			names = append(names, s)
		}
	}

	add(cfg.host)
	if cfg.port != "22" {
		add(fmt.Sprintf("[%s]:%s", cfg.host, cfg.port))
	}

	if cfg.resolveIPs {
		for _, ip := range resolveIPs(cfg.host) {
			add(ip)
			if cfg.port != "22" {
				add(fmt.Sprintf("[%s]:%s", ip, cfg.port))
			}
		}
	}

	return names
}

func emitMatchingEntries(cfg *config) error {
	candidates := candidateNames(cfg)
	lookupSet := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		lookupSet[c] = struct{}{}
	}

	f, err := os.Open(cfg.cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		field := strings.Fields(line)
		if len(field) == 0 {
			continue
		}

		for _, token := range strings.Split(field[0], ",") {
			if _, ok := lookupSet[token]; ok {
				fmt.Println(line)
				break
			}
		}
	}
	return sc.Err()
}

func initCacheDir(cfg *config) error {
	if err := os.MkdirAll(cfg.cacheDir, 0700); err != nil {
		return err
	}
	return syscall.Chmod(cfg.cacheDir, 0700)
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		os.Exit(0)
	}

	host := os.Args[1]
	port := "22"
	if len(os.Args) >= 3 && os.Args[2] != "" {
		port = os.Args[2]
	}

	cfg := newConfig(host, port)

	if err := initCacheDir(cfg); err != nil {
		logErr("cannot initialise cache directory: %v", err)
		os.Exit(0)
	}

	if err := ensureCache(cfg); err != nil {
		logErr("%v", err)
		os.Exit(0)
	}

	if err := emitMatchingEntries(cfg); err != nil {
		logErr("error reading cache: %v", err)
		os.Exit(1)
	}
}
