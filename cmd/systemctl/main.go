// systemctl3 – systemctl emulator for containers (Go port of systemctl3.py)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/gdraheim/systemctl-go/internal/commands"
	"github.com/gdraheim/systemctl-go/internal/logger"
	"github.com/gdraheim/systemctl-go/internal/systemctl"
	"github.com/gdraheim/systemctl-go/internal/types"
)

var logg = logger.GetLogger("main")

func main() {
	os.Exit(run())
}

func run() int {
	// ── flags ──────────────────────────────────────────────────────────────
	fVersion     := flag.Bool("version", false, "Show package version")
	fSystem      := flag.Bool("system", false, "Connect to system manager (default)")
	fUser        := flag.Bool("user", false, "Connect to user service manager")
	fType        := stringSlice("type", "t", "List units of a particular type")
	fState       := stringSlice("state", "", "List units with particular LOAD or SUB or ACTIVE state")
	fProperty    := stringSlice("property", "p", "Show only properties by this name")
	fWhat        := stringSlice("what", "", "Service directories to clean")
	fAll         := flag.Int("all", 0, "Show all loaded units/properties, including dead/empty ones")
	fFull        := flag.Bool("full", false, "Don't ellipsize unit names on output")
	fForce       := flag.Bool("force", false, "Override existing symlinks when enabling")
	fNow         := flag.Int("now", 0, "Start or stop unit in addition to enabling or disabling")
	fQuiet       := flag.Bool("quiet", false, "Suppress output")
	fNoLegend    := flag.Bool("no-legend", false, "Do not print a legend")
	fNoReload    := flag.Bool("no-reload", false, "Don't reload daemon after en-/dis-abling unit files")
	fNoAskPass   := flag.Bool("no-ask-password", false, "Do not ask for system passwords")
	fNoPager     := flag.Bool("no-pager", false, "Do not pipe output into pager")
	fPresetMode  := flag.String("preset-mode", "all", "Apply only enable, only disable, or all presets")
	fRoot        := flag.String("root", "", "Enable unit files in the specified root directory")
	fLines       := flag.String("lines", "", "Number of journal entries to show")
	fMaxTimeout  := flag.Int("maxtimeout", types.DefaultMaximumTimeout, "Override max timeout")
	fConfig      := stringSlice("config", "c", "Override internal variables (Name=Val)")
	fExtraVars   := stringSlice("extra-vars", "e", "Override settings in the syntax of 'Environment='")
	fVerbose     := flag.Int("verbose", 0, "Increase debugging information level")
	fIPv4        := flag.Bool("ipv4", false, "Only keep ipv4 localhost in /etc/hosts")
	fIPv6        := flag.Bool("ipv6", false, "Only keep ipv6 localhost in /etc/hosts")
	fExit        := flag.Int("exit", 0, "Exit init-process when no procs left")
	fInit        := flag.Int("init", 0, "Keep running as init-process")

	// ignored flags for compatibility
	flag.Bool("reverse", false, "")
	flag.String("job-mode", "", "")
	flag.Bool("show-types", false, "")
	flag.Bool("ignore-inhibitors", false, "")
	flag.String("kill-who", "", "")
	flag.String("signal", "", "")
	flag.Bool("no-block", false, "")
	flag.Bool("no-wall", false, "")
	flag.String("output", "", "")
	flag.Bool("plain", false, "")
	flag.Bool("no-warn", false, "")

	flag.Parse()

	// ── logging setup ─────────────────────────────────────────────────────
	level := logger.NOTE - 5**fVerbose
	if level < logger.TRACE {
		level = logger.TRACE
	}
	logg.SetLevel(level)
	log.SetFlags(0)

	// ── populate global option state ──────────────────────────────────────
	types.ExtraVars       = append(types.ExtraVars, *fExtraVars...)
	types.Force           = *fForce
	types.Full            = *fFull
	types.NoPager         = *fNoPager
	types.NoReload        = *fNoReload
	types.NoLegend        = *fNoLegend
	types.NoAskPassword   = *fNoAskPass
	types.Now             = *fNow
	types.PresetMode      = *fPresetMode
	types.Quiet           = *fQuiet
	types.Root            = *fRoot
	types.ShowAll         = *fAll
	types.OnlyState       = append(types.OnlyState, *fState...)
	types.OnlyType        = append(types.OnlyType, *fType...)
	types.OnlyProperty    = append(types.OnlyProperty, *fProperty...)
	types.OnlyWhat        = append(types.OnlyWhat, *fWhat...)
	types.DefaultMaximumTimeout = *fMaxTimeout
	types.ForceIPv4       = *fIPv4
	types.ForceIPv6       = *fIPv6
	types.ExitMode        = *fExit
	types.UserMode        = *fUser

	if *fLines != "" {
		if n, err := strconv.Atoi(*fLines); err == nil {
			types.LogLines = n
		}
	}

	pid1 := os.Getpid()
	initMode := *fInit
	if initMode == 0 && (pid1 == 0 || pid1 == 1) {
		initMode = 1
	}
	types.InitMode = initMode

	if *fSystem {
		types.UserMode = false
	} else if os.Geteuid() != 0 && (pid1 == 0 || pid1 == 1) {
		types.UserMode = true
	}

	// apply -c config overrides
	for _, setting := range *fConfig {
		applyConfig(setting)
	}

	// ── file-based logging ────────────────────────────────────────────────
	// (mirrors the Python logic that checks for writeable log files)
	// Omitted here – logging goes to stderr only in this port.

	// ── resolve args ──────────────────────────────────────────────────────
	args := flag.Args()
	commands.PrintBegin(os.Args, args)

	if *fVersion {
		args = []string{"version"}
	}
	if len(args) == 0 {
		if types.InitMode > 0 {
			args = []string{"default"}
		} else {
			args = []string{"list-units"}
		}
	}

	command := args[0]
	modules := args[1:]

	// remove bare "service" word – historical compat
	filtered := modules[:0]
	for _, m := range modules {
		if m != "service" {
			filtered = append(filtered, m)
		}
	}
	modules = filtered

	s := systemctl.New(types.Root)
	return commands.RunCommand(s, command, modules...)
}

// applyConfig processes a single -c Name=Val override.
func applyConfig(setting string) {
	name, val := setting, "1"
	if strings.Contains(setting, "=") {
		parts := strings.SplitN(setting, "=", 2)
		name, val = parts[0], parts[1]
	} else if strings.HasPrefix(strings.ToLower(name), "no-") {
		name, val = name[3:], "0"
	} else if strings.HasPrefix(name, "No") || strings.HasPrefix(name, "NO") {
		name, val = name[2:], "0"
	}
	isBoolStr := func(v string) bool {
		return v == "true" || v == "True" || v == "TRUE" || v == "yes" || v == "y" || v == "Y" || v == "YES" || v == "1"
	}
	// Only a handful of knobs are directly mappable here; add more as needed.
	switch name {
	case "InitLoopSleep":
		if n, err := strconv.Atoi(val); err == nil {
			// types package doesn't expose mutable InitLoopSleep at module level yet;
			// this would require a separate config function – left as TODO for Session 2.
			_ = n
		}
	case "DefaultMaximumTimeout":
		if n, err := strconv.Atoi(val); err == nil {
			types.DefaultMaximumTimeout = n
		}
	case "UserMode":
		types.UserMode = isBoolStr(val)
	case "ExpandKeepVars":
		types.ExpandKeepVars = isBoolStr(val)
	case "RestartFailedUnits":
		types.RestartFailedUnits = isBoolStr(val)
	default:
		fmt.Fprintf(os.Stderr, "(ignored) unknown config -c '%s'\n", name)
	}
}

// ── multi-value flag helper ───────────────────────────────────────────────

type strSliceValue []string

func (s *strSliceValue) String() string  { return strings.Join(*s, ",") }
func (s *strSliceValue) Set(v string) error { *s = append(*s, v); return nil }

// stringSlice registers a string-slice flag under both a long and optional
// short name and returns a pointer to the accumulated slice.
func stringSlice(long, short, usage string) *[]string {
	var v strSliceValue
	flag.Var(&v, long, usage)
	if short != "" && short != long {
		flag.Var(&v, short, usage+" (short)")
	}
	return (*[]string)(&v)
}
