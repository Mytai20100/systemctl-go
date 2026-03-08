// Package paths contains helpers for resolving OS paths, XDG directories
// and expanding systemd special-variable tokens (${HOME}, {RUN}, etc.).
package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/gdraheim/systemctl-go/internal/types"
)

// ── login / uid helpers ────────────────────────────────────────────────────

// OsGetlogin returns the login name of the effective user without calling
// os.Getlogin (which may fail in containers).
func OsGetlogin() string {
	u, err := user.LookupId(fmt.Sprintf("%d", os.Geteuid()))
	if err != nil {
		return "root"
	}
	return u.Username
}

// ── XDG / environment getters ──────────────────────────────────────────────

func GetHome() string { return os.ExpandEnv("~") }

func GetHOME(root bool) string {
	if root {
		return "/root"
	}
	return GetHome()
}

func GetUserID(root bool) int {
	if root {
		return 0
	}
	return os.Geteuid()
}

func GetUser(root bool) string {
	if root {
		return "root"
	}
	return OsGetlogin()
}

func GetGroupID(root bool) int {
	if root {
		return 0
	}
	return os.Getegid()
}

func GetGroup(root bool) string {
	if root {
		return "root"
	}
	u, err := user.LookupGroupId(fmt.Sprintf("%d", os.Getegid()))
	if err != nil {
		return "root"
	}
	return u.Name
}

func GetTMP(root bool) string {
	if root {
		return "/tmp"
	}
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "/tmp"
}

func GetVARTMP(root bool) string {
	if root {
		return "/var/tmp"
	}
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "/var/tmp"
}

func GetSHELL(root bool) string {
	if root {
		return "/bin/sh"
	}
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	return "/bin/sh"
}

func GetRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	return "/tmp/run-" + OsGetlogin()
}

func GetRUNTIME_DIR(root bool) string {
	if root {
		return "/run"
	}
	return GetRuntimeDir()
}

func GetCONFIG_HOME(root bool) string {
	if root {
		return "/etc"
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return GetHOME(false) + "/.config"
}

func GetCACHE_HOME(root bool) string {
	if root {
		return "/var/cache"
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v
	}
	return GetHOME(false) + "/.cache"
}

func GetDATA_HOME(root bool) string {
	if root {
		return "/usr/share"
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return GetHOME(false) + "/.local/share"
}

func GetLOG_DIR(root bool) string {
	if root {
		return "/var/log"
	}
	return filepath.Join(GetCONFIG_HOME(false), "log")
}

func GetVARLIB_HOME(root bool) string {
	if root {
		return "/var/lib"
	}
	return GetCONFIG_HOME(false)
}

// GetRUN returns the run directory for root or the current user.
func GetRUN(root bool) string {
	tmpVar := GetTMP(root)
	if types.Root != "" {
		tmpVar = types.Root
	}
	if root {
		for _, p := range []string{"/run", "/var/run", tmpVar + "/run"} {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				if err := os.MkdirAll(p, 0o755); err == nil {
					if isWritable(p) {
						return p
					}
				}
			}
		}
		p := tmpVar + "/run"
		_ = os.MkdirAll(p, 0o755)
		return p
	}
	uid := GetUserID(false)
	for _, p := range []string{
		fmt.Sprintf("/run/user/%d", uid),
		fmt.Sprintf("/var/run/user/%d", uid),
		fmt.Sprintf("%s/run-%d", tmpVar, uid),
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() && isWritable(p) {
			return p
		}
	}
	p := fmt.Sprintf("%s/run-%d", tmpVar, uid)
	_ = os.MkdirAll(p, 0o700)
	return p
}

func isWritable(path string) bool {
	return os.WriteFile(filepath.Join(path, ".writable_test"), nil, 0o600) == nil ||
		func() bool {
			f, err := os.OpenFile(filepath.Join(path, ".writable_test"), os.O_WRONLY|os.O_CREATE, 0o600)
			if err != nil {
				return false
			}
			_ = f.Close()
			_ = os.Remove(filepath.Join(path, ".writable_test"))
			return true
		}()
}

// GetPID_DIR returns the PID directory.
func GetPID_DIR(root bool) string {
	if root {
		return GetRUN(root)
	}
	return filepath.Join(GetRUN(false), "run")
}

// ── path rooting ───────────────────────────────────────────────────────────

// IsGoodRoot returns true if the root path has more than one path component
// (i.e. is not "/" or "/foo" but at least "/foo/bar").
func IsGoodRoot(root string) bool {
	if root == "" {
		return true
	}
	trimmed := strings.Trim(root, string(os.PathSeparator))
	return strings.Count(trimmed, string(os.PathSeparator)) > 1
}

// OsPath prepends root to path following systemd semantics.
func OsPath(root, path string) string {
	if root == "" {
		return path
	}
	if path == "" {
		return path
	}
	if IsGoodRoot(root) && strings.HasPrefix(path, root) {
		return path
	}
	if strings.HasPrefix(path, string(os.PathSeparator)) {
		p1 := path[1:]
		if strings.HasPrefix(p1, string(os.PathSeparator)) {
			return path // //paths are kept as-is
		}
		return filepath.Join(root, p1)
	}
	return filepath.Join(root, path)
}

// ── special variable expansion ─────────────────────────────────────────────

// ExpandPath expands {HOME}, {RUN}, {LOG}, {XDG_*} tokens in path.
// root=true uses system directories; root=false uses user directories.
func ExpandPath(path string, root bool) string {
	home := GetHOME(root)
	run := GetRUN(root)
	log := GetLOG_DIR(root)
	xdgDataHome := GetDATA_HOME(root)
	xdgConfigHome := GetCONFIG_HOME(root)
	xdgRuntimeDir := GetRUNTIME_DIR(root)

	// Normalise ${ to { for template-style substitution
	path = strings.ReplaceAll(path, "${", "{")

	replacer := strings.NewReplacer(
		"{HOME}", home,
		"{RUN}", run,
		"{LOG}", log,
		"{XDG_DATA_HOME}", xdgDataHome,
		"{XDG_CONFIG_HOME}", xdgConfigHome,
		"{XDG_RUNTIME_DIR}", xdgRuntimeDir,
	)
	expanded := replacer.Replace(path)
	// Also handle os-level ~ expansion
	if strings.HasPrefix(expanded, "~/") {
		expanded = home + expanded[1:]
	}
	return expanded
}
