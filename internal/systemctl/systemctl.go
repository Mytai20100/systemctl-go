// Package systemctl implements the Systemctl type which emulates systemctl
// commands against a set of *.service / *.socket / *.target unit files.
//
// NOTE FOR SESSION 2: This file contains the struct definition and the
// simpler helper methods.  The heavy service-control logic (start, stop,
// reload, …) is in systemctl_exec.go which still needs to be written.
package systemctl

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"systemctl-go/internal/conf"
	"systemctl-go/internal/journal"
	"systemctl-go/internal/logger"
	"systemctl-go/internal/paths"
	"systemctl-go/internal/process"
	"systemctl-go/internal/types"
	"systemctl-go/internal/units"
	"systemctl-go/internal/utils"
)

var logg = logger.GetLogger("systemctl")

// Systemctl is the main emulator of systemctl commands.
type Systemctl struct {
	Error int // accumulated exit code (OR of NotOK, NotActive, NotFound …)

	// options (mirrors Python module-level globals)
	extraVars      []string
	force          bool
	full           bool
	noAskPassword  bool
	noLegend       bool
	now            int
	presetMode     string
	quiet          bool
	showAll        int
	onlyWhat       []string
	onlyProperty   []string
	onlyState      []string
	onlyType       []string

	// runtime state
	systemdVersion  int
	defaultTarget   string
	sysinitTarget   *conf.Conf
	exitMode        int
	initMode        int
	boottime        *float64 // cached
	restartedUnit   map[string][]float64
	restartFailed   map[string]float64
	sockets         map[string]*conf.Socket
	LoopSleep_      int
	loopLock        sync.Mutex

	root      string
	Unitfiles *units.UnitFiles
	Journal   *journal.Journal
}

// New creates a Systemctl for the given root directory.
func New(root string) *Systemctl {
	if root == "" {
		root = types.Root
	}
	s := &Systemctl{
		Error:          types.NotAProblem,
		extraVars:      append([]string{}, types.ExtraVars...),
		force:          types.Force,
		full:           types.Full,
		noAskPassword:  types.NoAskPassword,
		noLegend:       types.NoLegend,
		now:            types.Now,
		presetMode:     types.PresetMode,
		quiet:          types.Quiet,
		showAll:        types.ShowAll,
		onlyWhat:       utils.Commalist(types.OnlyWhat),
		onlyProperty:   utils.Commalist(types.OnlyProperty),
		onlyState:      utils.Commalist(types.OnlyState),
		onlyType:       utils.Commalist(types.OnlyType),
		systemdVersion: types.SystemCompatibilityVersion,
		defaultTarget:  types.DefaultTarget,
		exitMode:       types.ExitMode,
		initMode:       types.InitMode,
		restartedUnit:  map[string][]float64{},
		restartFailed:  map[string]float64{},
		sockets:        map[string]*conf.Socket{},
		root:           root,
	}
	loopSleep := types.InitLoopSleepDefault
	if types.InitMode > 0 {
		loopSleep = max(1, types.InitLoopSleepDefault/types.InitMode)
	}
	s.LoopSleep_ = loopSleep
	s.Unitfiles = units.NewUnitFiles(root)
	s.Journal = journal.New(s.Unitfiles)
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// LoopSleep implements the listen.Controller interface.
func (s *Systemctl) LoopSleep() int { return s.LoopSleep_ }

// LoopLock implements the listen.Controller interface.
func (s *Systemctl) LoopLock() *sync.Mutex { return &s.loopLock }

// ── unit section helper ───────────────────────────────────────────────────

// GetUnitSection returns the capitalised section name for a module.
func (s *Systemctl) GetUnitSection(module, defaultSection string) string {
	if defaultSection == "" {
		defaultSection = types.SectionService
	}
	ut := utils.GetUnitType(module)
	if ut == "" {
		ut = defaultSection
	}
	return titleCase(ut)
}

func (s *Systemctl) GetUnitSectionFrom(c *conf.Conf, defaultSection string) string {
	return s.GetUnitSection(c.Name(), defaultSection)
}

// ── listing ───────────────────────────────────────────────────────────────

// ListServiceUnits returns (unit, state-string, description) for matching units.
func (s *Systemctl) ListServiceUnits(modules ...string) [][3]string {
	result := map[string]string{}
	active := map[string]string{}
	substate := map[string]string{}
	description := map[string]string{}

	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		result[unit] = "not-found"
		active[unit] = "inactive"
		substate[unit] = "dead"
		description[unit] = ""
		func() {
			defer func() {
				if r := recover(); r != nil {
					logg.Warnf("list-units: %v", r)
				}
			}()
			c := s.Unitfiles.GetConf(unit)
			result[unit] = "loaded"
			description[unit] = s.Unitfiles.GetDescription(c, types.SectionUnit, "")
			active[unit] = s.GetActiveFrom(c)
			sub := s.GetSubstateFrom(c)
			if sub == "" {
				sub = "unknown"
			}
			substate[unit] = sub
		}()
		if len(s.onlyState) > 0 {
			inState := false
			for _, st := range s.onlyState {
				if result[unit] == st || active[unit] == st || substate[unit] == st {
					inState = true
					break
				}
			}
			if !inState {
				delete(result, unit)
			}
		}
	}
	sorted := sortedKeys(result)
	out := make([][3]string, len(sorted))
	for i, unit := range sorted {
		out[i] = [3]string{unit, result[unit] + " " + active[unit] + " " + substate[unit], description[unit]}
	}
	return out
}

// ListUnitsModules wraps ListServiceUnits and adds a legend footer.
func (s *Systemctl) ListUnitsModules(modules ...string) [][3]string {
	result := s.ListServiceUnits(modules...)
	if s.noLegend {
		return result
	}
	hint := "To show all installed unit files use 'systemctl list-unit-files'."
	found := fmt.Sprintf("%d loaded units listed.", len(result))
	return append(result, [3]string{"", "", ""}, [3]string{found, "", ""}, [3]string{hint, "", ""})
}

// ListServiceUnitFiles returns (unit, enabled-state) for matching service units.
func (s *Systemctl) ListServiceUnitFiles(modules ...string) [][2]string {
	result := map[string]*conf.Conf{}
	enabled := map[string]string{}

	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if len(s.onlyType) > 0 {
			ut := utils.GetUnitType(unit)
			found := false
			for _, t := range s.onlyType {
				if ut == t {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		result[unit] = nil
		enabled[unit] = ""
		func() {
			defer func() { recover() }()
			c := s.Unitfiles.GetConf(unit)
			if s.Unitfiles.NotUserConf(c) {
				return
			}
			result[unit] = c
			enabled[unit] = s.EnabledFrom(c)
		}()
	}
	sorted := sortedKeys2(result)
	var out [][2]string
	for _, unit := range sorted {
		if result[unit] != nil {
			out = append(out, [2]string{unit, enabled[unit]})
		}
	}
	return out
}

// ListTargetUnitFiles returns (unit, enabled-state) for target units.
func (s *Systemctl) ListTargetUnitFiles(modules ...string) [][2]string {
	enabled := map[string]string{}
	targets := map[string]string{}

	for _, pair := range s.Unitfiles.EachTargetFile() {
		target, _ := pair[0], pair[1]
		targets[target] = "file"
		enabled[target] = "static"
	}
	for _, unit := range types.AllCommonTargets {
		targets[unit] = "common"
		enabled[unit] = "static"
		for _, u := range types.AllCommonEnabled {
			if u == unit {
				enabled[unit] = "enabled"
			}
		}
		for _, u := range types.AllCommonDisabled {
			if u == unit {
				enabled[unit] = "disabled"
			}
		}
	}
	sorted := func() []string {
		ks := make([]string, 0, len(targets))
		for k := range targets {
			ks = append(ks, k)
		}
		sortStrings(ks)
		return ks
	}()
	out := make([][2]string, len(sorted))
	for i, unit := range sorted {
		out[i] = [2]string{unit, enabled[unit]}
	}
	return out
}

// ListUnitFilesModules combines target and service unit file listings.
func (s *Systemctl) ListUnitFilesModules(modules ...string) [][2]string {
	var result [][2]string
	if s.now > 0 {
		for _, triple := range s.Unitfiles.ListAll() {
			result = append(result, [2]string{triple[0], triple[1] + " " + triple[2]})
		}
	} else if len(s.onlyType) > 0 {
		for _, t := range s.onlyType {
			if t == "target" {
				result = append(result, s.ListTargetUnitFiles()...)
			}
			if t == "service" {
				result = append(result, s.ListServiceUnitFiles()...)
			}
		}
	} else {
		result = s.ListTargetUnitFiles()
		result = append(result, s.ListServiceUnitFiles(modules...)...)
	}
	if s.noLegend {
		return result
	}
	found := fmt.Sprintf("%d unit files listed.", len(result))
	return append([][2]string{{"UNIT FILE", "STATE"}}, append(result, [2]string{"", ""}, [2]string{found, ""})...)
}

// ── PID file helpers ───────────────────────────────────────────────────────

// ReadPIDFile reads the first numeric line from a PID file.
func (s *Systemctl) ReadPIDFile(pidFile string, defaultPID *int) *int {
	if pidFile == "" || !fileExists(pidFile) {
		return defaultPID
	}
	if s.TruncateOld(pidFile) {
		return defaultPID
	}
	f, err := os.Open(pidFile)
	if err != nil {
		logg.Warnf("bad read of pid file '%s' >> %v", pidFile, err)
		return defaultPID
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			if n, err := strconv.Atoi(line); err == nil {
				return &n
			}
			break
		}
	}
	return defaultPID
}

// WaitPIDFile waits up to timeout seconds for the PID file to appear.
func (s *Systemctl) WaitPIDFile(pidFile string, timeout int) *int {
	if timeout <= 0 {
		timeout = types.DefaultTimeoutStartSec / 2
	}
	if timeout < types.MinimumTimeoutStartSec {
		timeout = types.MinimumTimeoutStartSec
	}
	dirpath := filepath.Dir(filepath.Clean(pidFile))
	for attempt := 0; attempt < timeout; attempt++ {
		logg.Debugf("%s wait pid file %s", utils.Delayed(attempt, "."), pidFile)
		if _, err := os.Stat(dirpath); err != nil {
			time.Sleep(time.Second)
			continue
		}
		pid := s.ReadPIDFile(pidFile, nil)
		if pid == nil {
			time.Sleep(time.Second)
			continue
		}
		if !process.PidExists(*pid) {
			time.Sleep(time.Second)
			continue
		}
		return pid
	}
	return nil
}

func (s *Systemctl) GetStatusPIDFile(unit string) string {
	c := s.Unitfiles.GetConf(unit)
	pf := s.PidFileFrom(c, "")
	if pf != "" {
		return pf
	}
	return s.GetStatusFileFrom(c)
}

func (s *Systemctl) PidFileFrom(c *conf.Conf, defaultVal string) string {
	pidFile := c.Get(types.SectionService, "PIDFile", defaultVal, true)
	if pidFile == "" {
		return ""
	}
	return paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(pidFile, c))
}

func (s *Systemctl) ReadMainpidFrom(c *conf.Conf, defaultPID *int) *int {
	pidFile := s.PidFileFrom(c, "")
	if pidFile != "" {
		return s.ReadPIDFile(pidFile, defaultPID)
	}
	status := s.ReadStatusFrom(c)
	if v, ok := status["MainPID"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return &n
		}
	}
	return defaultPID
}

func (s *Systemctl) CleanPidFileFrom(c *conf.Conf) {
	pidFile := s.PidFileFrom(c, "")
	if pidFile != "" && fileExists(pidFile) {
		if err := os.Remove(pidFile); err != nil {
			logg.Warnf("while rm %s >> %v", pidFile, err)
		}
	}
	s.WriteStatusFrom(c, map[string]interface{}{"MainPID": nil})
}

// ── status file helpers ────────────────────────────────────────────────────

func (s *Systemctl) GetStatusFile(unit string) string {
	return s.GetStatusFileFrom(s.Unitfiles.GetConf(unit))
}

func (s *Systemctl) GetStatusFileFrom(c *conf.Conf) string {
	sf := s.getStatusFileValue(c)
	return paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(sf, c))
}

func (s *Systemctl) getStatusFileValue(c *conf.Conf) string {
	sf := c.Get(types.SectionService, "StatusFile", "", true)
	if sf != "" {
		return sf
	}
	root := c.RootMode()
	folder := paths.GetPID_DIR(root)
	return filepath.Join(folder, c.Name()+".status")
}

func (s *Systemctl) CleanStatusFrom(c *conf.Conf) {
	sf := s.GetStatusFileFrom(c)
	if fileExists(sf) {
		_ = os.Remove(sf)
	}
	c.Status = map[string]string{}
}

// WriteStatusFrom persists the status key-value pairs to the status file.
func (s *Systemctl) WriteStatusFrom(c *conf.Conf, updates map[string]interface{}) bool {
	statusFile := s.GetStatusFileFrom(c)
	dirpath := filepath.Dir(filepath.Clean(statusFile))
	if err := os.MkdirAll(dirpath, 0o755); err != nil {
		logg.Errorf("[status] cannot create dir %s >> %v", dirpath, err)
		return false
	}
	if c.Status == nil {
		c.Status = s.ReadStatusFrom(c)
	}
	for key, val := range updates {
		// normalise common short-hand keys
		switch strings.ToUpper(key) {
		case "AS":
			key = "ActiveState"
		case "EXIT":
			key = "ExecMainCode"
		}
		if val == nil {
			delete(c.Status, key)
		} else {
			c.Status[key] = utils.StrE(val)
		}
	}
	f, err := os.Create(statusFile)
	if err != nil {
		logg.Errorf("[status] writing STATUS %v >> %v to %s", updates, err, statusFile)
		return false
	}
	defer f.Close()
	sorted := sortedStringKeys(c.Status)
	for _, key := range sorted {
		val := c.Status[key]
		if key == "MainPID" && val == "0" {
			logg.Warnf("[status] ignore writing MainPID=0")
			continue
		}
		line := fmt.Sprintf("%s=%s\n", key, val)
		logg.Debugf("[status] writing to %s\n\t%s", statusFile, strings.TrimSpace(line))
		_, _ = f.WriteString(line)
	}
	return true
}

// ReadStatusFrom reads the status file into a map.
func (s *Systemctl) ReadStatusFrom(c *conf.Conf) map[string]string {
	statusFile := s.GetStatusFileFrom(c)
	status := map[string]string{}
	if !fileExists(statusFile) {
		logg.Log(types.DebugStatus, "[status] no status file: %s", statusFile)
		return status
	}
	if s.TruncateOld(statusFile) {
		logg.Log(types.DebugStatus, "[status] old status file: %s", statusFile)
		return status
	}
	f, err := os.Open(statusFile)
	if err != nil {
		logg.Warnf("[status] bad read of status file '%s' >> %v", statusFile, err)
		return status
	}
	defer f.Close()
	re := regexp.MustCompile(`(\w+)[=:](.*)`)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m != nil {
			status[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
		}
	}
	return status
}

func (s *Systemctl) GetStatusFrom(c *conf.Conf, name, defaultVal string) string {
	if c.Status == nil {
		c.Status = s.ReadStatusFrom(c)
	}
	if v, ok := c.Status[name]; ok {
		return v
	}
	return defaultVal
}

func (s *Systemctl) SetStatusFrom(c *conf.Conf, name, value string) {
	if c.Status == nil {
		c.Status = s.ReadStatusFrom(c)
	}
	if value == "" {
		delete(c.Status, name)
	} else {
		c.Status[name] = value
	}
}

// ── boottime ──────────────────────────────────────────────────────────────

func (s *Systemctl) GetBoottime() float64 {
	if s.boottime == nil {
		bt := s.getBoottimeFromProc()
		s.boottime = &bt
	}
	return *s.boottime
}

func (s *Systemctl) getBoottimeFromProc() float64 {
	pid1 := types.BootPIDMin
	pidMax := types.BootPIDMax
	if pidMax < 0 {
		pidMax = pid1 - pidMax
	}
	for pid := pid1; pid < pidMax; pid++ {
		proc := fmt.Sprintf(types.ProcPIDStatFmt, pid)
		if _, err := os.Stat(proc); err == nil {
			bt, err := pathProcStarted(proc)
			if err == nil {
				return bt
			}
		}
	}
	return s.getBoottimeFromOldProc()
}

func (s *Systemctl) getBoottimeFromOldProc() float64 {
	booted := float64(time.Now().UnixNano()) / 1e9
	entries, err := os.ReadDir(types.ProcPIDDir)
	if err != nil {
		return booted
	}
	for _, e := range entries {
		proc := fmt.Sprintf(types.ProcPIDStatFmt, e.Name())
		bt, err := pathProcStarted(proc)
		if err == nil && bt < booted {
			booted = bt
		}
	}
	return booted
}

func pathProcStarted(proc string) (float64, error) {
	f, err := os.Open(proc)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return 0, fmt.Errorf("short proc stat")
	}
	startedTicks, err := strconv.ParseFloat(fields[21], 64)
	if err != nil {
		return 0, err
	}
	clockTicks := float64(100) // SC_CLK_TCK default
	startedSecs := startedTicks / clockTicks

	uptime, err := os.ReadFile(types.ProcSysUptime)
	if err != nil {
		return 0, err
	}
	uptimeSecs, err := strconv.ParseFloat(strings.Fields(string(uptime))[0], 64)
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	_ = now
	_ = uptimeSecs
	_ = startedSecs

	// Read btime from /proc/stat
	statData, err := os.ReadFile(types.ProcSysStat)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(statData), "\n") {
		if strings.HasPrefix(line, "btime") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				btime, err := strconv.ParseFloat(fields[1], 64)
				if err == nil {
					return btime + startedSecs, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("btime not found")
}

func (s *Systemctl) GetFiletime(filename string) float64 {
	fi, err := os.Stat(filename)
	if err != nil {
		return 0
	}
	return float64(fi.ModTime().UnixNano()) / 1e9
}

// TruncateOld returns true and truncates the file if it predates the boot time.
func (s *Systemctl) TruncateOld(filename string) bool {
	filetime := s.GetFiletime(filename)
	boottime := s.GetBoottime()
	if filetime >= boottime {
		return false
	}
	if err := process.ShutilTruncate(filename); err != nil {
		logg.Warnf("while truncating >> %v", err)
	}
	return true
}

// Getsize returns the file size, or 0 if the file is old / missing.
func (s *Systemctl) Getsize(filename string) int64 {
	if filename == "" || !fileExists(filename) {
		return 0
	}
	if s.TruncateOld(filename) {
		return 0
	}
	fi, err := os.Stat(filename)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ── command / environment display helpers ─────────────────────────────────

// CommandOfUnit returns the ExecStart value(s) for the unit.
func (s *Systemctl) CommandOfUnit(unit string) []string {
	c := s.Unitfiles.LoadConf(unit)
	if c == nil {
		logg.Errorf("Unit %s not found.", unit)
		s.Error |= types.NotFound
		return nil
	}
	if len(s.onlyProperty) > 0 {
		var found []string
		for _, prop := range s.onlyProperty {
			found = append(found, c.GetList(types.SectionService, prop, nil, true)...)
		}
		return found
	}
	return c.GetList(types.SectionService, "ExecStart", nil, true)
}

// EnvironmentOfUnit returns the resolved environment for the unit.
func (s *Systemctl) EnvironmentOfUnit(unit string) map[string]string {
	c := s.Unitfiles.LoadConf(unit)
	if c == nil {
		logg.Errorf("Unit %s not found.", unit)
		s.Error |= types.NotFound
		return nil
	}
	return s.Unitfiles.GetEnv(c)
}

// SystemExecEnv returns the init-loop environment as NAME=VALUE strings.
func (s *Systemctl) SystemExecEnv() []string {
	values := s.ExtendExecEnv(map[string]string{})
	result := make([]string, 0, len(values))
	sorted := sortedStringKeys(values)
	for _, name := range sorted {
		result = append(result, name+"="+values[name])
	}
	return result
}

// ExtendExecEnv adds systemd-specific vars to the given env map.
func (s *Systemctl) ExtendExecEnv(env map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range env {
		result[k] = v
	}
	// Add PATH if not set
	if _, ok := result["PATH"]; !ok {
		result["PATH"] = types.DefaultPath
	}
	// Reset locale vars
	for _, key := range types.ResetLocale {
		result[key] = ""
	}
	// Read locale.conf
	pairs, err := conf.ReadEnvFile(types.LocaleConf, s.root)
	if err == nil {
		for _, kv := range pairs {
			result[kv[0]] = kv[1]
		}
	}
	return result
}

// ── active / substate checking ────────────────────────────────────────────

// GetActiveFrom returns the active state string for a conf.
func (s *Systemctl) GetActiveFrom(c *conf.Conf) string {
	if c == nil {
		return "unknown"
	}
	unitType := utils.GetUnitType(c.Name())
	if unitType == "target" {
		return s.GetActiveTargetFrom(c)
	}
	return s.GetActiveServiceFrom(c)
}

// GetActiveServiceFrom checks whether the service is running.
func (s *Systemctl) GetActiveServiceFrom(c *conf.Conf) string {
	pid := s.ReadMainpidFrom(c, nil)
	if pid != nil && process.PidExists(*pid) {
		return "active"
	}
	status := s.ReadStatusFrom(c)
	if as, ok := status["ActiveState"]; ok {
		return as
	}
	if types.ActiveIfEnabled {
		if s.EnabledFrom(c) == "enabled" {
			return "active"
		}
	}
	return "inactive"
}

// GetActiveTargetFrom checks active state for a target unit.
func (s *Systemctl) GetActiveTargetFrom(c *conf.Conf) string {
	return s.GetActiveTarget(c.Name())
}

// GetActiveTarget returns the active state of a target.
func (s *Systemctl) GetActiveTarget(target string) string {
	for _, active := range s.GetActiveTargetList() {
		if active == target {
			return "active"
		}
	}
	return "inactive"
}

// GetActiveTargetList returns currently active targets.
func (s *Systemctl) GetActiveTargetList() []string {
	var result []string
	status := s.ReadStatusFrom(s.Unitfiles.GetConf(s.defaultTarget))
	if as := status["ActiveState"]; as == "active" {
		for _, target := range s.Unitfiles.GetTargetList(s.defaultTarget) {
			result = append(result, target)
		}
	}
	return result
}

// GetSubstateFrom returns the sub-state string for a conf.
func (s *Systemctl) GetSubstateFrom(c *conf.Conf) string {
	unitType := utils.GetUnitType(c.Name())
	switch unitType {
	case "target":
		if s.GetActiveTargetFrom(c) == "active" {
			return "active"
		}
		return "dead"
	case "socket":
		pid := s.ReadMainpidFrom(c, nil)
		if pid != nil && process.PidExists(*pid) {
			return "running"
		}
		return "dead"
	}
	// service
	pid := s.ReadMainpidFrom(c, nil)
	if pid != nil {
		if process.PidExists(*pid) {
			if process.PidZombie(*pid) {
				return "zombie"
			}
			return "running"
		}
	}
	status := s.ReadStatusFrom(c)
	if ss, ok := status["SubState"]; ok && ss != "" {
		return ss
	}
	if as, ok := status["ActiveState"]; ok {
		return as
	}
	return "dead"
}

// IsActiveFrom returns true when the unit is active.
func (s *Systemctl) IsActiveFrom(c *conf.Conf) bool {
	return s.GetActiveFrom(c) == "active"
}

// ActivePidFrom returns the main PID of an active service, or 0.
func (s *Systemctl) ActivePidFrom(c *conf.Conf) int {
	if !s.IsActiveFrom(c) {
		return 0
	}
	pid := s.ReadMainpidFrom(c, nil)
	if pid == nil {
		return 0
	}
	return *pid
}

// IsActivePid returns true if the PID belongs to an active process.
func (s *Systemctl) IsActivePid(pid int) bool {
	return process.PidExists(pid) && !process.PidZombie(pid)
}

// ── is-active / is-failed ─────────────────────────────────────────────────

// IsActiveModules checks whether each module is active and returns exit code.
func (s *Systemctl) IsActiveModules(modules ...string) ([]string, int) {
	var result []string
	found := 0
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		active := s.GetActiveFrom(c)
		result = append(result, active)
		if active == "active" {
			found++
		}
	}
	if found > 0 {
		return result, 0
	}
	return result, types.NotActive
}

// IsFailedModules checks whether any module is in a failed state.
func (s *Systemctl) IsFailedModules(modules ...string) ([]string, int) {
	var result []string
	found := 0
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		active := s.GetActiveFrom(c)
		result = append(result, active)
		if active == "failed" {
			found++
		}
	}
	if found > 0 {
		return result, types.NotOK
	}
	return result, 0
}

// ResetFailedFrom clears the failed status for a conf.
func (s *Systemctl) ResetFailedFrom(c *conf.Conf) bool {
	if c == nil {
		return true
	}
	pid := s.ReadMainpidFrom(c, nil)
	if pid != nil {
		delete(s.restartedUnit, c.Name())
	}
	status := s.ReadStatusFrom(c)
	if status["ActiveState"] == "failed" {
		s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
	}
	return true
}

// ── enabled state ─────────────────────────────────────────────────────────

// EnabledFrom returns the enabled state string for a conf.
func (s *Systemctl) EnabledFrom(c *conf.Conf) string {
	if c == nil {
		return "unknown"
	}
	if c.Masked != "" {
		return "masked"
	}
	if c.IsLoaded() == "" {
		return "not-found"
	}
	unitName := c.Name()
	// Check via symlinks in wants/requires directories
	for _, folder := range s.Unitfiles.UnitFileFolders() {
		if folder == "" {
			continue
		}
		folder = s.Unitfiles.OsPath(folder)
		for _, target := range types.AllCommonTargets {
			wantsDir := filepath.Join(folder, target+".wants")
			link := filepath.Join(wantsDir, unitName)
			if _, err := os.Lstat(link); err == nil {
				return "enabled"
			}
		}
	}
	// Check via preset
	preset := s.Unitfiles.GetPresetOfUnit(unitName)
	if preset == "enable" {
		return "enabled"
	}
	if preset == "disable" {
		return "disabled"
	}
	// Check WantedBy in [Install]
	targets := s.Unitfiles.GetInstallTargets(c, types.SectionInstall, "")
	if len(targets) == 0 {
		return "static"
	}
	return "disabled"
}

// ── status display ────────────────────────────────────────────────────────

// StatusUnits returns status lines for each unit.
func (s *Systemctl) StatusUnits(units_ []string) []string {
	var result []string
	for _, unit := range units_ {
		result = append(result, s.statusUnit(unit)...)
	}
	return result
}

func (s *Systemctl) statusUnit(unit string) []string {
	c := s.Unitfiles.GetConf(unit)
	var lines []string
	lines = append(lines, "# "+unit)
	description := s.Unitfiles.GetDescription(c, types.SectionUnit, "")
	lines = append(lines, fmt.Sprintf("   Loaded: %s (%s)", c.IsLoaded(), c.Filename()))
	if description != "" {
		lines = append(lines, fmt.Sprintf("   Desc:   %s", description))
	}
	active := s.GetActiveFrom(c)
	substate := s.GetSubstateFrom(c)
	lines = append(lines, fmt.Sprintf("   Active: %s (%s)", active, substate))
	pid := s.ReadMainpidFrom(c, nil)
	if pid != nil {
		lines = append(lines, fmt.Sprintf("   CGroup: /system.slice/%s", unit))
		lines = append(lines, fmt.Sprintf("           %d %s", *pid, unit))
	}
	return lines
}

// CatUnits returns the raw content of each unit file.
func (s *Systemctl) CatUnits(units_ []string) []string {
	var result []string
	for _, unit := range units_ {
		c := s.Unitfiles.GetConf(unit)
		filename := c.Filename()
		if filename == "" {
			logg.Errorf("# %s not loaded", unit)
			result = append(result, "# "+unit+" not loaded")
			continue
		}
		result = append(result, "# "+filename)
		data, err := os.ReadFile(filename)
		if err != nil {
			logg.Errorf("# could not read %s", filename)
			continue
		}
		result = append(result, string(data))
	}
	return result
}

// ── enable / disable / preset ─────────────────────────────────────────────

// EnableModules enables the listed units and returns (changed-list, ok).
func (s *Systemctl) EnableModules(modules ...string) ([]string, bool) {
	var result []string
	for _, unit := range modules {
		ok := s.enableUnit(unit)
		if ok {
			result = append(result, unit)
		}
	}
	return result, true
}

func (s *Systemctl) enableUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	if c.IsLoaded() == "" {
		logg.Errorf("Unit %s not found.", unit)
		s.Error |= types.NotFound
		return false
	}
	targets := s.Unitfiles.GetInstallTargets(c, types.SectionInstall, "")
	if len(targets) == 0 {
		logg.Warnf("Unit %s has no [Install] WantedBy", unit)
		return false
	}
	for _, target := range targets {
		folder := s.systemFolder()
		if folder == "" {
			continue
		}
		wantsDir := filepath.Join(folder, target+".wants")
		if err := os.MkdirAll(wantsDir, 0o755); err != nil {
			logg.Errorf("enable: mkdir %s >> %v", wantsDir, err)
			continue
		}
		link := filepath.Join(wantsDir, filepath.Base(c.Filename()))
		target_ := c.Filename()
		if _, err := os.Lstat(link); err == nil {
			if !s.force {
				logg.Debugf("enable: link already exists: %s", link)
				continue
			}
			_ = os.Remove(link)
		}
		if err := os.Symlink(target_, link); err != nil {
			logg.Errorf("enable: symlink %s -> %s >> %v", link, target_, err)
		} else {
			logg.Debugf("enable: %s => %s", link, target_)
		}
	}
	return true
}

// DisableModules disables the listed units.
func (s *Systemctl) DisableModules(modules ...string) ([]string, bool) {
	var result []string
	for _, unit := range modules {
		ok := s.disableUnit(unit)
		if ok {
			result = append(result, unit)
		}
	}
	return result, true
}

func (s *Systemctl) disableUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	for _, folder := range s.Unitfiles.UnitFileFolders() {
		if folder == "" {
			continue
		}
		folder = s.Unitfiles.OsPath(folder)
		entries, err := os.ReadDir(folder)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".wants") && !strings.HasSuffix(e.Name(), ".requires") {
				continue
			}
			link := filepath.Join(folder, e.Name(), filepath.Base(c.Filename()))
			target_, err := os.Readlink(link)
			if err != nil {
				continue
			}
			if filepath.Base(target_) == filepath.Base(c.Filename()) {
				logg.Debugf("disable: rm %s", link)
				_ = os.Remove(link)
			}
		}
	}
	return true
}

// IsEnabledModules checks the enabled state of the listed modules.
func (s *Systemctl) IsEnabledModules(modules ...string) ([]string, int) {
	var lines []string
	found := 0
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		state := s.EnabledFrom(c)
		lines = append(lines, state)
		if state == "enabled" || state == "static" {
			found++
		}
	}
	if found > 0 {
		return lines, 0
	}
	return lines, types.NotOK
}

// PresetModules applies preset rules to each module.
func (s *Systemctl) PresetModules(modules ...string) bool {
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		preset := s.Unitfiles.GetPresetOfUnit(unit)
		switch {
		case preset == "enable" && s.presetMode != "disable":
			s.EnableModules(unit)
		case preset == "disable" && s.presetMode != "enable":
			s.DisableModules(unit)
		}
	}
	return true
}

// PresetAllModules applies all preset rules.
func (s *Systemctl) PresetAllModules() bool {
	return s.PresetModules()
}

// ── daemon-reload / halt / reboot stubs ──────────────────────────────────

// DaemonReload reloads the unit file database.
func (s *Systemctl) DaemonReload() bool {
	s.Unitfiles.ScanUnitFiles(true)
	s.Unitfiles.ScanSysvFiles(true)
	return true
}

// DaemonReexec is a no-op in the emulator.
func (s *Systemctl) DaemonReexec() bool { return true }

// HaltModules sends SIGTERM then SIGKILL to all running services.
func (s *Systemctl) HaltModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		if !s.doStopUnitFrom(c) {
			ok = false
		}
	}
	return ok
}

// RebootModules is an alias for HaltModules in the emulator.
func (s *Systemctl) RebootModules(modules ...string) bool {
	return s.HaltModules(modules...)
}

// ── show / property helpers ───────────────────────────────────────────────

// ShowModules returns property info for the listed modules.
func (s *Systemctl) ShowModules(modules ...string) []string {
	var result []string
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		if len(s.onlyProperty) > 0 {
			for _, prop := range s.onlyProperty {
				v := c.Get("", prop, "", true)
				if v != "" {
					result = append(result, prop+"="+v)
				}
			}
		} else {
			// default: show all loaded properties
			for _, section := range c.Data.Sections() {
				for _, line := range c.GetList(section, "", nil, true) {
					_ = line
				}
			}
			result = append(result, "Id="+c.Name())
			result = append(result, "ActiveState="+s.GetActiveFrom(c))
		}
	}
	return result
}

// ── misc commands ─────────────────────────────────────────────────────────

// Version returns the emulated systemd version string.
func (s *Systemctl) Version() string {
	return fmt.Sprintf("systemd %d\n+PAM +AUDIT +SELINUX +IMA +SYSVINIT +LIBCRYPTSETUP +GCRYPT +ACL +XZ\n", s.systemdVersion)
}

// ListPaths shows key directory paths.
func (s *Systemctl) ListPaths() []string {
	var result []string
	root := !types.UserMode
	result = append(result, "ConfDir="+paths.GetCONFIG_HOME(root))
	result = append(result, "RunDir="+paths.GetRUN(root))
	result = append(result, "LogDir="+paths.GetLOG_DIR(root))
	return result
}

// Help returns a brief command listing.
func (s *Systemctl) Help() string {
	return `systemctl [OPTIONS] COMMAND [UNIT...]
Commands: start stop reload restart try-restart kill status list-units
  list-unit-files enable disable is-enabled is-active is-failed preset
  cat show daemon-reload reset-failed version help`
}

// ── helpers ───────────────────────────────────────────────────────────────

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sortStrings(ks)
	return ks
}

func sortedKeys2(m map[string]*conf.Conf) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sortStrings(ks)
	return ks
}

func sortedStringKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sortStrings(ks)
	return ks
}

func sortStrings(ss []string) {
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			if ss[i] > ss[j] {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
	}
}

func (s *Systemctl) systemFolder() string {
	folders := s.Unitfiles.SystemFolders()
	if len(folders) > 0 {
		return s.Unitfiles.OsPath(folders[0])
	}
	return ""
}

// doStopUnitFrom is a minimal stop stub; the full implementation is in
// systemctl_exec.go (to be written in Session 2).
func (s *Systemctl) doStopUnitFrom(c *conf.Conf) bool {
	pid := s.ReadMainpidFrom(c, nil)
	if pid == nil {
		return true
	}
	if !process.PidExists(*pid) {
		return true
	}
	if err := killProcess(*pid, 15); err != nil { // SIGTERM
		logg.Warnf("stop %s: SIGTERM pid %d >> %v", c.Name(), *pid, err)
	}
	time.Sleep(500 * time.Millisecond)
	if process.PidExists(*pid) {
		if err := killProcess(*pid, 9); err != nil { // SIGKILL
			logg.Warnf("stop %s: SIGKILL pid %d >> %v", c.Name(), *pid, err)
		}
	}
	s.CleanPidFileFrom(c)
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
	return true
}

func killProcess(pid, sig int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(processSignal(sig))
}

func processSignal(sig int) os.Signal {
	switch sig {
	case 9:
		return os.Kill
	default:
		return os.Interrupt
	}
}


