// Package commands maps systemctl command strings to Systemctl methods and
// provides the output-printing helpers used by main().
package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/gdraheim/systemctl-go/internal/logger"
	"github.com/gdraheim/systemctl-go/internal/systemctl"
	"github.com/gdraheim/systemctl-go/internal/types"
)

var logg = logger.GetLogger("commands")

// RunCommand dispatches the given command string to the Systemctl instance.
// Returns the process exit code.
func RunCommand(s *systemctl.Systemctl, command string, modules ...string) int {
	switch command {
	case "list-units":
		return printTriple(s.ListUnitsModules(modules...))
	case "list-unit-files":
		return printPair(s.ListUnitFilesModules(modules...))
	case "list-dependencies":
		unit := firstOrDefault(modules, types.DefaultTarget)
		lines := s.Unitfiles.ListDependencies(unit, "")
		printLines(lines)
		return 0
	case "is-active":
		lines, rc := s.IsActiveModules(modules...)
		printLines(lines)
		return rc
	case "is-failed":
		lines, rc := s.IsFailedModules(modules...)
		printLines(lines)
		return rc
	case "is-enabled":
		lines, rc := s.IsEnabledModules(modules...)
		printLines(lines)
		return rc
	case "status":
		printLines(s.StatusUnits(modules))
		return s.Error
	case "cat":
		printLines(s.CatUnits(modules))
		return s.Error
	case "show":
		printLines(s.ShowModules(modules...))
		return s.Error
	case "start":
		if !s.StartModules(modules...) {
			return types.NotOK
		}
		return 0
	case "stop":
		if !s.StopModules(modules...) {
			return types.NotOK
		}
		return 0
	case "reload":
		if !s.ReloadModules(modules...) {
			return types.NotOK
		}
		return 0
	case "restart":
		if !s.RestartModules(modules...) {
			return types.NotOK
		}
		return 0
	case "try-restart":
		if !s.TryRestartModules(modules...) {
			return types.NotOK
		}
		return 0
	case "reload-or-restart":
		if !s.ReloadOrRestartModules(modules...) {
			return types.NotOK
		}
		return 0
	case "reload-or-try-restart":
		if !s.ReloadOrTryRestartModules(modules...) {
			return types.NotOK
		}
		return 0
	case "kill":
		if !s.KillModules(modules...) {
			return types.NotOK
		}
		return 0
	case "enable":
		list, ok := s.EnableModules(modules...)
		printLines(list)
		if !ok {
			return types.NotOK
		}
		return 0
	case "disable":
		list, ok := s.DisableModules(modules...)
		printLines(list)
		if !ok {
			return types.NotOK
		}
		return 0
	case "preset":
		if !s.PresetModules(modules...) {
			return types.NotOK
		}
		return 0
	case "preset-all":
		if !s.PresetAllModules() {
			return types.NotOK
		}
		return 0
	case "daemon-reload":
		if !s.DaemonReload() {
			return types.NotOK
		}
		return 0
	case "daemon-reexec":
		if !s.DaemonReexec() {
			return types.NotOK
		}
		return 0
	case "reset-failed":
		for _, unit := range s.Unitfiles.MatchUnits(modules) {
			c := s.Unitfiles.GetConf(unit)
			s.ResetFailedFrom(c)
		}
		return 0
	case "version":
		fmt.Print(s.Version())
		return 0
	case "list-paths":
		printLines(s.ListPaths())
		return 0
	case "help":
		fmt.Println(s.Help())
		return 0
	case "log":
		for _, unit := range modules {
			c := s.Unitfiles.LoadConf(unit)
			if c == nil {
				logg.Errorf("Unit %s not found.", unit)
				return types.NotFound
			}
			rc := s.Journal.LogUnitFrom(c, types.LogLines, false)
			if rc != 0 {
				return rc
			}
		}
		return 0
	case "environment", "show-environment":
		printLines(s.SystemExecEnv())
		return s.Error
	case "command":
		result := s.CommandOfUnit(firstOrDefault(modules, ""))
		if result != nil {
			printLines(result)
		}
		return s.Error
	case "halt", "poweroff":
		if !s.HaltModules(modules...) {
			return types.NotOK
		}
		return 0
	case "reboot":
		if !s.RebootModules(modules...) {
			return types.NotOK
		}
		return 0
	default:
		logg.Errorf("Unknown command '%s'", command)
		fmt.Fprintf(os.Stderr, "Unknown operation '%s'.\n", command)
		return types.NotOK
	}
}

// PrintBegin logs the full invocation for debugging.
func PrintBegin(argv []string, args []string) {
	logg.Debugf("EXEC: %s", strings.Join(argv, " "))
	logg.Debugf("ARGS: %v", args)
}

func printLines(lines []string) int {
	for _, line := range lines {
		fmt.Println(line)
	}
	return 0
}

func printPair(pairs [][2]string) int {
	for _, p := range pairs {
		if p[1] != "" {
			fmt.Printf("%-48s %s\n", p[0], p[1])
		} else {
			fmt.Println(p[0])
		}
	}
	return 0
}

func printTriple(triples [][3]string) int {
	for _, t := range triples {
		switch {
		case t[1] != "" && t[2] != "":
			fmt.Printf("%-40s %-24s %s\n", t[0], t[1], t[2])
		case t[1] != "":
			fmt.Printf("%-40s %s\n", t[0], t[1])
		default:
			fmt.Println(t[0])
		}
	}
	return 0
}

func firstOrDefault(modules []string, def string) string {
	if len(modules) > 0 {
		return modules[0]
	}
	return def
}


