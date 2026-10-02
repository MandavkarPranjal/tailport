// Package state records the tailport runs on this machine so that `tailport
// status` and `tailport stop` can find them again after tailport exits.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mode is where a run is published.
type Mode string

const (
	// ModeFunnel is published to the public internet by Tailscale Funnel.
	ModeFunnel Mode = "funnel"
	// ModeTailnet is reachable only from inside the tailnet.
	ModeTailnet Mode = "tailnet"
)

// Run is one running tailport.
type Run struct {
	// PID is the tailport process serving the port.
	PID int `json:"pid"`
	// Port is the local port being shared.
	Port int `json:"port"`
	// Path is the URL prefix the service is published under, if any.
	Path string `json:"path,omitempty"`
	// Hostname is the tailport node name, e.g. tailport.
	Hostname string `json:"hostname"`
	// Mode is where the run is published.
	Mode Mode `json:"mode"`
	// Started is when the run came up.
	Started time.Time `json:"started"`
	// URLs are the addresses the run is reachable on.
	URLs []string `json:"urls"`
	// LogPath is the file a detached run writes to.
	LogPath string `json:"logPath,omitempty"`
}

// FileName is the file a run is recorded in, keyed by pid so several tailports
// can run side by side.
func FileName(pid int) string {
	return fmt.Sprintf("%d.json", pid)
}

// Dir is the directory run files live in, under the tailport state directory.
func Dir(stateDir string) string {
	return filepath.Join(stateDir, "runs")
}

// Save writes a run's state file.
func Save(stateDir string, run Run) error {
	dir := Dir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, FileName(run.PID))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// Load reads the state file for one pid.
func Load(stateDir string, pid int) (*Run, error) {
	data, err := os.ReadFile(filepath.Join(Dir(stateDir), FileName(pid)))
	if err != nil {
		return nil, err
	}
	return decode(data)
}

func decode(data []byte) (*Run, error) {
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if run.PID <= 0 || run.Port <= 0 {
		return nil, fmt.Errorf("decode state: incomplete run record")
	}
	return &run, nil
}

// List returns every recorded run, oldest first. Files that no longer parse or
// no longer belong to a live process are left alone for Prune to clean up.
func List(stateDir string) ([]Run, error) {
	entries, err := os.ReadDir(Dir(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state directory: %w", err)
	}
	var runs []Run
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(Dir(stateDir), entry.Name()))
		if err != nil {
			continue
		}
		run, err := decode(data)
		if err != nil {
			continue
		}
		runs = append(runs, *run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Started.Equal(runs[j].Started) {
			return runs[i].PID < runs[j].PID
		}
		return runs[i].Started.Before(runs[j].Started)
	})
	return runs, nil
}

// Remove deletes the state file for a pid.
func Remove(stateDir string, pid int) error {
	err := os.Remove(filepath.Join(Dir(stateDir), FileName(pid)))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// Prune deletes state files whose process is gone and returns how many it
// removed.
func Prune(stateDir string) (int, error) {
	entries, err := os.ReadDir(Dir(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read state directory: %w", err)
	}
	// The request logs have to be dealt with first, while the records that say
	// whether their runs are still alive are still on disk. The loop below
	// deletes every record of a dead run, so anything decided afterwards would
	// be looking for a file that is already gone.
	removed := pruneRequestLogs(stateDir, entries)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(Dir(stateDir), entry.Name()))
		if err != nil {
			continue
		}
		run, err := decode(data)
		if err != nil || Alive(run.PID) {
			continue
		}
		if err := os.Remove(filepath.Join(Dir(stateDir), entry.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// requestLogSuffix marks the request log that sits beside a run record. It
// deliberately does not end in .json, so state.List and Prune never mistake a
// stream of requests for a run.
const requestLogSuffix = ".requests.jsonl"

// pruneRequestLogs deletes the request logs of runs that have gone away. A run
// removes its own log when it stops, so anything left here belongs to a run that
// was killed, and the log would otherwise grow without bound on disk.
//
// It has to run before the records are pruned, because the record is what says
// whether the log is still wanted. Called afterwards, every lookup fails on the
// records already deleted above and the logs survive for ever.
func pruneRequestLogs(stateDir string, entries []os.DirEntry) int {
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), requestLogSuffix) {
			continue
		}
		// <pid>.requests.jsonl belongs to <pid>.json, so the run record decides
		// whether it is still wanted.
		stem := strings.TrimSuffix(entry.Name(), requestLogSuffix)
		data, err := os.ReadFile(filepath.Join(Dir(stateDir), stem+".json"))
		if err != nil {
			continue
		}
		run, err := decode(data)
		if err != nil || Alive(run.PID) {
			continue
		}
		if err := os.Remove(filepath.Join(Dir(stateDir), entry.Name())); err == nil {
			removed++
		}
	}
	return removed
}
