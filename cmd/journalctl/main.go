// journalctl3 – journalctl emulator (Go port of journalctl3.py).
// Delegates to systemctl3's "log" command.
package main

import (
	"flag"
	"os"

	"systemctl-go/internal/commands"
	"systemctl-go/internal/systemctl"
	"systemctl-go/internal/types"
)

func main() {
	os.Exit(run())
}

func run() int {
	fUnit    := flag.String("unit", "", "Systemd unit to display")
	fFollow  := flag.Bool("follow", false, "Follow the log")
	fLines   := flag.Int("lines", 0, "Number of lines to display")
	fNoPager := flag.Bool("no-pager", false, "Do not pipe through a pager")
	fSystem  := flag.Bool("system", false, "Show system units")
	fUser    := flag.Bool("user", false, "Show user units")
	fRoot    := flag.String("root", "", "Use subdirectory path")
	fVerbose := flag.Bool("x", false, "Verbose mode")
	flag.StringVar(fUnit, "u", "", "Systemd unit to display (short)")
	flag.BoolVar(fFollow, "f", false, "Follow the log (short)")
	flag.IntVar(fLines, "n", 0, "Number of lines (short)")
	flag.Parse()

	if *fUnit == "" {
		flag.Usage()
		return 1
	}

	if *fNoPager {
		types.NoPager = true
	}
	if *fUser {
		types.UserMode = true
	}
	if *fSystem {
		types.UserMode = false
	}
	if *fRoot != "" {
		types.Root = *fRoot
	}
	if *fLines > 0 {
		types.LogLines = *fLines
	}
	if *fVerbose {
		// verbose mode: increase log level (placeholder)
	}

	s := systemctl.New(types.Root)
	s.Journal.NoPager = *fNoPager

	return commands.RunCommand(s, "log", *fUnit)
}
