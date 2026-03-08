// Package types holds every global constant, default value and mutable
// option variable that the Python source stored as module-level globals.
// Commands mutate the Opt struct; internal packages read from it.
package types

import "os"

// ── exit-code sentinels ────────────────────────────────────────────────────

const (
	NotAProblem = 0 // FOUND_OK
	NotOK       = 1 // FOUND_ERROR
	NotActive   = 2 // FOUND_INACTIVE
	NotFound    = 4 // FOUND_UNKNOWN
)

// ── systemd section names ──────────────────────────────────────────────────

const (
	SectionUnit    = "Unit"
	SectionService = "Service"
	SectionSocket  = "Socket"
	SectionInstall = "Install"
)

// ── default timeout / timing values ───────────────────────────────────────

const (
	SystemCompatibilityVersion   = 219
	SysInitTarget                = "sysinit.target"
	SysInitWait                  = 5
	MinimumYield           float64 = 0.5
	MinimumTimeoutStartSec         = 4
	MinimumTimeoutStopSec          = 4
	DefaultTimeoutStartSec         = 90
	DefaultTimeoutStopSec          = 90
	DefaultTimeoutAbortSec         = 3600
	DefaultRestartSec        float64 = 0.1
	DefaultStartLimitIntervalSec   = 10
	DefaultStartLimitBurst         = 5
	InitLoopSleepDefault           = 5
	DefaultListenBacklog           = 2
	LogBufSize                     = 8192
	ProcMaxDepth                   = 100
	ExpandVarsMaxDepth             = 20
)

// DefaultMaximumTimeout can be overridden via --maxtimeout.
var DefaultMaximumTimeout = 200

// ── mutable global option state (mirrors Python module-level vars) ─────────

var (
	ExtraVars        []string
	Force            bool
	Full             bool
	LogLines         int
	NoPager          bool
	Now              int
	NoReload         bool
	NoLegend         bool
	NoAskPassword    bool
	PresetMode       = "all"
	Quiet            bool
	Root             string
	ShowAll          int
	UserMode         bool
	OnlyWhat         []string
	OnlyType         []string
	OnlyState        []string
	OnlyProperty     []string
	ForceIPv4        bool
	ForceIPv6        bool
	InitMode         int
	ExitMode         int
	ExecSpawn        = false
	ExecDup2         = true
	RemoveLockFile   bool
	BootPIDMin       = 0
	BootPIDMax       = -9
	ExpandKeepVars   = true
	RestartFailedUnits = true
	ActiveIfEnabled  = false
	OKConditionFailure = true
)

// ── paths used in the rest of the code ────────────────────────────────────

const (
	DevNull          = "/dev/null"
	DevZero          = "/dev/zero"
	EtcHosts         = "/etc/hosts"
	Rc3BootFolder    = "/etc/rc3.d"
	Rc3InitFolder    = "/etc/init.d/rc3.d"
	Rc5BootFolder    = "/etc/rc5.d"
	Rc5InitFolder    = "/etc/init.d/rc5.d"
	ProcPIDStatFmt   = "/proc/%d/stat"
	ProcPIDStatusFmt = "/proc/%d/status"
	ProcPIDCmdlineFmt = "/proc/%d/cmdline"
	ProcPIDDir       = "/proc"
	ProcSysUptime    = "/proc/uptime"
	ProcSysStat      = "/proc/stat"
	LocaleConf       = "/etc/locale.conf"
	DefaultPath      = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// ── folder lists ──────────────────────────────────────────────────────────

var SystemFolders = []string{
	"/etc/systemd/system",
	"/run/systemd/system",
	"/var/run/systemd/system",
	"/usr/local/lib/systemd/system",
	"/usr/lib/systemd/system",
	"/lib/systemd/system",
}

var UserFolders = []string{
	"{XDG_CONFIG_HOME}/systemd/user",
	"/etc/systemd/user",
	"{XDG_RUNTIME_DIR}/systemd/user",
	"/run/systemd/user",
	"/var/run/systemd/user",
	"{XDG_DATA_HOME}/systemd/user",
	"/usr/local/lib/systemd/user",
	"/usr/lib/systemd/user",
	"/lib/systemd/user",
}

var InitFolders = []string{
	"/etc/init.d",
	"/run/init.d",
	"/var/run/init.d",
}

var PresetFolders = []string{
	"/etc/systemd/system-preset",
	"/run/systemd/system-preset",
	"/var/run/systemd/system-preset",
	"/usr/local/lib/systemd/system-preset",
	"/usr/lib/systemd/system-preset",
	"/lib/systemd/system-preset",
}

// ── target / alias tables ─────────────────────────────────────────────────

var DefaultTargets = []string{
	"poweroff.target", "rescue.target", "sysinit.target",
	"basic.target", "multi-user.target", "graphical.target", "reboot.target",
}

var FeatureTargets = []string{
	"network.target", "remote-fs.target", "local-fs.target",
	"timers.target", "nfs-client.target",
}

var AllCommonTargets = append([]string{"default.target"}, append(DefaultTargets, FeatureTargets...)...)

var AllCommonEnabled  = []string{"default.target", "multi-user.target", "remote-fs.target"}
var AllCommonDisabled = []string{"graphical.target", "rescue.target", "nfs-client.target"}

var TargetRequires = map[string]string{
	"graphical.target":  "multi-user.target",
	"multi-user.target": "basic.target",
	"basic.target":      "sockets.target",
}

var TargetAlias = map[string]string{
	"default.target": "multi-user.target",
	"timers.target":  "sockets.target",
	"network.target": "basic.target",
}

var RunlevelMappings = map[string]string{
	"0": "poweroff.target",
	"1": "rescue.target",
	"2": "multi-user.target",
	"3": "multi-user.target",
	"4": "multi-user.target",
	"5": "graphical.target",
	"6": "reboot.target",
}

var SysvMappings = map[string]string{
	"$local_fs":  "local-fs.target",
	"$network":   "network.target",
	"$remote_fs": "remote-fs.target",
	"$timer":     "timers.target",
}

// ── misc paths with template tokens ───────────────────────────────────────

const (
	NotifySocketFolder  = "{RUN}/systemd"
	JournalLogFolder    = "{LOG}/journal"
	SystemctlDebugLog   = "{LOG}/systemctl.debug.log"
	SystemctlExtraLog   = "{LOG}/systemctl.log"
)

var TailCmds = []string{"/bin/tail", "/usr/bin/tail", "/usr/local/bin/tail"}
var LessCmds = []string{"/bin/less", "/usr/bin/less", "/usr/local/bin/less"}
var CatCmds  = []string{"/bin/cat", "/usr/bin/cat", "/usr/local/bin/cat"}

// ── env-driven defaults ────────────────────────────────────────────────────

var (
	DefaultUnit           = envOr("SYSTEMD_DEFAULT_UNIT", "default.target")
	DefaultTarget         = envOr("SYSTEMD_DEFAULT_TARGET", "multi-user.target")
	DefaultStandardInput  = envOr("SYSTEMD_STANDARD_INPUT", "null")
	DefaultStandardOutput = envOr("SYSTEMD_STANDARD_OUTPUT", "journal")
	DefaultStandardError  = envOr("SYSTEMD_STANDARD_ERROR", "inherit")
)

var ResetLocale = []string{
	"LANG", "LANGUAGE", "LC_CTYPE", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
	"LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
	"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION", "LC_ALL",
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── debug-level knobs (mirrors Python DEBUG_* globals) ────────────────────

var (
	DebugAfter    = 0
	DebugStatus   = 0
	DebugBootTime = 5 // TRACE
	DebugInitLoop = 0
	DebugKillAll  = 0
	DebugFlock    = 0
	DebugExpand   = 0
	InfoExpand    = 20 // INFO
	DebugResult   = 0
	TraceResult   = 0
)

// TestListen / TestAccept are testing hooks.
var (
	TestListen = false
	TestAccept = false
)
