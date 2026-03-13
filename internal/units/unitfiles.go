// Package units implements the unit-file database: scanning directories,
// loading / caching unit configurations, preset handling and the expand_special
// / expand_env helpers used throughout the service-control logic.
package units

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"systemctl-go/internal/conf"
	"systemctl-go/internal/logger"
	"systemctl-go/internal/paths"
	"systemctl-go/internal/types"
	"systemctl-go/internal/utils"
)

var logg = logger.GetLogger("units")

// ── UnitFiles ─────────────────────────────────────────────────────────────

// UnitFiles is the central database of loaded unit descriptors.
type UnitFiles struct {
	Root             string
	UserMode         bool
	userGetlogin     string
	extraVars        []string
	systemdUnitPath  string // lazy; "" means "not yet loaded"
	systemdSysvPath  string
	systemdPresetPath string

	loadedUnitFiles float64 // time.Time as Unix float
	loadedSysvFiles float64

	loadedSysvConf map[string]*conf.Conf // path -> Conf
	loadedUnitConf map[string]*conf.Conf // path -> Conf
	fileForSysv    map[string]string     // name.service -> /etc/init.d/name
	fileForUnit    map[string]string     // name.service -> /etc/systemd/system/name.service

	presetFileList map[string]*conf.PresetFile // name -> PresetFile
}

// NewUnitFiles constructs a UnitFiles for the given root directory.
func NewUnitFiles(root string) *UnitFiles {
	u := &UnitFiles{
		Root:           root,
		UserMode:       types.UserMode,
		userGetlogin:   paths.OsGetlogin(),
		extraVars:      append([]string{}, types.ExtraVars...),
		loadedSysvConf: map[string]*conf.Conf{},
		loadedUnitConf: map[string]*conf.Conf{},
		fileForSysv:    map[string]string{},
		fileForUnit:    map[string]string{},
	}
	return u
}

// OsPath applies the root prefix to a path.
func (u *UnitFiles) OsPath(p string) string { return paths.OsPath(u.Root, p) }

// User returns the login name of the effective user.
func (u *UnitFiles) User() string { return u.userGetlogin }

// UserModeEnabled returns whether user-mode is active.
func (u *UnitFiles) UserModeEnabled() bool { return u.UserMode }

// ── SYSTEMD_* path getters ────────────────────────────────────────────────

func (u *UnitFiles) getSystemdUnitPath() string {
	if u.systemdUnitPath == "" {
		u.systemdUnitPath = envOr("SYSTEMD_UNIT_PATH", ":")
	}
	return u.systemdUnitPath
}

func (u *UnitFiles) getSystemdSysvPath() string {
	if u.systemdSysvPath == "" {
		u.systemdSysvPath = envOr("SYSTEMD_SYSVINIT_PATH", ":")
	}
	return u.systemdSysvPath
}

func (u *UnitFiles) getSystemdPresetPath() string {
	if u.systemdPresetPath == "" {
		u.systemdPresetPath = envOr("SYSTEMD_PRESET_PATH", ":")
	}
	return u.systemdPresetPath
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── folder iterators ──────────────────────────────────────────────────────

func (u *UnitFiles) expandFolders(envPath string, fallback []string) []string {
	var result []string
	for _, p := range strings.Split(envPath, ":") {
		if p != "" {
			result = append(result, paths.ExpandPath(strings.TrimSpace(p), !u.UserMode))
		}
	}
	if strings.HasSuffix(envPath, ":") {
		for _, p := range fallback {
			result = append(result, paths.ExpandPath(strings.TrimSpace(p), !u.UserMode))
		}
	}
	return result
}

func (u *UnitFiles) SystemFolders() []string {
	return u.expandFolders(u.getSystemdUnitPath(), types.SystemFolders)
}

func (u *UnitFiles) UserFolders() []string {
	return u.expandFolders(u.getSystemdUnitPath(), types.UserFolders)
}

func (u *UnitFiles) InitFolders() []string {
	return u.expandFolders(u.getSystemdSysvPath(), types.InitFolders)
}

func (u *UnitFiles) PresetFolders() []string {
	return u.expandFolders(u.getSystemdPresetPath(), types.PresetFolders)
}

func (u *UnitFiles) UnitFileFolders() []string {
	var result []string
	if u.UserMode {
		result = append(result, u.UserFolders()...)
	}
	result = append(result, u.SystemFolders()...)
	return result
}

// ── file scanning ─────────────────────────────────────────────────────────

// ScanUnitFiles loads the file-path index for all unit files.
func (u *UnitFiles) ScanUnitFiles(reload bool) []string {
	if u.loadedUnitFiles == 0 || reload {
		u.loadedUnitFiles = float64(time.Now().UnixNano()) / 1e9
		found := 0
		for _, folder := range u.UnitFileFolders() {
			if folder == "" {
				continue
			}
			folder = u.OsPath(folder)
			entries, err := os.ReadDir(folder)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				path := filepath.Join(folder, e.Name())
				found = u.addUnitFile(e.Name(), path)
			}
		}
		logg.Debugf("found %d unit files", found)
	}
	keys := make([]string, 0, len(u.fileForUnit))
	for k := range u.fileForUnit {
		keys = append(keys, k)
	}
	return keys
}

// ScanSysvFiles loads the file-path index for all SysV init scripts.
func (u *UnitFiles) ScanSysvFiles(reload bool) []string {
	if u.loadedSysvFiles == 0 || reload {
		u.loadedSysvFiles = float64(time.Now().UnixNano()) / 1e9
		found := 0
		for _, folder := range u.InitFolders() {
			if folder == "" {
				continue
			}
			folder = u.OsPath(folder)
			entries, err := os.ReadDir(folder)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				path := filepath.Join(folder, e.Name())
				found = u.addSysvFile(e.Name(), path)
			}
		}
		logg.Debugf("found %d sysv files", found)
	}
	keys := make([]string, 0, len(u.fileForSysv))
	for k := range u.fileForSysv {
		keys = append(keys, k)
	}
	return keys
}

func (u *UnitFiles) addUnitFile(name, path string) int {
	if _, ok := u.fileForUnit[name]; !ok {
		u.fileForUnit[name] = path
	}
	return len(u.fileForUnit)
}

func (u *UnitFiles) addSysvFile(name, path string) int {
	serviceName := name + ".service"
	if _, ok := u.fileForSysv[serviceName]; !ok {
		u.fileForSysv[serviceName] = path
	}
	return len(u.fileForSysv)
}

// ── unit file lookups ─────────────────────────────────────────────────────

// GetUnitFile returns the filesystem path for a systemd unit file.
func (u *UnitFiles) GetUnitFile(module string) string {
	u.ScanUnitFiles(false)
	if module != "" {
		if p, ok := u.fileForUnit[module]; ok {
			return p
		}
		if p, ok := u.fileForUnit[utils.UnitOf(module)]; ok {
			return p
		}
	}
	return ""
}

// GetSysvFile returns the filesystem path for a SysV init script.
func (u *UnitFiles) GetSysvFile(module string) string {
	u.ScanSysvFiles(false)
	if module != "" {
		if p, ok := u.fileForSysv[module]; ok {
			return p
		}
		if p, ok := u.fileForSysv[utils.UnitOf(module)]; ok {
			return p
		}
	}
	return ""
}

// UnitFile returns the first matching file (systemd preferred over sysv).
func (u *UnitFiles) UnitFile(module string) string {
	if p := u.GetUnitFile(module); p != "" {
		return p
	}
	return u.GetSysvFile(module)
}

// IsSysvFile returns true if filename belongs to the sysv index, false if
// systemd, or an error if not found.
func (u *UnitFiles) IsSysvFile(filename string) (bool, bool) {
	u.UnitFile("") // scan all
	if filename == "" {
		return false, false
	}
	for _, v := range u.fileForUnit {
		if v == filename {
			return false, true
		}
	}
	for _, v := range u.fileForSysv {
		if v == filename {
			return true, true
		}
	}
	return false, false
}

// IsUserConf returns true when the conf path indicates a user-scoped unit.
func (u *UnitFiles) IsUserConf(c *conf.Conf) bool {
	if c == nil {
		return false
	}
	filename := c.NonloadedPath
	if filename == "" {
		filename = c.Filename()
	}
	return strings.Contains(filename, "/user/")
}

// NotUserConf returns true when a conf cannot be started as a user service.
func (u *UnitFiles) NotUserConf(c *conf.Conf) bool {
	if c == nil {
		return true
	}
	if !u.UserMode {
		return false
	}
	if u.IsUserConf(c) {
		return false
	}
	userVal := u.GetUser(c)
	if userVal != "" && userVal == u.User() {
		return false
	}
	return true
}

// ── drop-in files ─────────────────────────────────────────────────────────

// FindDropInFiles searches for <unit>.d/*.conf drop-in overrides.
func (u *UnitFiles) FindDropInFiles(unit string) map[string]string {
	result := map[string]string{}
	basenameD := unit + ".d"
	for _, folder := range u.UnitFileFolders() {
		if folder == "" {
			continue
		}
		folder = u.OsPath(folder)
		overrideD := filepath.Join(folder, basenameD)
		entries, err := os.ReadDir(overrideD)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			path := filepath.Join(overrideD, e.Name())
			if _, ok := result[e.Name()]; !ok {
				result[e.Name()] = path
			}
		}
	}
	return result
}

// ── conf loading ──────────────────────────────────────────────────────────

// LoadUnitTemplateConf loads a template unit conf (e.g. foo@.service).
func (u *UnitFiles) LoadUnitTemplateConf(module string) *conf.Conf {
	if !strings.Contains(module, "@") {
		return nil
	}
	unit := utils.ParseUnit(module)
	service := fmt.Sprintf("%s@.service", unit.Prefix)
	c := u.LoadUnitConf(service)
	if c != nil {
		c.Module = module
	}
	return c
}

// LoadUnitConf reads and caches the unit file for the given module.
func (u *UnitFiles) LoadUnitConf(module string) *conf.Conf {
	path := u.GetUnitFile(module)
	if path == "" {
		return nil
	}
	if c, ok := u.loadedUnitConf[path]; ok {
		return c
	}
	masked := ""
	if link, err := os.Readlink(path); err == nil && strings.HasPrefix(link, "/dev") {
		masked = link
	}
	dropInFiles := map[string]string{}
	parser := conf.NewConfigParser()
	if masked == "" {
		if _, err := parser.ReadUnitFile(path); err != nil {
			logg.Warnf("loading %s: %v", path, err)
		}
		dropInFiles = u.FindDropInFiles(filepath.Base(path))
		names := make([]string, 0, len(dropInFiles))
		for n := range dropInFiles {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if _, err := parser.ReadUnitFile(dropInFiles[n]); err != nil {
				logg.Warnf("loading drop-in %s: %v", dropInFiles[n], err)
			}
		}
	}
	c := conf.NewConf(parser, module)
	c.Masked = masked
	c.NonloadedPath = path
	c.DropInFiles = dropInFiles
	c.Root = u.Root
	u.loadedUnitConf[path] = c
	return c
}

// LoadSysvConf reads and caches a SysV init script as a unit conf.
func (u *UnitFiles) LoadSysvConf(module string) *conf.Conf {
	path := u.GetSysvFile(module)
	if path == "" {
		return nil
	}
	if c, ok := u.loadedSysvConf[path]; ok {
		return c
	}
	parser := conf.NewConfigParser()
	if _, err := parser.ReadSysvFile(path); err != nil {
		logg.Warnf("loading sysv %s: %v", path, err)
	}
	c := conf.NewConf(parser, module)
	c.Root = u.Root
	u.loadedSysvConf[path] = c
	return c
}

// LoadConf loads the conf for a module (systemd first, then template, then sysv).
func (u *UnitFiles) LoadConf(module string) *conf.Conf {
	defer func() {
		if r := recover(); r != nil {
			logg.Warnf("%s not loaded >> %v", module, r)
		}
	}()
	if c := u.LoadUnitConf(module); c != nil {
		return c
	}
	if c := u.LoadUnitTemplateConf(module); c != nil {
		return c
	}
	return u.LoadSysvConf(module)
}

// DefaultConf returns a placeholder conf for units that cannot be found.
func (u *UnitFiles) DefaultConf(module, description string) *conf.Conf {
	if description == "" {
		description = "NOT-FOUND " + module
	}
	parser := conf.NewConfigParser()
	parser.SetStr(types.SectionUnit, "Description", description)
	c := conf.NewConf(parser, module)
	c.Root = u.Root
	return c
}

// GetConf returns the conf for a module, returning a default (not-loaded) conf
// if the unit file cannot be found.
func (u *UnitFiles) GetConf(module string) *conf.Conf {
	if c := u.LoadConf(module); c != nil {
		return c
	}
	return u.DefaultConf(module, "")
}

// ── unit matching ─────────────────────────────────────────────────────────

// MatchUnitTemplates yields units from the template index that match the given modules.
func (u *UnitFiles) MatchUnitTemplates(modules []string) []string {
	if len(modules) == 0 {
		return nil
	}
	u.ScanUnitFiles(false)
	var result []string
	for item := range u.fileForUnit {
		if !strings.Contains(item, "@") {
			continue
		}
		serviceUnit := utils.ParseUnit(item)
		for _, module := range modules {
			if !strings.Contains(module, "@") {
				continue
			}
			moduleUnit := utils.ParseUnit(module)
			if serviceUnit.Prefix == moduleUnit.Prefix {
				candidate := fmt.Sprintf("%s@%s.%s", serviceUnit.Prefix, moduleUnit.Instance, serviceUnit.Suffix)
				result = append(result, candidate)
			}
		}
	}
	return result
}

// MatchUnitFiles yields unit names from the systemd index matching the glob patterns.
func (u *UnitFiles) MatchUnitFiles(modules []string) []string {
	const suffix = ".service"
	u.ScanUnitFiles(false)
	names := make([]string, 0, len(u.fileForUnit))
	for k := range u.fileForUnit {
		names = append(names, k)
	}
	sort.Strings(names)
	var result []string
	for _, item := range names {
		if !strings.Contains(item, ".") {
			continue
		}
		if len(modules) == 0 {
			result = append(result, item)
			continue
		}
		for _, module := range modules {
			if ok, _ := filepath.Match(module, item); ok {
				result = append(result, item)
				break
			}
			if module+suffix == item {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

// MatchSysvFiles yields unit names from the sysv index matching the glob patterns.
func (u *UnitFiles) MatchSysvFiles(modules []string) []string {
	const suffix = ".service"
	u.ScanSysvFiles(false)
	names := make([]string, 0, len(u.fileForSysv))
	for k := range u.fileForSysv {
		names = append(names, k)
	}
	sort.Strings(names)
	var result []string
	for _, item := range names {
		if len(modules) == 0 {
			result = append(result, item)
			continue
		}
		for _, module := range modules {
			if ok, _ := filepath.Match(module, item); ok {
				result = append(result, item)
				break
			}
			if module+suffix == item {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

// MatchUnits returns all units matching the given glob patterns (deduped).
func (u *UnitFiles) MatchUnits(modules []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, unit := range u.MatchUnitFiles(modules) {
		if !seen[unit] {
			seen[unit] = true
			result = append(result, unit)
		}
	}
	for _, unit := range u.MatchUnitTemplates(modules) {
		if !seen[unit] {
			seen[unit] = true
			result = append(result, unit)
		}
	}
	for _, unit := range u.MatchSysvFiles(modules) {
		if !seen[unit] {
			seen[unit] = true
			result = append(result, unit)
		}
	}
	return result
}

// ListAll returns the basic (name, type, path) tuples for all known units.
func (u *UnitFiles) ListAll() [][3]string {
	u.UnitFile("") // ensure scan
	var result [][3]string
	for name, value := range u.fileForUnit {
		result = append(result, [3]string{name, "Unit", value})
	}
	for name, value := range u.fileForSysv {
		result = append(result, [3]string{name, "SysV", value})
	}
	return result
}

// ── target conf helpers ───────────────────────────────────────────────────

// EachTargetFile yields (filename, fullpath) for *.target files in the
// system or user folders.
func (u *UnitFiles) EachTargetFile() [][2]string {
	folders := u.SystemFolders()
	if u.UserMode {
		folders = u.UserFolders()
	}
	var result [][2]string
	for _, folder1 := range folders {
		folder := u.OsPath(folder1)
		entries, err := os.ReadDir(folder)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".target") {
				result = append(result, [2]string{e.Name(), filepath.Join(folder, e.Name())})
			}
		}
	}
	return result
}

// GetTargetConf returns the conf for a target, filling in Requires from the
// built-in target tables if no file is found.
func (u *UnitFiles) GetTargetConf(module string) *conf.Conf {
	if c := u.LoadConf(module); c != nil {
		return c
	}
	targetConf := u.DefaultConf(module, "")
	if req, ok := types.TargetRequires[module]; ok {
		targetConf.SetStr(types.SectionUnit, "Requires", req)
	} else if alias, ok := types.TargetAlias[module]; ok {
		targetConf.SetStr(types.SectionUnit, "Requires", alias)
	}
	return targetConf
}

// GetTargetList returns the chain of targets required by module.
func (u *UnitFiles) GetTargetList(module string) []string {
	target := module
	if !strings.Contains(target, ".") {
		target += ".target"
	}
	targets := []string{target}
	c := u.GetTargetConf(module)
	requires := c.Get(types.SectionUnit, "Requires", "", true)
	if alias, ok := types.TargetAlias[requires]; ok {
		requires = alias
	}
	for {
		next, ok := types.TargetRequires[requires]
		if !ok {
			break
		}
		targets = append([]string{requires}, targets...)
		requires = next
	}
	logg.Debugf("the [%s] requires %v", module, targets)
	return targets
}

// ── getter helpers ────────────────────────────────────────────────────────

func (u *UnitFiles) GetInstallTargets(c *conf.Conf, section, defaultVal string) []string {
	if c == nil {
		if defaultVal != "" {
			return []string{defaultVal}
		}
		return nil
	}
	targets := utils.Wordlist(c.GetList(section, "WantedBy", nil, true))
	if len(targets) == 0 && defaultVal != "" {
		return []string{defaultVal}
	}
	return targets
}

func (u *UnitFiles) GetTimeoutStartSec(c *conf.Conf, section string) float64 {
	dflt := fmt.Sprintf("%d", types.DefaultTimeoutStartSec)
	timeout := c.Get(section, "TimeoutSec", dflt, true)
	timeout = c.Get(section, "TimeoutStartSec", timeout, true)
	return utils.TimeToSeconds(timeout, float64(types.DefaultMaximumTimeout))
}

func (u *UnitFiles) GetSocketTimeoutSec(c *conf.Conf, section string) float64 {
	dflt := fmt.Sprintf("%d", types.DefaultTimeoutStartSec)
	timeout := c.Get(section, "TimeoutSec", dflt, true)
	return utils.TimeToSeconds(timeout, float64(types.DefaultMaximumTimeout))
}

func (u *UnitFiles) GetTimeoutStopSec(c *conf.Conf, section string) float64 {
	dflt := fmt.Sprintf("%d", types.DefaultTimeoutStartSec)
	timeout := c.Get(section, "TimeoutSec", dflt, true)
	timeout = c.Get(section, "TimeoutStopSec", timeout, true)
	return utils.TimeToSeconds(timeout, float64(types.DefaultMaximumTimeout))
}

func (u *UnitFiles) GetRemainAfterExit(c *conf.Conf, section string) bool {
	return c.GetBool(section, "RemainAfterExit", "no")
}

func (u *UnitFiles) GetRuntimeDirectoryPreserve(c *conf.Conf, section string) bool {
	return c.GetBool(section, "RuntimeDirectoryPreserve", "no")
}

func (u *UnitFiles) GetRuntimeDirectory(c *conf.Conf, section string) string {
	return u.ExpandSpecial(c.Get(section, "RuntimeDirectory", "", true), c)
}

func (u *UnitFiles) GetStateDirectory(c *conf.Conf, section string) string {
	return u.ExpandSpecial(c.Get(section, "StateDirectory", "", true), c)
}

func (u *UnitFiles) GetCacheDirectory(c *conf.Conf, section string) string {
	return u.ExpandSpecial(c.Get(section, "CacheDirectory", "", true), c)
}

func (u *UnitFiles) GetLogsDirectory(c *conf.Conf, section string) string {
	return u.ExpandSpecial(c.Get(section, "LogsDirectory", "", true), c)
}

func (u *UnitFiles) GetConfigurationDirectory(c *conf.Conf, section string) string {
	return u.ExpandSpecial(c.Get(section, "ConfigurationDirectory", "", true), c)
}

func (u *UnitFiles) GetWorkingDirectory(c *conf.Conf, section string) string {
	return c.Get(section, "WorkingDirectory", "", true)
}

func (u *UnitFiles) GetSendSIGKILL(c *conf.Conf, section string) bool {
	return c.GetBool(section, "SendSIGKILL", "yes")
}

func (u *UnitFiles) GetSendSIGHUP(c *conf.Conf, section string) bool {
	return c.GetBool(section, "SendSIGHUP", "no")
}

func (u *UnitFiles) GetKillMode(c *conf.Conf, section string) string {
	return c.Get(section, "KillMode", "control-group", true)
}

func (u *UnitFiles) GetKillSignal(c *conf.Conf, section string) string {
	return c.Get(section, "KillSignal", "SIGTERM", true)
}

func (u *UnitFiles) GetStartLimitBurst(c *conf.Conf, section string) int {
	return utils.ToInt(c.Get(section, "StartLimitBurst", fmt.Sprintf("%d", types.DefaultStartLimitBurst), true),
		types.DefaultStartLimitBurst)
}

func (u *UnitFiles) GetStartLimitIntervalSec(c *conf.Conf, section string) float64 {
	maximum := float64(types.DefaultMaximumTimeout * 5)
	dflt := fmt.Sprintf("%d", types.DefaultStartLimitIntervalSec)
	interval := c.Get(section, "StartLimitIntervalSec", dflt, true)
	return utils.TimeToSeconds(interval, maximum)
}

func (u *UnitFiles) GetRestartSec(c *conf.Conf, section string) float64 {
	dflt := fmt.Sprintf("%g", types.DefaultRestartSec)
	delay := c.Get(section, "RestartSec", dflt, true)
	return utils.TimeToSeconds(delay, float64(types.DefaultMaximumTimeout))
}

func (u *UnitFiles) GetDescription(c *conf.Conf, section, defaultVal string) string {
	if c == nil {
		return defaultVal
	}
	description := c.Get(section, "Description", defaultVal, true)
	return u.ExpandSpecial(description, c)
}

func (u *UnitFiles) GetUser(c *conf.Conf) string {
	if c == nil {
		return ""
	}
	return u.ExpandSpecial(c.Get(types.SectionService, "User", "", true), c)
}

func (u *UnitFiles) GetGroup(c *conf.Conf) string {
	if c == nil {
		return ""
	}
	return u.ExpandSpecial(c.Get(types.SectionService, "Group", "", true), c)
}

func (u *UnitFiles) GetSupplementaryGroups(c *conf.Conf) []string {
	groupLines := c.GetList(types.SectionService, "SupplementaryGroups", nil, true)
	return u.expandList(groupLines, c)
}

func (u *UnitFiles) expandList(groupLines []string, c *conf.Conf) []string {
	var result []string
	for _, line := range groupLines {
		for _, item := range strings.Fields(line) {
			if item != "" {
				result = append(result, u.ExpandSpecial(item, c))
			}
		}
	}
	return result
}

// ── ExpandSpecial ─────────────────────────────────────────────────────────

// ExpandSpecial replaces %i, %t, %u, %h and similar systemd specifiers in cmd.
func (u *UnitFiles) ExpandSpecial(cmd string, c *conf.Conf) string {
	if cmd == "" {
		return ""
	}
	confs := u.getConfs(c)
	re := regexp.MustCompile(`%(.)`)
	result := re.ReplaceAllStringFunc(cmd, func(s string) string {
		key := string(s[1])
		if val, ok := confs[key]; ok {
			return val
		}
		logg.Log(types.InfoExpand, "can not expand %%%s", key)
		return ""
	})
	logg.Log(types.DebugExpand, "expanded => %s", result)
	return result
}

func (u *UnitFiles) getConfs(c *conf.Conf) map[string]string {
	confs := map[string]string{"%": "%"}
	if c == nil {
		return confs
	}
	unit := utils.ParseUnit(c.Name())
	root := c.RootMode()

	vartmp := paths.GetVARTMP(root)
	tmp    := paths.GetTMP(root)
	run    := paths.GetRUNTIME_DIR(root)
	etc    := paths.GetCONFIG_HOME(root)
	dat    := paths.GetVARLIB_HOME(root)
	log    := paths.GetLOG_DIR(root)
	cache  := paths.GetCACHE_HOME(root)
	home   := paths.GetHOME(root)
	userName  := paths.GetUser(root)
	userID := paths.GetUserID(root)
	group := paths.GetGroup(root)
	groupID := paths.GetGroupID(root)
	shell := paths.GetSHELL(root)

	xx := func(arg string) string { return utils.UnitNameUnescape(arg) }
	yy := func(arg string) string { return arg }

	confs["C"] = u.OsPath(cache)
	confs["E"] = u.OsPath(etc)
	confs["F"] = c.Filename()
	confs["f"] = "/" + xx(utils.StrE(unit.Instance))
	if unit.Instance == "" {
		confs["f"] = "/" + xx(unit.Prefix)
	}
	confs["h"] = home
	confs["i"] = yy(unit.Instance)
	confs["I"] = xx(unit.Instance)
	confs["j"] = yy(unit.Component)
	confs["J"] = xx(unit.Component)
	confs["L"] = u.OsPath(log)
	confs["n"] = yy(unit.FullName)
	confs["N"] = yy(unit.Name)
	confs["p"] = yy(unit.Prefix)
	confs["P"] = xx(unit.Prefix)
	confs["s"] = shell
	confs["S"] = u.OsPath(dat)
	confs["t"] = u.OsPath(run)
	confs["T"] = u.OsPath(tmp)
	confs["g"] = group
	confs["G"] = fmt.Sprintf("%d", groupID)
	confs["u"] = userName
	confs["U"] = fmt.Sprintf("%d", userID)
	confs["V"] = u.OsPath(vartmp)

	_ = cache // already used above
	return confs
}

// ExtraVars returns the command-line environment overrides.
func (u *UnitFiles) ExtraVars() []string { return u.extraVars }

// GetEnv builds the full environment for starting a service.
func (u *UnitFiles) GetEnv(c *conf.Conf) map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		idx := strings.Index(pair, "=")
		if idx > 0 {
			env[pair[:idx]] = pair[idx+1:]
		}
	}
	for _, envPart := range c.GetList(types.SectionService, "Environment", nil, true) {
		for _, kv := range u.ReadEnvPart(u.ExpandSpecial(envPart, c)) {
			env[kv[0]] = kv[1]
		}
	}
	for _, envFile := range c.GetList(types.SectionService, "EnvironmentFile", nil, true) {
		pairs, err := conf.ReadEnvFile(u.ExpandSpecial(envFile, c), u.Root)
		if err != nil {
			continue
		}
		for _, kv := range pairs {
			env[kv[0]] = u.ExpandEnv(kv[1], env)
		}
	}
	logg.Log(types.DebugExpand, "extra-vars %v", u.ExtraVars())
	for _, extra := range u.ExtraVars() {
		if strings.HasPrefix(extra, "@") {
			pairs, err := conf.ReadEnvFile(extra[1:], "")
			if err == nil {
				for _, kv := range pairs {
					logg.Log(types.InfoExpand, "override %s=%s", kv[0], kv[1])
					env[kv[0]] = u.ExpandEnv(kv[1], env)
				}
			}
		} else {
			for _, kv := range u.ReadEnvPart(extra) {
				logg.Log(types.InfoExpand, "override %s=%s", kv[0], kv[1])
				env[kv[0]] = kv[1]
			}
		}
	}
	return env
}

// ExpandEnv performs shell-style variable expansion using the given env map.
func (u *UnitFiles) ExpandEnv(cmd string, env map[string]string) string {
	maxDepth := types.ExpandVarsMaxDepth
	simple := regexp.MustCompile(`\$(\w+)`)
	braced := regexp.MustCompile(`\$\{(\w+)\}`)

	get1 := func(name string) string {
		if v, ok := env[name]; ok {
			return v
		}
		if types.ExpandKeepVars {
			return "$" + name
		}
		return ""
	}
	get2 := func(name string) string {
		if v, ok := env[name]; ok {
			return v
		}
		if types.ExpandKeepVars {
			return "${" + name + "}"
		}
		return ""
	}

	expanded := simple.ReplaceAllStringFunc(strings.ReplaceAll(cmd, "\\\n", ""), func(s string) string {
		return get1(s[1:])
	})
	for i := 0; i < maxDepth; i++ {
		next := braced.ReplaceAllStringFunc(expanded, func(s string) string {
			return get2(s[2 : len(s)-1])
		})
		if next == expanded {
			return expanded
		}
		expanded = next
	}
	logg.Errorf("shell variable expansion exceeded maxdepth %d", maxDepth)
	return expanded
}

// ReadEnvFile reads an environment file and returns key=value pairs.
func (u *UnitFiles) ReadEnvFile(envFile string) [][2]string {
	mode, file := utils.LoadPath(envFile)
	realFile := u.OsPath(file)
	if _, err := os.Stat(realFile); err != nil {
		if mode.Check {
			logg.Errorf("file does not exist: %s", realFile)
		} else {
			logg.Log(types.DebugExpand, "file does not exist: %s", realFile)
		}
		return nil
	}
	pairs, err := conf.ReadEnvFile(file, u.Root)
	if err != nil {
		logg.Log(types.InfoExpand, "while reading %s >> %v", envFile, err)
		return nil
	}
	return pairs
}

// ReadEnvPart parses an Environment= value (space-separated key=value pairs,
// possibly quoted with double-quotes).
func (u *UnitFiles) ReadEnvPart(envPart string) [][2]string {
	re := regexp.MustCompile(`\s*("[\w_]+=[^"]*"|[\w_]+=\S*)`)
	var result [][2]string
	for _, line := range strings.Split(envPart, "\n") {
		for _, match := range re.FindAllString(strings.TrimSpace(line), -1) {
			part := strings.TrimSpace(match)
			if strings.HasPrefix(part, `"`) {
				part = part[1 : len(part)-1]
			}
			idx := strings.Index(part, "=")
			if idx < 0 {
				continue
			}
			result = append(result, [2]string{part[:idx], part[idx+1:]})
		}
	}
	return result
}

// ── dependency helpers ────────────────────────────────────────────────────

// GetDependenciesUnit returns the direct dependencies of unit with their styles.
func (u *UnitFiles) GetDependenciesUnit(unit string, styles []string) map[string]string {
	if styles == nil {
		styles = []string{"Requires", "Wants", "Requisite", "BindsTo", "PartOf", "ConsistsOf",
			".requires", ".wants", "PropagateReloadTo", "Conflicts"}
	}
	c := u.GetConf(unit)
	deps := map[string]string{}
	for _, style := range styles {
		if strings.HasPrefix(style, ".") {
			for _, folder := range u.UnitFileFolders() {
				if folder == "" {
					continue
				}
				requirePath := u.OsPath(filepath.Join(folder, unit+style))
				entries, err := os.ReadDir(requirePath)
				if err != nil {
					continue
				}
				for _, e := range entries {
					if _, ok := deps[e.Name()]; !ok {
						deps[e.Name()] = style
					}
				}
			}
		} else {
			for _, requireList := range c.GetList(types.SectionUnit, style, nil, true) {
				for _, required := range strings.Fields(requireList) {
					if required = strings.TrimSpace(required); required != "" {
						deps[required] = style
					}
				}
			}
		}
	}
	return deps
}

// GetRequiredDependencies returns only the "hard" dependency styles.
func (u *UnitFiles) GetRequiredDependencies(unit string) map[string]string {
	styles := []string{"Requires", "Wants", "Requisite", "BindsTo", ".requires", ".wants"}
	return u.GetDependenciesUnit(unit, styles)
}

// SortedAfter returns the correctly-ordered start sequence for the unit list.
func (u *UnitFiles) SortedAfter(unitlist []string) []string {
	var conflist []*conf.Conf
	for _, unit := range unitlist {
		c := u.GetConf(unit)
		if c.Masked != "" {
			continue
		}
		conflist = append(conflist, c)
	}
	sortlist := ConfSortedAfter(conflist)
	result := make([]string, len(sortlist))
	for i, c := range sortlist {
		result[i] = c.Name()
	}
	return result
}

// ListDependencies yields the tree of dependencies for a unit.
func (u *UnitFiles) ListDependencies(unit string, indent string) []string {
	return u.listDependencies(unit, "", indent, "", nil)
}

func (u *UnitFiles) listDependencies(unit, show, indent, mark string, loop []string) []string {
	mapping := map[string]string{
		"Requires":          "required to start",
		"Wants":             "wanted to start",
		"Requisite":         "required started",
		"Bindsto":           "binds to start",
		"PartOf":            "part of started",
		".requires":         ".required to start",
		".wants":            ".wanted to start",
		"PropagateReloadTo": "(to be reloaded as well)",
		"Conflicts":         "(to be stopped on conflict)",
	}
	restrict := []string{"Requires", "Requisite", "ConsistsOf", "Wants", "BindsTo", ".requires", ".wants"}

	var result []string
	deps := u.GetDependenciesUnit(unit, nil)
	c := u.GetConf(unit)
	if c.IsLoaded() == "" {
		if strings.Contains(show, "notloaded") {
			result = append(result, fmt.Sprintf("%s(%s): %s", indent, unit, mark))
		}
		return result
	}
	result = append(result, fmt.Sprintf("%s%s: %s", indent, unit, mark))

	for _, stopAt := range []string{"Conflict", "conflict", "reloaded", "Propagate"} {
		if strings.Contains(mark, stopAt) {
			return result
		}
	}

	for dep, depStyle := range deps {
		inLoop := false
		for _, l := range loop {
			if l == dep {
				inLoop = true
				break
			}
		}
		if inLoop {
			continue
		}
		newLoop := append(append([]string{}, loop...), func() []string {
			ks := make([]string, 0, len(deps))
			for k := range deps {
				ks = append(ks, k)
			}
			return ks
		}()...)
		newIndent := indent + "| "
		newMark := depStyle
		if strings.Contains(show, "restrict") {
			inRestrict := false
			for _, r := range restrict {
				if newMark == r {
					inRestrict = true
					break
				}
			}
			if !inRestrict {
				continue
			}
		}
		if mapped, ok := mapping[newMark]; ok {
			newMark = mapped
		}
		result = append(result, u.listDependencies(dep, show, newIndent, newMark, newLoop)...)
	}
	return result
}

// ── sorting ───────────────────────────────────────────────────────────────

// GetBefore returns the units listed in Before= of the conf.
func GetBefore(c *conf.Conf) []string {
	var result []string
	for _, befores := range c.GetList(types.SectionUnit, "Before", nil, true) {
		for _, before := range strings.Fields(befores) {
			name := strings.TrimSpace(before)
			if name == "" {
				continue
			}
			found := false
			for _, r := range result {
				if r == name {
					found = true
					break
				}
			}
			if !found {
				result = append(result, name)
			}
		}
	}
	return result
}

// GetAfter returns the units listed in After= of the conf.
func GetAfter(c *conf.Conf) []string {
	var result []string
	for _, afters := range c.GetList(types.SectionUnit, "After", nil, true) {
		for _, after := range strings.Fields(afters) {
			name := strings.TrimSpace(after)
			if name == "" {
				continue
			}
			found := false
			for _, r := range result {
				if r == name {
					found = true
					break
				}
			}
			if !found {
				result = append(result, name)
			}
		}
	}
	return result
}

// CompareAfter returns -1, 0, +1 ordering for two confs based on After/Before.
func CompareAfter(confA, confB *conf.Conf) int {
	idA, idB := confA.Name(), confB.Name()
	for _, after := range GetAfter(confA) {
		if after == idB {
			return -1
		}
	}
	for _, after := range GetAfter(confB) {
		if after == idA {
			return 1
		}
	}
	for _, before := range GetBefore(confA) {
		if before == idB {
			return 1
		}
	}
	for _, before := range GetBefore(confB) {
		if before == idA {
			return -1
		}
	}
	return 0
}

type sortTuple struct {
	rank int
	conf *conf.Conf
}

// ConfSortedAfter returns a topologically sorted list of confs respecting
// After/Before constraints.
func ConfSortedAfter(conflist []*conf.Conf) []*conf.Conf {
	items := make([]*sortTuple, len(conflist))
	for i, c := range conflist {
		items[i] = &sortTuple{rank: 0, conf: c}
	}
	for range items {
		changed := 0
		for a := range items {
			for b := range items {
				if a == b {
					continue
				}
				before := CompareAfter(items[a].conf, items[b].conf)
				if before > 0 && items[a].rank <= items[b].rank {
					items[a].rank = items[b].rank + 1
					changed++
				}
				if before < 0 && items[b].rank <= items[a].rank {
					items[b].rank = items[a].rank + 1
					changed++
				}
			}
		}
		if changed == 0 {
			break
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].rank > items[j].rank
	})
	result := make([]*conf.Conf, len(items))
	for i, it := range items {
		result[i] = it.conf
	}
	return result
}

// ── preset handling ───────────────────────────────────────────────────────

// LoadPresetFiles reads all *.preset files in the preset folders.
func (u *UnitFiles) LoadPresetFiles(modules ...string) []string {
	if u.presetFileList == nil {
		u.presetFileList = map[string]*conf.PresetFile{}
		for _, folder1 := range u.PresetFolders() {
			folder := u.OsPath(folder1)
			entries, err := os.ReadDir(folder)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".preset") || e.IsDir() {
					continue
				}
				if _, ok := u.presetFileList[e.Name()]; ok {
					continue
				}
				path := filepath.Join(folder, e.Name())
				pf := conf.NewPresetFile()
				if _, err := pf.Read(path); err == nil {
					u.presetFileList[e.Name()] = pf
				}
			}
		}
		logg.Debugf("found %d preset files", len(u.presetFileList))
	}
	var result []string
	for name := range u.presetFileList {
		if utils.FnMatched(name, modules...) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

// GetPresetOfUnit returns "enable" or "disable" for the unit from preset files.
func (u *UnitFiles) GetPresetOfUnit(unit string) string {
	u.LoadPresetFiles()
	names := make([]string, 0, len(u.presetFileList))
	for n := range u.presetFileList {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		status := u.presetFileList[name].GetPreset(unit)
		if status != "" {
			return status
		}
	}
	return ""
}

// ── syntax checking ───────────────────────────────────────────────────────

// CheckEnvConditions verifies ConditionEnvironment= / AssertEnvironment=.
func (u *UnitFiles) CheckEnvConditions(c *conf.Conf, section string) []string {
	var problems []string
	unit := c.Name()
	for _, spec := range []string{"ConditionEnvironment", "AssertEnvironment"} {
		isAssert := strings.Contains(spec, "Assert")
		for _, checkname := range c.GetList(section, spec, nil, true) {
			mode, want := utils.Checkprefix(checkname)
			wantValue := ""
			name := want
			if idx := strings.Index(want, "="); idx >= 0 {
				name = want[:idx]
				wantValue = want[idx+1:]
			}
			value, exists := os.LookupEnv(name)
			if !exists {
				if !strings.Contains(mode, "!") {
					if isAssert {
						logg.Errorf("%s: %s - $%s not found", unit, spec, name)
					} else {
						logg.Warnf("%s: %s - $%s not found", unit, spec, name)
					}
					problems = append(problems, spec)
				}
			} else {
				if strings.Contains(mode, "!") {
					if wantValue != "" && value == wantValue {
						logg.Warnf("%s: %s - $%s wrong value - avoid '%s' have '%s'", unit, spec, name, wantValue, value)
						problems = append(problems, spec)
					} else if wantValue == "" {
						logg.Warnf("%s: %s - $%s was found", unit, spec, name)
						problems = append(problems, spec)
					}
				} else if wantValue != "" && value != wantValue {
					logg.Warnf("%s: %s - $%s wrong value - want '%s' have '%s'", unit, spec, name, wantValue, value)
					problems = append(problems, spec)
				}
			}
		}
	}
	return problems
}

// CheckFileConditions verifies ConditionPathExists= and related conditions.
func (u *UnitFiles) CheckFileConditions(c *conf.Conf, section string) []string {
	var problems []string
	unit := c.Name()
	for _, spec := range []string{"ConditionPathExistsGlob", "AssertPathExistsGlob"} {
		isAssert := strings.Contains(spec, "Assert")
		for _, checkfile := range c.GetList(section, spec, nil, true) {
			mode, filename := utils.Checkprefix(checkfile)
			filepath_ := u.OsPath(filename)
			matches, _ := filepath.Glob(filepath_)
			found := len(matches)
			if found > 0 {
				if strings.Contains(mode, "!") {
					if isAssert {
						logg.Errorf("%s: %s - found %d files in: %s", unit, spec, found, filename)
					} else {
						logg.Warnf("%s: %s - found %d files in: %s", unit, spec, found, filename)
					}
					problems = append(problems, spec+"="+checkfile)
				}
			} else {
				if !strings.Contains(mode, "!") {
					logg.Warnf("%s: %s - no files found: %s", unit, spec, filename)
					problems = append(problems, spec+"="+checkfile)
				}
			}
		}
	}
	// PathExists and related checks
	pathSpecs := []string{
		"ConditionPathExists", "ConditionPathIsDirectory", "ConditionPathIsSymbolicLink",
		"ConditionPathIsMountPoint", "ConditionPathIsReadWrite", "ConditionDirectoryNotEmpty",
		"ConditionFileIsExecutable", "ConditionFileNotEmpty",
		"AssertPathExists", "AssertPathIsDirectory", "AssertPathIsSymbolicLink",
		"AssertPathIsMountPoint", "AssertPathIsReadWrite", "AssertDirectoryNotEmpty",
		"AssertFileIsExecutable", "AssertFileNotEmpty",
	}
	for _, spec := range pathSpecs {
		for _, checkfile := range c.GetList(section, spec, nil, true) {
			mode, checkname := utils.Checkprefix(checkfile)
			filename := u.ExpandSpecial(checkname, c)
			if !filepath.IsAbs(filename) {
				logg.Errorf("%s: %s - path not absolute: %s", unit, spec, filename)
				problems = append(problems, spec+"="+checkfile)
				continue
			}
			rootPrefix := u.Root
			if strings.HasPrefix(filename, "//") {
				rootPrefix = ""
			}
			fpath := paths.OsPath(rootPrefix, filename)
			_, statErr := os.Lstat(fpath)
			exists := statErr == nil
			negate := strings.Contains(mode, "!")
			if !exists {
				if !negate {
					logg.Warnf("%s: %s - path not found: %s", unit, spec, filename)
					problems = append(problems, spec+"="+checkfile)
				}
				continue
			}
			// existence confirmed; run specific checks
			fi, _ := os.Lstat(fpath)
			if strings.Contains(spec, "PathExists") && negate {
				logg.Warnf("%s: %s - must not exist: %s", unit, spec, filename)
				problems = append(problems, spec+"="+checkfile)
			}
			if strings.Contains(spec, "FileNotEmpty") && fi != nil {
				if !fi.Mode().IsRegular() || fi.Size() == 0 {
					if !negate {
						logg.Warnf("%s: %s - file is empty or not a file: %s", unit, spec, filename)
						problems = append(problems, spec+"="+checkfile)
					}
				} else if negate {
					logg.Warnf("%s: %s - file is not empty: %s", unit, spec, filename)
					problems = append(problems, spec+"="+checkfile)
				}
			}
			if strings.Contains(spec, "IsDirectory") && fi != nil {
				if !fi.IsDir() {
					if !negate {
						logg.Warnf("%s: %s - not a directory: %s", unit, spec, filename)
						problems = append(problems, spec+"="+checkfile)
					}
				} else if negate {
					logg.Warnf("%s: %s - is a directory: %s", unit, spec, filename)
					problems = append(problems, spec+"="+checkfile)
				}
			}
		}
	}
	return problems
}

// SyntaxCheck validates a unit conf returning error count.
func (u *UnitFiles) SyntaxCheck(c *conf.Conf, conditions bool) int {
	errors := 0
	if conditions {
		errors += len(u.CheckFileConditions(c, types.SectionUnit))
	}
	filename := c.Filename()
	if strings.HasSuffix(filename, ".service") {
		errors += u.syntaxCheckService(c)
	}
	errors += u.syntaxCheckEnable(c)
	return errors
}

func (u *UnitFiles) syntaxCheckEnable(c *conf.Conf) int {
	errors := 0
	unit := c.Name()
	for _, target := range utils.Wordlist(c.GetList(types.SectionInstall, "WantedBy", nil, true)) {
		_, inRequires := types.TargetRequires[target]
		_, inAlias := types.TargetAlias[target]
		if !inRequires && !inAlias {
			logg.Errorf("%s: [Install] WantedBy unknown: %s", unit, target)
			errors++
		}
	}
	return errors
}

func (u *UnitFiles) syntaxCheckService(c *conf.Conf) int {
	unit := c.Name()
	if !c.Data.HasSection(types.SectionService) {
		logg.Errorf(" %s: a .service file without [Service] section", unit)
		return 101
	}
	errors := 0
	haveType := c.Get(types.SectionService, "Type", "simple", true)
	validTypes := []string{"simple", "exec", "forking", "notify", "oneshot", "dbus", "idle"}
	typeOK := false
	for _, vt := range validTypes {
		if haveType == vt {
			typeOK = true
			break
		}
	}
	if !typeOK {
		logg.Errorf(" %s: Failed to parse service type, ignoring: %s", unit, haveType)
		errors += 100
	}
	checkExecs := func(execs string) int {
		errs := 0
		for _, line := range c.GetList(types.SectionService, execs, nil, true) {
			_, exe := utils.ExecPath(line)
			if !strings.HasPrefix(exe, "/") {
				logg.Errorf("  %s: %s Executable path is not absolute.", unit, execs)
				errs++
			}
		}
		return errs
	}
	usedStart := c.GetList(types.SectionService, "ExecStart", nil, true)
	usedStop  := c.GetList(types.SectionService, "ExecStop", nil, true)
	errors += checkExecs("ExecStart")
	errors += checkExecs("ExecStop")
	errors += checkExecs("ExecReload")

	if haveType != "oneshot" {
		if len(usedStart) == 0 && len(usedStop) == 0 {
			logg.Errorf(" %s: [Service] lacks both ExecStart and ExecStop=", unit)
			errors += 101
		} else if len(usedStart) == 0 {
			logg.Errorf(" %s: [Service] has no ExecStart= (only allowed for Type=oneshot)", unit)
			errors += 101
		}
	}
	if len(usedStart) > 1 && haveType != "oneshot" {
		logg.Errorf(" %s: only one ExecStart statement allowed (unless Type=oneshot)", unit)
		errors++
	}
	return errors
}

// ExpandCmd parses and expands an ExecXxx= command line.
func (u *UnitFiles) ExpandCmd(cmd string, env map[string]string, c *conf.Conf) (utils.ExecMode, []string) {
	mode, exe := utils.ExecPath(cmd)
	var newCmd []string
	if mode.NoExpand {
		newCmd = u.splitCmd(exe)
	} else {
		newCmd = u.splitCmdAndExpand(exe, env, c)
	}
	if mode.Argv0 && len(newCmd) > 1 {
		newCmd = append(newCmd[:1], newCmd[2:]...)
	}
	return mode, newCmd
}

func (u *UnitFiles) splitCmd(cmd string) []string {
	cmd2 := strings.ReplaceAll(cmd, "\\\n", "")
	var result []string
	// Simple shell-like split (no quoting support beyond basic)
	current := ""
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd2); i++ {
		c := cmd2[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else {
				current += string(c)
			}
		} else {
			switch c {
			case '"', '\'':
				inQuote = true
				quoteChar = c
			case ' ', '\t':
				if current != "" {
					result = append(result, current)
					current = ""
				}
			default:
				current += string(c)
			}
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func (u *UnitFiles) splitCmdAndExpand(cmd string, env map[string]string, c *conf.Conf) []string {
	cmd2 := strings.ReplaceAll(cmd, "\\\n", "")
	simple := regexp.MustCompile(`\$(\w+)`)
	braced := regexp.MustCompile(`\$\{(\w+)\}`)

	get1 := func(name string) string {
		if v, ok := env[name]; ok {
			return v
		}
		return ""
	}
	cmd3 := simple.ReplaceAllStringFunc(cmd2, func(s string) string { return get1(s[1:]) })
	parts := u.splitCmd(cmd3)
	result := make([]string, len(parts))
	for i, part := range parts {
		part2 := u.ExpandSpecial(part, c)
		result[i] = braced.ReplaceAllStringFunc(part2, func(s string) string {
			return get1(s[2 : len(s)-1])
		})
	}
	return result
}

// ListStartDependenciesUnits returns (unit, style-summary) pairs in start order.
func (u *UnitFiles) ListStartDependenciesUnits(units []string) [][2]string {
	unitOrder := append([]string{}, units...)
	deps := map[string][]string{}
	for _, unit := range units {
		unitDeps := u.GetDependenciesUnit(unit, nil)
		for depUnit, depStyle := range unitDeps {
			found := false
			for _, s := range deps[depUnit] {
				if s == depStyle {
					found = true
					break
				}
			}
			if !found {
				deps[depUnit] = append(deps[depUnit], depStyle)
			}
		}
	}
	var depsConf []*conf.Conf
	for _, dep := range func() []string {
		ks := make([]string, 0, len(deps))
		for k := range deps {
			ks = append(ks, k)
		}
		return ks
	}() {
		inOrder := false
		for _, u2 := range unitOrder {
			if u2 == dep {
				inOrder = true
				break
			}
		}
		if inOrder {
			continue
		}
		c := u.GetConf(dep)
		if c.IsLoaded() != "" {
			depsConf = append(depsConf, c)
		}
	}
	for _, unit := range unitOrder {
		deps[unit] = []string{"Requested"}
		c := u.GetConf(unit)
		if c.IsLoaded() != "" {
			depsConf = append(depsConf, c)
		}
	}
	var result [][2]string
	for _, item := range ConfSortedAfter(depsConf) {
		styles := deps[item.Name()]
		result = append(result, [2]string{item.Name(), "(" + strings.Join(styles, " ") + ")"})
	}
	return result
}
