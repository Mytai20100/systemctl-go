// Package process provides UNIX process management helpers used by the
// service-control logic: PID existence checks, zombie detection, credential
// switching and waitpid wrappers.
package process

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"syscall"

	"github.com/gdraheim/systemctl-go/internal/logger"
)

var logg = logger.GetLogger("process")

// ── pid existence ──────────────────────────────────────────────────────────

// PidExists checks whether a PID is present in the process table.
func PidExists(pid int) bool {
	if pid == 0 {
		panic("invalid PID 0")
	}
	return pidExists(pid)
}

func pidExists(pid int) bool {
	if pid < 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if err == syscall.ESRCH {
		return false
	}
	if err == syscall.EPERM {
		return true // process exists but we lack permission
	}
	return false
}

// PidZombie returns true if the process exists but is in zombie state.
func PidZombie(pid int) bool {
	if pid <= 0 {
		return false
	}
	return pidZombie(pid)
}

func pidZombie(pid int) bool {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	f, err := os.Open(statusPath)
	if err != nil {
		return false
	}
	defer f.Close()
	buf, _ := io.ReadAll(f)
	for _, line := range strings.Split(string(buf), "\n") {
		if strings.HasPrefix(line, "State:") {
			return strings.Contains(line, "Z")
		}
	}
	return false
}

// ── WaitPID result ─────────────────────────────────────────────────────────

// WaitPIDResult holds the outcome of a waitpid call.
type WaitPIDResult struct {
	PID        int
	ReturnCode *int // nil means still running
	Signal     int
}

// SubprocessWaitpid blocks until pid exits and returns the result.
func SubprocessWaitpid(pid int) WaitPIDResult {
	proc, err := os.FindProcess(pid)
	if err != nil {
		rc := 1
		return WaitPIDResult{PID: pid, ReturnCode: &rc}
	}
	state, err := proc.Wait()
	if err != nil {
		rc := 1
		return WaitPIDResult{PID: pid, ReturnCode: &rc}
	}
	rc := state.ExitCode()
	sig := 0
	if !state.Exited() {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok {
			sig = int(ws.Signal())
		}
	}
	return WaitPIDResult{PID: pid, ReturnCode: &rc, Signal: sig}
}

// SubprocessTestpid is a non-blocking wait – returns nil ReturnCode if still running.
func SubprocessTestpid(pid int) WaitPIDResult {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if err != nil || wpid == 0 {
		return WaitPIDResult{PID: pid, ReturnCode: nil, Signal: 0}
	}
	rc := ws.ExitStatus()
	sig := int(ws.Signal())
	return WaitPIDResult{PID: wpid, ReturnCode: &rc, Signal: sig}
}

// MustHaveFailed corrects a zero exit code for /bin/kill with unknown $MAINPID.
func MustHaveFailed(result WaitPIDResult, cmd []string) WaitPIDResult {
	if len(cmd) == 0 || cmd[0] != "/bin/kill" {
		return result
	}
	hasPID := false
	for _, arg := range cmd[1:] {
		if !strings.HasPrefix(arg, "-") {
			hasPID = true
			break
		}
	}
	if !hasPID {
		if result.ReturnCode == nil || *result.ReturnCode == 0 {
			logg.Errorf("waitpid %v did return %v => correcting as 11", cmd, result.ReturnCode)
			rc := 11
			return WaitPIDResult{PID: result.PID, ReturnCode: &rc, Signal: result.Signal}
		}
	}
	return result
}

// ── credential switching ───────────────────────────────────────────────────

// SetuidInfo holds the environment variables to set after credential switch.
type SetuidInfo struct {
	User    string
	Logname string
	Home    string
	Shell   string
}

// ShutilSetuid switches the process UID/GID in a forked child.
// Call only after fork. Returns environment variables to export.
func ShutilSetuid(userName, groupName string, xgroups []string) (SetuidInfo, error) {
	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return SetuidInfo{}, fmt.Errorf("group %s not found: %w", groupName, err)
		}
		var gid uint32
		fmt.Sscanf(g.Gid, "%d", &gid)
		if err := syscall.Setgid(int(gid)); err != nil {
			return SetuidInfo{}, err
		}
		if err := syscall.Setgroups([]int{int(gid)}); err != nil {
			logg.Debugf("setgroups %v < (%s) >> %v", []int{int(gid)}, groupName, err)
		}
	}
	if userName != "" {
		pw, err := user.Lookup(userName)
		if err != nil {
			return SetuidInfo{}, fmt.Errorf("user %s not found: %w", userName, err)
		}
		var uid, gid uint32
		fmt.Sscanf(pw.Uid, "%d", &uid)
		fmt.Sscanf(pw.Gid, "%d", &gid)
		if groupName == "" {
			if err := syscall.Setgid(int(gid)); err != nil {
				return SetuidInfo{}, err
			}
		}
		var groups []int
		allGroups, _ := user.LookupGroupId(pw.Gid)
		if allGroups != nil {
			groups = append(groups, int(gid))
		}
		for _, xg := range xgroups {
			eg, err := user.LookupGroup(xg)
			if err != nil {
				continue
			}
			var egid int
			fmt.Sscanf(eg.Gid, "%d", &egid)
			found := false
			for _, g := range groups {
				if g == egid {
					found = true
					break
				}
			}
			if !found {
				groups = append(groups, egid)
			}
		}
		if len(groups) == 0 {
			groups = []int{int(gid)}
		}
		if err := syscall.Setgroups(groups); err != nil {
			logg.Debugf("setgroups %v >> %v", groups, err)
		}
		if err := syscall.Setuid(int(uid)); err != nil {
			return SetuidInfo{}, err
		}
		return SetuidInfo{
			User:    userName,
			Logname: pw.Username,
			Home:    pw.HomeDir,
			Shell:   "/bin/sh",
		}, nil
	}
	return SetuidInfo{}, nil
}

// ShutilFchown calls fchown on the given file descriptor.
func ShutilFchown(fd int, userName, groupName string) error {
	uid, gid := -1, -1
	if userName != "" {
		pw, err := user.Lookup(userName)
		if err != nil {
			return err
		}
		fmt.Sscanf(pw.Uid, "%d", &uid)
		fmt.Sscanf(pw.Gid, "%d", &gid)
	}
	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return err
		}
		fmt.Sscanf(g.Gid, "%d", &gid)
	}
	if uid != -1 || gid != -1 {
		return syscall.Fchown(fd, uid, gid)
	}
	return nil
}

// ShutilTruncate creates or truncates a file (creates parent directories as needed).
func ShutilTruncate(filename string) error {
	dir := strings.ReplaceAll(filename, "\\", "/")
	dir = dir[:strings.LastIndex(dir, "/")+1]
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	return f.Close()
}