package systemctl

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"systemctl-go/internal/conf"
	"systemctl-go/internal/listen"
	"systemctl-go/internal/paths"
	"systemctl-go/internal/process"
	"systemctl-go/internal/types"
	"systemctl-go/internal/utils"
)

// ── service directory helpers ─────────────────────────────────────────────

// RemoveServiceDirectories removes the RuntimeDirectory for the unit.
func (s *Systemctl) RemoveServiceDirectories(c *conf.Conf) bool {
	runtimeDir := c.Get(types.SectionService, "RuntimeDirectory", "", true)
	if runtimeDir == "" {
		return true
	}
	runtimeDir = paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(runtimeDir, c))
	if err := os.RemoveAll(runtimeDir); err != nil {
		logg.Warnf("remove runtime dir %s >> %v", runtimeDir, err)
	}
	return true
}

// CleanServiceDirectories removes all service-managed directories.
func (s *Systemctl) CleanServiceDirectories(c *conf.Conf) bool {
	for _, dir := range s.serviceDirectoryList(c) {
		if err := os.RemoveAll(dir); err != nil {
			logg.Warnf("clean service dir %s >> %v", dir, err)
		}
	}
	return true
}

func (s *Systemctl) serviceDirectoryList(c *conf.Conf) []string {
	var dirs []string
	for _, key := range []string{
		"RuntimeDirectory", "StateDirectory", "CacheDirectory",
		"LogsDirectory", "ConfigurationDirectory",
	} {
		val := c.Get(types.SectionService, key, "", true)
		if val == "" {
			continue
		}
		dirs = append(dirs, paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(val, c)))
	}
	return dirs
}

// EnvServiceDirectories returns env vars for service-managed directories.
func (s *Systemctl) EnvServiceDirectories(c *conf.Conf) map[string]string {
	env := map[string]string{}
	pairs := map[string]string{
		"RuntimeDirectory":       "RUNTIME_DIRECTORY",
		"StateDirectory":         "STATE_DIRECTORY",
		"CacheDirectory":         "CACHE_DIRECTORY",
		"LogsDirectory":          "LOGS_DIRECTORY",
		"ConfigurationDirectory": "CONFIGURATION_DIRECTORY",
	}
	for key, envKey := range pairs {
		val := c.Get(types.SectionService, key, "", true)
		if val == "" {
			continue
		}
		env[envKey] = paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(val, c))
	}
	return env
}

// CreateServiceDirectories creates all service-managed directories.
func (s *Systemctl) CreateServiceDirectories(c *conf.Conf) bool {
	for _, key := range []string{
		"RuntimeDirectory", "StateDirectory", "CacheDirectory",
		"LogsDirectory", "ConfigurationDirectory",
	} {
		val := c.Get(types.SectionService, key, "", true)
		if val == "" {
			continue
		}
		dir := paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(val, c))
		modeStr := c.Get(types.SectionService, key+"Mode", "0755", true)
		mode := os.FileMode(0o755)
		if m, ok := utils.IntMode(modeStr); ok {
			mode = m
		}
		if !s.MakeServiceDirectory(c, dir, mode) {
			return false
		}
	}
	return true
}

// MakeServiceDirectory creates a single directory and optionally chowns it.
func (s *Systemctl) MakeServiceDirectory(c *conf.Conf, dir string, mode os.FileMode) bool {
	if err := os.MkdirAll(dir, mode); err != nil {
		logg.Errorf("mkdir %s >> %v", dir, err)
		return false
	}
	userName := c.Get(types.SectionService, "User", "", true)
	groupName := c.Get(types.SectionService, "Group", "", true)
	if userName != "" || groupName != "" {
		s.ChownServiceDirectory(dir, userName, groupName)
	}
	return true
}

// ChownServiceDirectory sets ownership of a directory.
func (s *Systemctl) ChownServiceDirectory(dir, userName, groupName string) {
	f, err := os.Open(dir)
	if err != nil {
		logg.Warnf("chown open %s >> %v", dir, err)
		return
	}
	defer f.Close()
	if err := process.ShutilFchown(int(f.Fd()), userName, groupName); err != nil {
		logg.Warnf("chown %s >> %v", dir, err)
	}
}

// ── notify socket ─────────────────────────────────────────────────────────

// GetNotifySocketFrom returns the path for the unit's notify socket.
func (s *Systemctl) GetNotifySocketFrom(c *conf.Conf) string {
	root := c.RootMode()
	folder := paths.ExpandPath(types.NotifySocketFolder, root)
	return filepath.Join(folder, c.Name()+".notify")
}

// NotifySocketFrom creates and returns a listening Unix datagram socket.
func (s *Systemctl) NotifySocketFrom(c *conf.Conf) (*net.UnixConn, string) {
	sockPath := s.GetNotifySocketFrom(c)
	dir := filepath.Dir(sockPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logg.Warnf("notify mkdir %s >> %v", dir, err)
		return nil, ""
	}
	_ = os.Remove(sockPath)
	addr := &net.UnixAddr{Name: sockPath, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		logg.Warnf("notify socket %s >> %v", sockPath, err)
		return nil, ""
	}
	return conn, sockPath
}

// ReadNotifySocket reads one message from the notify socket with a timeout.
func (s *Systemctl) ReadNotifySocket(conn *net.UnixConn, timeout int) map[string]string {
	result := map[string]string{}
	if conn == nil {
		return result
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(timeout) * time.Second))
	buf := make([]byte, types.LogBufSize)
	n, err := conn.Read(buf)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "="); idx >= 0 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	return result
}

// WaitNotifySocket polls the notify socket until READY=1 or timeout.
func (s *Systemctl) WaitNotifySocket(conn *net.UnixConn, timeout int, pid int) bool {
	if conn == nil {
		return false
	}
	for attempt := 0; attempt < timeout; attempt++ {
		logg.Debugf("%s wait notify socket", utils.Delayed(attempt, "."))
		msg := s.ReadNotifySocket(conn, 1)
		if msg["READY"] == "1" {
			return true
		}
		if pid > 0 && !process.PidExists(pid) {
			return false
		}
	}
	return false
}

// ── exec helpers ──────────────────────────────────────────────────────────

// ExecveFrom starts a child process with the given arguments and environment.
// Returns the child PID or 0 on error.
func (s *Systemctl) ExecveFrom(c *conf.Conf, env map[string]string, args []string) (int, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("ExecveFrom: empty args")
	}
	cmd := exec.Command(args[0], args[1:]...)
	s.applyStdio(cmd, c)
	cmd.Env = buildEnvSlice(env)
	workDir := c.Get(types.SectionService, "WorkingDirectory", "", true)
	if workDir != "" {
		cmd.Dir = paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(workDir, c))
	}
	userName := c.Get(types.SectionService, "User", "", true)
	groupName := c.Get(types.SectionService, "Group", "", true)
	sysProcAttr := &syscall.SysProcAttr{Setsid: true}
	if userName != "" || groupName != "" {
		if cred, err := resolveCredential(userName, groupName); err == nil {
			sysProcAttr.Credential = cred
		} else {
			logg.Warnf("exec %s: credential %s/%s >> %v", c.Name(), userName, groupName, err)
		}
	}
	cmd.SysProcAttr = sysProcAttr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func (s *Systemctl) applyStdio(cmd *exec.Cmd, c *conf.Conf) {
	stdIO, err := s.Journal.OpenStandardLog(c)
	if err != nil {
		logg.Warnf("open stdio for %s >> %v", c.Name(), err)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return
	}
	cmd.Stdin = stdIO.InpFile
	cmd.Stdout = stdIO.OutFile
	cmd.Stderr = stdIO.ErrFile
}

func buildEnvSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func resolveCredential(userName, groupName string) (*syscall.Credential, error) {
	cred := &syscall.Credential{}
	if userName != "" {
		pw, err := osUserLookup(userName)
		if err != nil {
			return nil, err
		}
		cred.Uid = pw[0]
		cred.Gid = pw[1]
	}
	if groupName != "" {
		gid, err := osGroupLookup(groupName)
		if err != nil {
			return nil, err
		}
		cred.Gid = gid
	}
	return cred, nil
}

func osUserLookup(name string) ([2]uint32, error) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return [2]uint32{}, err
	}
	defer f.Close()
	buf := make([]byte, 65536)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 4 && parts[0] == name {
			var uid, gid uint32
			fmt.Sscanf(parts[2], "%d", &uid)
			fmt.Sscanf(parts[3], "%d", &gid)
			return [2]uint32{uid, gid}, nil
		}
	}
	return [2]uint32{}, fmt.Errorf("user %s not found", name)
}

func osGroupLookup(name string) (uint32, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 65536)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) >= 3 && parts[0] == name {
			var gid uint32
			fmt.Sscanf(parts[2], "%d", &gid)
			return gid, nil
		}
	}
	return 0, fmt.Errorf("group %s not found", name)
}

// ── start ─────────────────────────────────────────────────────────────────

// StartModules starts every matched unit and returns overall success.
func (s *Systemctl) StartModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if !s.StartUnit(unit) {
			ok = false
		}
	}
	return ok
}

// StartUnits starts a list of specific unit names (no glob matching).
func (s *Systemctl) StartUnits(units_ []string) bool {
	ok := true
	for _, unit := range units_ {
		if !s.StartUnit(unit) {
			ok = false
		}
	}
	return ok
}

// StartUnit starts a single named unit.
func (s *Systemctl) StartUnit(unit string) bool {
	c := s.Unitfiles.LoadConf(unit)
	if c == nil {
		logg.Errorf("Unit %s not found.", unit)
		s.Error |= types.NotFound
		return false
	}
	return s.StartUnitFrom(c)
}

// StartUnitFrom dispatches to the appropriate handler by unit type.
func (s *Systemctl) StartUnitFrom(c *conf.Conf) bool {
	unitType := utils.GetUnitType(c.Name())
	switch unitType {
	case "socket":
		return s.DoStartSocketFrom(c)
	case "target":
		return s.startTarget(c)
	default:
		return s.DoStartUnitFrom(c)
	}
}

func (s *Systemctl) startTarget(c *conf.Conf) bool {
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "active"})
	return true
}

// DoStartUnitFrom acquires the waitlock and calls DoStartServiceFrom.
func (s *Systemctl) DoStartUnitFrom(c *conf.Conf) bool {
	wl := conf.NewWaitlock(c)
	if !wl.Lock() {
		logg.Warnf("start %s: could not acquire lock", c.Name())
		return false
	}
	defer wl.Unlock()
	return s.DoStartServiceFrom(c)
}

// DoStartServiceFrom performs the actual service start logic.
func (s *Systemctl) DoStartServiceFrom(c *conf.Conf) bool {
	unit := c.Name()

	// Already running?
	if s.GetActiveServiceFrom(c) == "active" {
		pid := s.ReadMainpidFrom(c, nil)
		if pid != nil && process.PidExists(*pid) {
			logg.Debugf("start %s: already active (pid %d)", unit, *pid)
			return true
		}
	}

	serviceType := c.Get(types.SectionService, "Type", "simple", true)
	logg.Debugf("start %s type=%s", unit, serviceType)

	// Prepare directories
	s.CreateServiceDirectories(c)

	// ExecStartPre
	for _, pre := range c.GetList(types.SectionService, "ExecStartPre", nil, true) {
		if !s.runExecCmd(c, pre, false) {
			logg.Errorf("start %s: ExecStartPre failed: %s", unit, pre)
			return false
		}
	}

	env := s.buildServiceEnv(c)

	// Notify socket for Type=notify
	var notifyConn *net.UnixConn
	if serviceType == "notify" {
		notifyConn, _ = s.NotifySocketFrom(c)
		if notifyConn != nil {
			env["NOTIFY_SOCKET"] = s.GetNotifySocketFrom(c)
			defer func() { _ = notifyConn.Close() }()
		}
	}

	execStarts := c.GetList(types.SectionService, "ExecStart", nil, true)
	if len(execStarts) == 0 {
		logg.Errorf("start %s: no ExecStart", unit)
		return false
	}

	var pid int
	var startErr error

	switch serviceType {
	case "oneshot":
		return s.startOneshot(c, execStarts, env)
	case "forking":
		pid, startErr = s.startForking(c, execStarts[0], env)
	case "notify":
		pid, startErr = s.startNotify(c, execStarts[0], env, notifyConn)
	case "dbus":
		pid, startErr = s.startSimple(c, execStarts[0], env)
	case "exec", "idle":
		pid, startErr = s.startSimple(c, execStarts[0], env)
	default: // simple
		pid, startErr = s.startSimple(c, execStarts[0], env)
	}

	if startErr != nil {
		logg.Errorf("start %s: %v", unit, startErr)
		s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "failed", "EXIT": "1"})
		return false
	}

	if pid > 0 {
		logg.Debugf("start %s: pid=%d", unit, pid)
		s.WriteStatusFrom(c, map[string]interface{}{
			"ActiveState": "active",
			"MainPID":     pid,
		})
	}

	// ExecStartPost
	for _, post := range c.GetList(types.SectionService, "ExecStartPost", nil, true) {
		s.runExecCmd(c, post, true)
	}

	return true
}

func (s *Systemctl) startSimple(c *conf.Conf, execStart string, env map[string]string) (int, error) {
	_, cmd := utils.ExecPath(execStart)
	args := s.expandArgs(c, cmd, execStart)
	return s.ExecveFrom(c, env, args)
}

func (s *Systemctl) startOneshot(c *conf.Conf, execStarts []string, env map[string]string) bool {
	unit := c.Name()
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "activating"})
	for _, execStart := range execStarts {
		_, cmd := utils.ExecPath(execStart)
		args := s.expandArgs(c, cmd, execStart)
		pid, err := s.ExecveFrom(c, env, args)
		if err != nil {
			logg.Errorf("start %s oneshot: %v", unit, err)
			s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "failed"})
			return false
		}
		result := process.SubprocessWaitpid(pid)
		if result.ReturnCode != nil && *result.ReturnCode != 0 {
			if s.checkExecCheck(execStart) {
				logg.Errorf("start %s oneshot: exit %d", unit, *result.ReturnCode)
				s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "failed", "EXIT": *result.ReturnCode})
				return false
			}
		}
	}
	remainAfter := c.Get(types.SectionService, "RemainAfterExit", "no", true)
	if utils.StrYes(remainAfter) == "yes" {
		s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "active"})
	} else {
		s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
	}
	return true
}

func (s *Systemctl) startForking(c *conf.Conf, execStart string, env map[string]string) (int, error) {
	unit := c.Name()
	_, cmd := utils.ExecPath(execStart)
	args := s.expandArgs(c, cmd, execStart)
	pid, err := s.ExecveFrom(c, env, args)
	if err != nil {
		return 0, err
	}
	// For forking: wait for child to exit, then read PIDFile
	timeout := utils.ToInt(c.Get(types.SectionService, "TimeoutStartSec", "", true), types.DefaultTimeoutStartSec)
	pidFile := s.PidFileFrom(c, "")
	if pidFile != "" {
		mainPID := s.WaitPIDFile(pidFile, timeout)
		if mainPID == nil {
			logg.Warnf("start %s forking: no pid file %s", unit, pidFile)
			return pid, nil
		}
		return *mainPID, nil
	}
	// Wait for parent to exit (it daemonized)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		r := process.SubprocessTestpid(pid)
		if r.ReturnCode != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return pid, nil
}

func (s *Systemctl) startNotify(c *conf.Conf, execStart string, env map[string]string, notifyConn *net.UnixConn) (int, error) {
	_, cmd := utils.ExecPath(execStart)
	args := s.expandArgs(c, cmd, execStart)
	pid, err := s.ExecveFrom(c, env, args)
	if err != nil {
		return 0, err
	}
	timeout := utils.ToInt(c.Get(types.SectionService, "TimeoutStartSec", "", true), types.DefaultTimeoutStartSec)
	if !s.WaitNotifySocket(notifyConn, timeout, pid) {
		logg.Warnf("start %s notify: no READY=1 within %ds", c.Name(), timeout)
	}
	return pid, nil
}

func (s *Systemctl) buildServiceEnv(c *conf.Conf) map[string]string {
	env := s.Unitfiles.GetEnv(c)
	env = s.ExtendExecEnv(env)
	for k, v := range s.EnvServiceDirectories(c) {
		env[k] = v
	}
	return env
}

func (s *Systemctl) expandArgs(c *conf.Conf, cmd, raw string) []string {
	expanded := s.Unitfiles.ExpandSpecial(cmd, c)
	parts := strings.Fields(expanded)
	if len(parts) == 0 {
		return []string{raw}
	}
	return parts
}

func (s *Systemctl) checkExecCheck(execStart string) bool {
	mode, _ := utils.ExecPath(execStart)
	return mode.Check
}

func (s *Systemctl) runExecCmd(c *conf.Conf, execLine string, ignoreError bool) bool {
	mode, cmd := utils.ExecPath(execLine)
	args := s.expandArgs(c, cmd, execLine)
	env := s.buildServiceEnv(c)
	pid, err := s.ExecveFrom(c, env, args)
	if err != nil {
		if !mode.Check || ignoreError {
			return true
		}
		return false
	}
	result := process.SubprocessWaitpid(pid)
	if result.ReturnCode != nil && *result.ReturnCode != 0 {
		if mode.Check && !ignoreError {
			return false
		}
	}
	return true
}

// ── stop ──────────────────────────────────────────────────────────────────

// StopModules stops every matched unit.
func (s *Systemctl) StopModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		if !s.StopUnitFrom(c) {
			ok = false
		}
	}
	return ok
}

// StopUnit stops a single named unit.
func (s *Systemctl) StopUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	return s.StopUnitFrom(c)
}

// StopUnitFrom acquires the waitlock and stops the unit.
func (s *Systemctl) StopUnitFrom(c *conf.Conf) bool {
	wl := conf.NewWaitlock(c)
	if !wl.Lock() {
		logg.Warnf("stop %s: could not acquire lock", c.Name())
		return false
	}
	defer wl.Unlock()
	return s.DoStopUnitFrom(c)
}

// DoStopUnitFrom dispatches to the appropriate stop handler by unit type.
func (s *Systemctl) DoStopUnitFrom(c *conf.Conf) bool {
	unitType := utils.GetUnitType(c.Name())
	switch unitType {
	case "socket":
		return s.DoStopSocketFrom(c)
	case "target":
		s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
		return true
	default:
		return s.DoStopServiceFrom(c)
	}
}

// DoStopServiceFrom sends ExecStop commands, then signals the process.
func (s *Systemctl) DoStopServiceFrom(c *conf.Conf) bool {
	unit := c.Name()
	pid := s.ReadMainpidFrom(c, nil)

	// ExecStop
	for _, stopCmd := range c.GetList(types.SectionService, "ExecStop", nil, true) {
		s.runExecCmd(c, stopCmd, true)
	}

	if pid != nil && process.PidExists(*pid) {
		killMode := c.Get(types.SectionService, "KillMode", "control-group", true)
		sigStr := c.Get(types.SectionService, "KillSignal", "SIGTERM", true)
		sig := parseSignal(sigStr)

		logg.Debugf("stop %s: sending signal %d to pid %d (KillMode=%s)", unit, sig, *pid, killMode)
		if err := killProcess(*pid, sig); err != nil {
			logg.Warnf("stop %s: kill pid %d >> %v", unit, *pid, err)
		}

		timeout := utils.ToInt(c.Get(types.SectionService, "TimeoutStopSec", "", true), types.DefaultTimeoutStopSec)
		if !s.WaitVanishedPid(*pid, timeout) {
			logg.Warnf("stop %s: SIGKILL after timeout", unit)
			_ = killProcess(*pid, 9)
			s.WaitVanishedPid(*pid, types.MinimumTimeoutStopSec)
		}
	}

	// ExecStopPost
	for _, post := range c.GetList(types.SectionService, "ExecStopPost", nil, true) {
		s.runExecCmd(c, post, true)
	}

	s.CleanPidFileFrom(c)
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
	s.RemoveServiceDirectories(c)
	return true
}

// WaitVanishedPid waits up to timeout seconds for a PID to disappear.
func (s *Systemctl) WaitVanishedPid(pid int, timeout int) bool {
	if timeout <= 0 {
		timeout = types.MinimumTimeoutStopSec
	}
	for i := 0; i < timeout; i++ {
		logg.Debugf("%s wait vanished pid %d", utils.Delayed(i, "."), pid)
		if !process.PidExists(pid) {
			return true
		}
		time.Sleep(time.Second)
	}
	return !process.PidExists(pid)
}

// ── stop socket ───────────────────────────────────────────────────────────

// DoStopSocketFrom stops a socket unit.
func (s *Systemctl) DoStopSocketFrom(c *conf.Conf) bool {
	unit := c.Name()
	s.loopLock.Lock()
	sock, ok := s.sockets[unit]
	if ok {
		delete(s.sockets, unit)
	}
	s.loopLock.Unlock()
	if sock != nil && sock.Sock != nil {
		if l, ok2 := sock.Sock.(net.Listener); ok2 {
			_ = l.Close()
		} else if pc, ok2 := sock.Sock.(net.PacketConn); ok2 {
			_ = pc.Close()
		}
	}
	s.CleanPidFileFrom(c)
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "inactive"})
	return true
}

// ── reload ────────────────────────────────────────────────────────────────

// ReloadModules reloads every matched unit.
func (s *Systemctl) ReloadModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if !s.ReloadUnit(unit) {
			ok = false
		}
	}
	return ok
}

// ReloadUnit reloads a single named unit.
func (s *Systemctl) ReloadUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	return s.ReloadUnitFrom(c)
}

// ReloadUnitFrom acquires the waitlock and reloads the unit.
func (s *Systemctl) ReloadUnitFrom(c *conf.Conf) bool {
	wl := conf.NewWaitlock(c)
	if !wl.Lock() {
		return false
	}
	defer wl.Unlock()
	return s.DoReloadUnitFrom(c)
}

// DoReloadUnitFrom dispatches to the appropriate reload handler.
func (s *Systemctl) DoReloadUnitFrom(c *conf.Conf) bool {
	unitType := utils.GetUnitType(c.Name())
	if unitType == "service" {
		return s.DoReloadServiceFrom(c)
	}
	return true
}

// DoReloadServiceFrom sends ExecReload and waits for READY=1 if notify.
func (s *Systemctl) DoReloadServiceFrom(c *conf.Conf) bool {
	unit := c.Name()
	if !s.IsActiveFrom(c) {
		logg.Warnf("reload %s: not active", unit)
		return false
	}

	reloadCmds := c.GetList(types.SectionService, "ExecReload", nil, true)
	if len(reloadCmds) == 0 {
		logg.Warnf("reload %s: no ExecReload defined", unit)
		return false
	}

	for _, cmd := range reloadCmds {
		if !s.runExecCmd(c, cmd, false) {
			logg.Errorf("reload %s: ExecReload failed: %s", unit, cmd)
			return false
		}
	}
	return true
}

// ── restart ───────────────────────────────────────────────────────────────

// RestartModules restarts every matched unit.
func (s *Systemctl) RestartModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if !s.RestartUnit(unit) {
			ok = false
		}
	}
	return ok
}

// RestartUnit restarts a single named unit.
func (s *Systemctl) RestartUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	return s.DoRestartUnitFrom(c)
}

// DoRestartUnitFrom stops then starts the unit.
func (s *Systemctl) DoRestartUnitFrom(c *conf.Conf) bool {
	unit := c.Name()
	restartSec := utils.TimeToSeconds(
		c.Get(types.SectionService, "RestartSec", "", true),
		float64(types.DefaultTimeoutStartSec),
	)
	if restartSec == 0 {
		restartSec = types.DefaultRestartSec
	}

	wl := conf.NewWaitlock(c)
	if !wl.Lock() {
		return false
	}
	defer wl.Unlock()

	if s.IsActiveFrom(c) {
		if !s.DoStopServiceFrom(c) {
			logg.Warnf("restart %s: stop failed", unit)
		}
	}
	if restartSec > 0 {
		time.Sleep(time.Duration(restartSec * float64(time.Second)))
	}
	return s.DoStartServiceFrom(c)
}

// TryRestartModules restarts only currently-active units.
func (s *Systemctl) TryRestartModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		if !s.IsActiveFrom(c) {
			continue
		}
		if !s.DoRestartUnitFrom(c) {
			ok = false
		}
	}
	return ok
}

// ReloadOrRestartModules reloads if ExecReload is defined, else restarts.
func (s *Systemctl) ReloadOrRestartModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		reloadCmds := c.GetList(types.SectionService, "ExecReload", nil, true)
		if len(reloadCmds) > 0 && s.IsActiveFrom(c) {
			if !s.ReloadUnitFrom(c) {
				ok = false
			}
		} else {
			if !s.DoRestartUnitFrom(c) {
				ok = false
			}
		}
	}
	return ok
}

// ReloadOrTryRestartModules reloads or try-restarts each matched unit.
func (s *Systemctl) ReloadOrTryRestartModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		c := s.Unitfiles.GetConf(unit)
		reloadCmds := c.GetList(types.SectionService, "ExecReload", nil, true)
		if len(reloadCmds) > 0 && s.IsActiveFrom(c) {
			if !s.ReloadUnitFrom(c) {
				ok = false
			}
		} else if s.IsActiveFrom(c) {
			if !s.DoRestartUnitFrom(c) {
				ok = false
			}
		}
	}
	return ok
}

// ── kill ──────────────────────────────────────────────────────────────────

// KillModules sends a signal to each matched unit's process(es).
func (s *Systemctl) KillModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if !s.KillUnit(unit) {
			ok = false
		}
	}
	return ok
}

// KillUnit sends a signal to a single named unit's process.
func (s *Systemctl) KillUnit(unit string) bool {
	c := s.Unitfiles.GetConf(unit)
	return s.DoKillUnitFrom(c)
}

// DoKillUnitFrom sends the configured kill signal to the unit's main PID.
func (s *Systemctl) DoKillUnitFrom(c *conf.Conf) bool {
	sigStr := c.Get(types.SectionService, "KillSignal", "SIGTERM", true)
	sig := parseSignal(sigStr)
	pid := s.ReadMainpidFrom(c, nil)
	if pid == nil {
		logg.Warnf("kill %s: no main pid", c.Name())
		return false
	}
	return s.KillPid(*pid, sig)
}

// KillPid sends signal sig to the given PID.
func (s *Systemctl) KillPid(pid, sig int) bool {
	if err := killProcess(pid, sig); err != nil {
		logg.Warnf("kill pid %d sig %d >> %v", pid, sig, err)
		return false
	}
	return true
}

// ── socket activation ─────────────────────────────────────────────────────

// ListenModules activates socket listeners for matched socket units.
func (s *Systemctl) ListenModules(modules ...string) bool {
	ok := true
	for _, unit := range s.Unitfiles.MatchUnits(modules) {
		if !strings.HasSuffix(unit, ".socket") {
			continue
		}
		if !s.ListenUnit(unit) {
			ok = false
		}
	}
	return ok
}

// ListenUnit starts a socket listener for a single socket unit.
func (s *Systemctl) ListenUnit(unit string) bool {
	c := s.Unitfiles.LoadConf(unit)
	if c == nil {
		logg.Errorf("listen %s: not found", unit)
		return false
	}
	return s.DoListenUnitFrom(c)
}

// DoListenUnitFrom creates the socket and registers it.
func (s *Systemctl) DoListenUnitFrom(c *conf.Conf) bool {
	unit := c.Name()
	sock, err := s.CreateSocket(c)
	if err != nil {
		logg.Errorf("listen %s: create socket >> %v", unit, err)
		return false
	}
	s.loopLock.Lock()
	s.sockets[unit] = sock
	s.loopLock.Unlock()
	s.WriteStatusFrom(c, map[string]interface{}{"ActiveState": "active"})
	return true
}

// SocketList implements listen.Controller – returns the registered SocketItems.
func (s *Systemctl) SocketList() []listen.SocketItem {
	s.loopLock.Lock()
	defer s.loopLock.Unlock()
	result := make([]listen.SocketItem, 0, len(s.sockets))
	for _, sock := range s.sockets {
		if !sock.Skip {
			result = append(result, sock)
		}
	}
	return result
}

// DoAcceptSocketFrom accepts a connection on sock and triggers the service.
func (s *Systemctl) DoAcceptSocketFrom(sock listen.SocketItem) {
	go s.GetSocketServiceFrom(sock)
}

// GetSocketServiceFrom finds and starts the service that matches a socket unit.
func (s *Systemctl) GetSocketServiceFrom(sock listen.SocketItem) {
	socketUnit := sock.Name()
	serviceUnit := utils.PathReplaceExtension(socketUnit, ".socket", ".service")
	logg.Debugf("accept %s -> %s", socketUnit, serviceUnit)
	c := s.Unitfiles.LoadConf(serviceUnit)
	if c == nil {
		logg.Errorf("socket service %s not found", serviceUnit)
		return
	}
	if !s.IsActiveFrom(c) {
		s.DoStartServiceFrom(c)
	}
}

// DoStartSocketFrom creates the socket listener for a socket unit.
func (s *Systemctl) DoStartSocketFrom(c *conf.Conf) bool {
	return s.DoListenUnitFrom(c)
}

// CreateSocket creates a net.Listener or net.PacketConn for a socket unit.
func (s *Systemctl) CreateSocket(c *conf.Conf) (*conf.Socket, error) {
	stream := c.Get(types.SectionSocket, "ListenStream", "", true)
	dgram := c.Get(types.SectionSocket, "ListenDatagram", "", true)
	seq := c.Get(types.SectionSocket, "ListenSequentialPacket", "", true)

	var sockObj interface{}
	var err error

	switch {
	case stream != "":
		sockObj, err = s.CreatePortSocket(c, "tcp", stream)
		if err != nil {
			sockObj, err = s.CreateUnixSocket(c, "unix", stream)
		}
	case dgram != "":
		sockObj, err = s.CreatePortSocket(c, "udp", dgram)
		if err != nil {
			sockObj, err = s.CreateUnixSocket(c, "unixgram", dgram)
		}
	case seq != "":
		sockObj, err = s.CreateUnixSocket(c, "unixpacket", seq)
	default:
		return conf.NewSocket(c, nil, true), nil
	}
	if err != nil {
		return nil, err
	}
	return conf.NewSocket(c, sockObj, false), nil
}

// CreateUnixSocket creates a Unix domain socket.
func (s *Systemctl) CreateUnixSocket(c *conf.Conf, network, path string) (net.Listener, error) {
	path = paths.OsPath(s.root, s.Unitfiles.ExpandSpecial(path, c))
	_ = os.Remove(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return net.Listen(network, path)
}

// CreatePortSocket creates a TCP/UDP port socket.
func (s *Systemctl) CreatePortSocket(c *conf.Conf, network, addr string) (interface{}, error) {
	if network == "udp" {
		return net.ListenPacket("udp", addr)
	}
	return net.Listen("tcp", addr)
}

// ── init loop ─────────────────────────────────────────────────────────────

// InitDefault sets up the sysinit target and starts it.
func (s *Systemctl) InitDefault() bool {
	logg.Log(types.DebugInitLoop, "init default target=%s", s.defaultTarget)
	c := s.Unitfiles.GetTargetConf(types.SysInitTarget)
	s.sysinitTarget = c
	return true
}

// StartDefault starts the default target and its wanted/required units.
func (s *Systemctl) StartDefault() bool {
	logg.Log(types.DebugInitLoop, "start default target=%s", s.defaultTarget)
	s.WriteStatusFrom(s.Unitfiles.GetConf(s.defaultTarget), map[string]interface{}{"ActiveState": "active"})
	wants := s.Unitfiles.GetTargetList(s.defaultTarget)
	for _, unit := range wants {
		if strings.HasSuffix(unit, ".target") {
			continue
		}
		c := s.Unitfiles.LoadConf(unit)
		if c == nil {
			continue
		}
		logg.Log(types.DebugInitLoop, "start default wants: %s", unit)
		s.DoStartUnitFrom(c)
	}
	return true
}

// ExitDefault stops all services in the default target.
func (s *Systemctl) ExitDefault() bool {
	logg.Log(types.DebugInitLoop, "exit default target=%s", s.defaultTarget)
	wants := s.Unitfiles.GetTargetList(s.defaultTarget)
	for i := len(wants) - 1; i >= 0; i-- {
		unit := wants[i]
		if strings.HasSuffix(unit, ".target") {
			continue
		}
		c := s.Unitfiles.LoadConf(unit)
		if c == nil {
			continue
		}
		s.DoStopUnitFrom(c)
	}
	s.WriteStatusFrom(s.Unitfiles.GetConf(s.defaultTarget), map[string]interface{}{"ActiveState": "inactive"})
	return true
}

// InitLoop runs the socket-activation listener in the foreground.
// It blocks until exitMode is set or the process is signalled.
func (s *Systemctl) InitLoop() {
	logg.Log(types.DebugInitLoop, "init loop starting")
	lt := listen.New(s)
	go lt.Run()
	for {
		time.Sleep(time.Duration(s.LoopSleep_) * time.Second)
		if s.exitMode != 0 {
			break
		}
	}
	lt.Stop()
	logg.Log(types.DebugInitLoop, "init loop stopped")
}

// ── signal helpers ────────────────────────────────────────────────────────

func parseSignal(sigStr string) int {
	sigStr = strings.TrimPrefix(strings.ToUpper(sigStr), "SIG")
	switch sigStr {
	case "HUP":
		return int(syscall.SIGHUP)
	case "INT":
		return int(syscall.SIGINT)
	case "QUIT":
		return int(syscall.SIGQUIT)
	case "ILL":
		return int(syscall.SIGILL)
	case "ABRT":
		return int(syscall.SIGABRT)
	case "KILL":
		return 9
	case "TERM":
		return int(syscall.SIGTERM)
	case "USR1":
		return int(syscall.SIGUSR1)
	case "USR2":
		return int(syscall.SIGUSR2)
	case "PIPE":
		return int(syscall.SIGPIPE)
	case "ALRM":
		return int(syscall.SIGALRM)
	case "CONT":
		return int(syscall.SIGCONT)
	case "STOP":
		return int(syscall.SIGSTOP)
	case "WINCH":
		return int(syscall.SIGWINCH)
	}
	n, err := strconv.Atoi(sigStr)
	if err == nil {
		return n
	}
	return int(syscall.SIGTERM)
}
