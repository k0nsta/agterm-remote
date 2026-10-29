package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// HostInfo is the locally cached result of probing one remote host.
type HostInfo struct {
	// Host is the destination the file was saved for: the file name keeps it
	// only lossily (FileKey).
	Host        string    `json:"host,omitempty"`
	Home        string    `json:"home"`
	Mux         string    `json:"mux"`
	Relay       string    `json:"relay"`
	Mosh        bool      `json:"mosh"`
	AgrVersion  string    `json:"agr_version"`
	ZmxVersion  string    `json:"zmx_version"`
	TmuxVersion string    `json:"tmux_version"`
	ZmxLabels   bool      `json:"zmx_labels"`
	ProbedAt    time.Time `json:"probed_at"`
}

// LoadHostInfo reads the cached information for host. Missing information is
// returned as an os.ErrNotExist-wrapping error so callers can distinguish the
// first probe from a corrupt cache.
func LoadHostInfo(dirs paths.Dirs, host string) (HostInfo, error) {
	if !token.ValidHost(host) {
		return HostInfo{}, fmt.Errorf("invalid host %q", host)
	}
	data, err := os.ReadFile(dirs.HostInfo(token.FileKey(host)))
	if err != nil {
		return HostInfo{}, fmt.Errorf("read host info: %w", err)
	}
	var info HostInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return HostInfo{}, fmt.Errorf("decode host info: %w", err)
	}
	return info, nil
}

// SaveHostInfo atomically persists host's probe information below dirs.
func SaveHostInfo(dirs paths.Dirs, host string, info HostInfo) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	info.Host = host
	path := dirs.HostInfo(token.FileKey(host))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create host info directory: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode host info: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".host-info-*")
	if err != nil {
		return fmt.Errorf("create host info temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod host info temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write host info temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync host info temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close host info temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace host info: %w", err)
	}
	removeTemp = false
	return nil
}

// CachedHosts lists the hosts with cached probe information, sorted. A file
// written before HostInfo.Host yields its host only when the file name's
// prefix maps back to the same key, which holds for any host in the token
// alphabet; one that does not (user@host) is left out.
func CachedHosts(dirs paths.Dirs) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(filepath.Dir(dirs.HostInfo("")), "*.json"))
	if err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(files))
	for _, file := range files {
		key := strings.TrimSuffix(filepath.Base(file), ".json")
		var info HostInfo
		if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &info) == nil &&
			token.ValidHost(info.Host) && token.FileKey(info.Host) == key {
			hosts = append(hosts, info.Host)
			continue
		}
		if dash := strings.LastIndexByte(key, '-'); dash > 0 && token.FileKey(key[:dash]) == key {
			hosts = append(hosts, key[:dash])
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

func hostInfoMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
