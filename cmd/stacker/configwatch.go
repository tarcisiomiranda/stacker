package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

const configPollInterval = 500 * time.Millisecond

// startConfigWatcher polls stacker.yml for external edits and queues a reload
// on the model. Safe to call once after configPath is set.
func (m *model) startConfigWatcher() {
	if m.configPath == "" {
		return
	}
	// Seed hash so the first poll does not re-apply the already-loaded config.
	if data, err := os.ReadFile(m.configPath); err == nil {
		m.setConfigHash(hashBytes(data))
	}
	go m.configWatchLoop()
}

func (m *model) configWatchLoop() {
	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()
	var lastMod time.Time
	if info, err := os.Stat(m.configPath); err == nil {
		lastMod = info.ModTime()
	}
	for {
		select {
		case <-m.watchStop:
			return
		case <-ticker.C:
			info, err := os.Stat(m.configPath)
			if err != nil {
				continue
			}
			mod := info.ModTime()
			if !mod.After(lastMod) {
				continue
			}
			lastMod = mod
			m.tryReloadConfig()
		}
	}
}

func (m *model) tryReloadConfig() {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		m.queueConfigErr(fmt.Sprintf("config reload failed: %v", err))
		return
	}
	sum := hashBytes(data)
	if sum == m.getConfigHash() {
		return
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		m.queueConfigErr(fmt.Sprintf("config reload failed: %v", err))
		return
	}
	m.pendingMu.Lock()
	m.pendingConfig = &cfg
	m.pendingConfigHash = sum
	m.pendingConfigErr = ""
	m.pendingMu.Unlock()
	m.notify()
}

func (m *model) queueConfigErr(msg string) {
	m.pendingMu.Lock()
	m.pendingConfigErr = msg
	m.pendingMu.Unlock()
	m.notify()
}

func (m *model) getConfigHash() string {
	m.hashMu.Lock()
	defer m.hashMu.Unlock()
	return m.configHash
}

func (m *model) setConfigHash(h string) {
	m.hashMu.Lock()
	defer m.hashMu.Unlock()
	m.configHash = h
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// applyPendingConfig runs on the supervisor/TUI goroutine (refresh path).
func (m *model) applyPendingConfig() {
	m.pendingMu.Lock()
	cfg := m.pendingConfig
	hash := m.pendingConfigHash
	errMsg := m.pendingConfigErr
	m.pendingConfig = nil
	m.pendingConfigHash = ""
	m.pendingConfigErr = ""
	m.pendingMu.Unlock()

	if errMsg != "" {
		m.statusText = errMsg
	}
	if cfg == nil {
		return
	}
	m.applyConfigDiff(*cfg)
	m.setConfigHash(hash)
	if m.statusText == "" || !strings.HasPrefix(m.statusText, "config reload failed") {
		if !strings.Contains(m.statusText, "still running") {
			m.statusText = "Config reloaded"
		}
	}
}

// applyConfigDiff merges a newly loaded config into the live process list.
//
// List layout after apply:
//
//	[YAML processes…][orphaned running services…][YAML tasks…][orphaned tasks…]
//
// numProcesses counts only non-orphaned YAML services (reorderable prefix).
// Running processes removed from YAML stay as orphans until they stop.
func (m *model) applyConfigDiff(cfg Config) {
	if cfg.UI.WheelLines <= 0 {
		cfg.UI.WheelLines = 3
	}

	selectedName := ""
	if p := m.current(); p != nil {
		selectedName = p.Name
	}

	oldByName := make(map[string]*Process, len(m.processes))
	for _, p := range m.processes {
		oldByName[p.Name] = p
	}

	hl := cfg.UI.HighlightErrors
	m.hlErr.Store(hl)

	var (
		yamlProcs    []*Process
		orphanSvcs   []*Process
		yamlTasks    []*Process
		orphanTasks  []*Process
		toAutostart  []*Process
	)

	// --- Real processes (YAML order) ---
	for _, name := range orderedNames(cfg) {
		pc := cfg.Processes[name]
		if old, ok := oldByName[name]; ok && !old.oneShot {
			old.mu.Lock()
			old.Config = pc
			if cfg.UI.MaxLogLines > 0 {
				old.maxLogs = cfg.UI.MaxLogLines
			}
			old.detectErrors = hl
			if !hl {
				old.errCount = 0
			}
			old.orphaned = false
			old.mu.Unlock()
			yamlProcs = append(yamlProcs, old)
			delete(oldByName, name)
			continue
		}
		p := NewProcess(name, pc, cfg.UI.MaxLogLines)
		p.detectErrors = hl
		yamlProcs = append(yamlProcs, p)
		if pc.Autostart {
			toAutostart = append(toAutostart, p)
		}
	}

	// Orphan services: removed from YAML but still active.
	for name, old := range oldByName {
		if old.oneShot {
			continue
		}
		st := old.Status()
		active := st == StatusRunning || st == StatusStarting || st == StatusStopping
		if active {
			old.mu.Lock()
			old.orphaned = true
			old.mu.Unlock()
			orphanSvcs = append(orphanSvcs, old)
		}
		delete(oldByName, name)
	}

	// --- Standalone tasks (YAML order) ---
	// Re-collect one-shot entries that were in oldByName (we deleted non-oneShot above).
	// Rebuild map of remaining one-shots from previous processes.
	// (oldByName only has one-shots now if we only deleted non-oneShot — wait, we deleted all non-oneShot.)
	// Actually above we delete every non-oneShot name. One-shots remain in oldByName only if we
	// never touched them. First loop only matched !oneShot; one-shots stay in oldByName.
	// Fix: the orphan service loop deleted non-oneShot. One-shots still in oldByName.

	for _, name := range orderedTaskNames(cfg) {
		tc := cfg.Tasks[name]
		pc := ProcessConfig{Command: tc.Command, Cwd: tc.Cwd, Color: tc.Color}
		if old, ok := oldByName[name]; ok && old.oneShot {
			old.mu.Lock()
			old.Config = pc
			if cfg.UI.MaxLogLines > 0 {
				old.maxLogs = cfg.UI.MaxLogLines
			}
			old.detectErrors = hl
			if !hl {
				old.errCount = 0
			}
			old.orphaned = false
			old.mu.Unlock()
			yamlTasks = append(yamlTasks, old)
			delete(oldByName, name)
			continue
		}
		// Name might exist as a former service — ignore type flip for simplicity.
		p := NewProcess(name, pc, cfg.UI.MaxLogLines)
		p.oneShot = true
		p.detectErrors = hl
		yamlTasks = append(yamlTasks, p)
	}

	for name, old := range oldByName {
		if !old.oneShot {
			delete(oldByName, name)
			continue
		}
		st := old.Status()
		active := st == StatusRunning || st == StatusStarting || st == StatusStopping
		if active {
			old.mu.Lock()
			old.orphaned = true
			old.mu.Unlock()
			orphanTasks = append(orphanTasks, old)
		}
		delete(oldByName, name)
	}

	newProcs := make([]*Process, 0, len(yamlProcs)+len(orphanSvcs)+len(yamlTasks)+len(orphanTasks))
	newProcs = append(newProcs, yamlProcs...)
	newProcs = append(newProcs, orphanSvcs...)
	newProcs = append(newProcs, yamlTasks...)
	newProcs = append(newProcs, orphanTasks...)

	for _, p := range newProcs {
		p.SetDetectErrors(hl)
	}

	m.procsMu.Lock()
	m.processes = newProcs
	m.numProcesses = len(yamlProcs)
	m.cfg = cfg
	m.procsMu.Unlock()

	m.selected = -1
	for i, p := range newProcs {
		if p.Name == selectedName {
			m.selected = i
			break
		}
	}
	if m.selected < 0 && len(newProcs) > 0 {
		m.selected = 0
	}

	for _, p := range toAutostart {
		go func(proc *Process) { _ = proc.Start(m.notify) }(p)
	}

	if n := len(orphanSvcs) + len(orphanTasks); n > 0 {
		m.statusText = fmt.Sprintf("Config reloaded (%d removed still running — stop to drop)", n)
	}
}

// pruneOrphans drops orphaned processes that have finished so the list
// eventually matches YAML after the user stops them.
func (m *model) pruneOrphans() {
	procs := m.procs()
	selectedName := ""
	if p := m.current(); p != nil {
		selectedName = p.Name
	}

	var (
		yamlProcs   []*Process
		orphanSvcs  []*Process
		yamlTasks   []*Process
		orphanTasks []*Process
		changed     bool
	)
	for _, p := range procs {
		if p.orphaned {
			st := p.Status()
			active := st == StatusRunning || st == StatusStarting || st == StatusStopping
			if !active {
				changed = true
				continue
			}
			if p.oneShot {
				orphanTasks = append(orphanTasks, p)
			} else {
				orphanSvcs = append(orphanSvcs, p)
			}
			continue
		}
		if p.oneShot {
			yamlTasks = append(yamlTasks, p)
		} else {
			yamlProcs = append(yamlProcs, p)
		}
	}
	if !changed {
		return
	}

	kept := make([]*Process, 0, len(yamlProcs)+len(orphanSvcs)+len(yamlTasks)+len(orphanTasks))
	kept = append(kept, yamlProcs...)
	kept = append(kept, orphanSvcs...)
	kept = append(kept, yamlTasks...)
	kept = append(kept, orphanTasks...)

	m.procsMu.Lock()
	m.processes = kept
	m.numProcesses = len(yamlProcs)
	m.procsMu.Unlock()

	m.selected = -1
	for i, p := range kept {
		if p.Name == selectedName {
			m.selected = i
			break
		}
	}
	if m.selected < 0 && len(kept) > 0 {
		m.selected = 0
	}
}

// stopConfigWatcher signals the poll loop to exit (best-effort).
func (m *model) stopConfigWatcher() {
	select {
	case <-m.watchStop:
	default:
		close(m.watchStop)
	}
}
