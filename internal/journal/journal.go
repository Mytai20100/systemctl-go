// Package journal implements SystemctlJournal which manages per-service log
// files and streams them to the terminal via tail/cat/less.
package journal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"systemctl-go/internal/conf"
	"systemctl-go/internal/logger"
	"systemctl-go/internal/paths"
	"systemctl-go/internal/types"
	"systemctl-go/internal/units"
	"systemctl-go/internal/utils"
)

var logg = logger.GetLogger("journal")

// StandardIO groups the three standard streams for a spawned service.
type StandardIO struct {
	InpPath string
	OutPath string
	ErrPath string
	InpFile *os.File
	OutFile *os.File
	ErrFile *os.File
}

// Journal manages log-file handles for a set of service units.
type Journal struct {
	Unitfiles  *units.UnitFiles
	logFile    map[string]int   // unit -> opened fd
	logHold    map[string][]byte
	logFolder  string
	NoPager    bool
	TailCmds   []string
	LessCmds   []string
	CatCmds    []string
	ExecSpawn  bool
}

// New creates a Journal backed by the given UnitFiles.
func New(uf *units.UnitFiles) *Journal {
	if uf == nil {
		uf = units.NewUnitFiles(types.Root)
	}
	return &Journal{
		Unitfiles: uf,
		logFile:   map[string]int{},
		logHold:   map[string][]byte{},
		logFolder: types.JournalLogFolder,
		NoPager:   types.NoPager,
		TailCmds:  types.TailCmds,
		LessCmds:  types.LessCmds,
		CatCmds:   types.CatCmds,
		ExecSpawn: types.ExecSpawn,
	}
}

// StartLogFiles opens non-blocking read handles to each unit's log file.
func (j *Journal) StartLogFiles(units_ []string) {
	j.logFile = map[string]int{}
	j.logHold = map[string][]byte{}
	for _, unit := range units_ {
		c := j.Unitfiles.LoadConf(unit)
		if c == nil {
			continue
		}
		if j.SkipLog(c) {
			continue
		}
		logPath := j.GetLogFrom(c)
		fd, err := syscall.Open(logPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			logg.Errorf("can not open %s log: %s >> %v", unit, logPath, err)
			continue
		}
		j.logFile[unit] = fd
		j.logHold[unit] = []byte{}
	}
}

// PrintLogFiles reads new data from each unit log and writes to stdout fd.
func (j *Journal) PrintLogFiles(units_ []string, stdout int) int {
	const bufSize = types.LogBufSize
	printed := 0
	for _, unit := range units_ {
		fd, ok := j.logFile[unit]
		if !ok {
			continue
		}
		newText := []byte{}
		buf := make([]byte, bufSize)
		for {
			n, err := syscall.Read(fd, buf)
			if n <= 0 || err != nil {
				break
			}
			newText = append(newText, buf[:n]...)
		}
		text := append(j.logHold[unit], newText...)
		if len(text) == 0 {
			continue
		}
		lines := strings.Split(string(text), "\n")
		if !strings.HasSuffix(string(text), "\n") {
			j.logHold[unit] = []byte(lines[len(lines)-1])
			lines = lines[:len(lines)-1]
		} else {
			j.logHold[unit] = []byte{}
		}
		for _, line := range lines {
			content := []byte(unit + ": " + line + "\n")
			_, _ = syscall.Write(stdout, content)
			_ = syscall.Fsync(stdout)
			printed++
		}
	}
	return printed
}

// ReadLogFiles is an alias for PrintLogFiles with stdout=1.
func (j *Journal) ReadLogFiles(units_ []string) {
	j.PrintLogFiles(units_, 1)
}

// StopLogFiles closes all open log file handles.
func (j *Journal) StopLogFiles(units_ []string) {
	for _, unit := range units_ {
		if fd, ok := j.logFile[unit]; ok {
			_ = syscall.Close(fd)
		}
	}
	j.logFile = map[string]int{}
	j.logHold = map[string][]byte{}
}

// SkipLog returns true when the unit does not write to a journal log file.
func (j *Journal) SkipLog(c *conf.Conf) bool {
	if utils.GetUnitType(c.Name()) != "service" {
		return true
	}
	stdOut := c.Get(types.SectionService, "StandardOutput", types.DefaultStandardOutput, true)
	stdErr := c.Get(types.SectionService, "StandardError", types.DefaultStandardError, true)
	out := stdOut == "null" || strings.HasPrefix(stdOut, "file:")
	if stdErr == "inherit" {
		stdErr = stdOut
	}
	err := stdErr == "null" || strings.HasPrefix(stdErr, "file:") || strings.HasPrefix(stdErr, "append:")
	return out && err
}

// LogUnitFrom tails the log for the given conf.
func (j *Journal) LogUnitFrom(c *conf.Conf, lines int, follow bool) int {
	return j.TailLogFile(j.GetLogFrom(c), lines, follow, c.Name())
}

// TailLogFile invokes tail/cat/less on a log file path.
func (j *Journal) TailLogFile(logPath string, lines int, follow bool, unit string) int {
	var cmd *exec.Cmd
	if follow {
		tail := utils.GetExistPath(j.TailCmds)
		if tail == "" {
			fmt.Println("tail command not found")
			return 1
		}
		args := []string{"-n", fmt.Sprintf("%d", max(lines, 10)), "-F", logPath}
		logg.Debugf("journalctl %s -> %v", unit, args)
		if j.ExecSpawn {
			cmd = exec.Command(tail, args...)
		} else {
			if err := syscall.Exec(tail, append([]string{tail}, args...), os.Environ()); err != nil {
				return 1
			}
			return 0
		}
	} else if lines > 0 {
		tail := utils.GetExistPath(j.TailCmds)
		if tail == "" {
			fmt.Println("tail command not found")
			return 1
		}
		args := []string{"-n", fmt.Sprintf("%d", lines), logPath}
		logg.Debugf("journalctl %s -> %v", unit, args)
		if j.ExecSpawn {
			cmd = exec.Command(tail, args...)
		} else {
			if err := syscall.Exec(tail, append([]string{tail}, args...), os.Environ()); err != nil {
				return 1
			}
			return 0
		}
	} else if j.NoPager {
		cat := utils.GetExistPath(j.CatCmds)
		if cat == "" {
			fmt.Println("cat command not found")
			return 1
		}
		args := []string{logPath}
		logg.Debugf("journalctl %s -> %v", unit, args)
		if j.ExecSpawn {
			cmd = exec.Command(cat, args...)
		} else {
			if err := syscall.Exec(cat, append([]string{cat}, args...), os.Environ()); err != nil {
				return 1
			}
			return 0
		}
	} else {
		less := utils.GetExistPath(j.LessCmds)
		if less == "" {
			fmt.Println("less command not found")
			return 1
		}
		args := []string{logPath}
		logg.Debugf("journalctl %s -> %v", unit, args)
		if j.ExecSpawn {
			cmd = exec.Command(less, args...)
		} else {
			if err := syscall.Exec(less, append([]string{less}, args...), os.Environ()); err != nil {
				return 1
			}
			return 0
		}
	}
	if cmd != nil {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return 1
		}
	}
	return 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GetLogFrom returns the absolute path of the log file for the conf.
func (j *Journal) GetLogFrom(c *conf.Conf) string {
	return j.Unitfiles.OsPath(j.GetLog(c))
}

// GetLog returns the relative (to root) log file path for a conf.
func (j *Journal) GetLog(c *conf.Conf) string {
	filename := filepath.Base(utils.StrE(c.Filename()))
	unitname := (c.Name() + ".unit")
	if unitname == ".unit" {
		unitname = "default.unit"
	}
	name := filename
	if name == "" {
		name = unitname
	}
	logFolder := paths.ExpandPath(j.logFolder, c.RootMode())
	logFile := strings.ReplaceAll(name, string(os.PathSeparator), ".") + ".log"
	if strings.HasPrefix(logFile, ".") {
		logFile = "dot." + logFile
	}
	return filepath.Join(logFolder, logFile)
}

// OpenLog opens (or creates) the log file for appending.
func (j *Journal) OpenLog(c *conf.Conf) (*os.File, error) {
	logFile := j.GetLogFrom(c)
	logFolder := filepath.Dir(logFile)
	if err := os.MkdirAll(logFolder, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// OpenStandardLog opens the stdin/stdout/stderr streams as configured in the
// unit file and returns a StandardIO struct with populated file handles.
func (j *Journal) OpenStandardLog(c *conf.Conf) (*StandardIO, error) {
	stdInp := c.Get(types.SectionService, "StandardInput", types.DefaultStandardInput, true)
	stdOut := c.Get(types.SectionService, "StandardOutput", types.DefaultStandardOutput, true)
	stdErr := c.Get(types.SectionService, "StandardError", types.DefaultStandardError, true)

	result := &StandardIO{}
	var err error

	// stdin
	switch {
	case stdInp == "null":
		result.InpFile, err = os.Open(types.DevNull)
	case strings.HasPrefix(stdInp, "file:"):
		fname := stdInp[len("file:"):]
		if _, statErr := os.Stat(fname); statErr == nil {
			result.InpFile, err = os.Open(fname)
		} else {
			result.InpFile, err = os.Open(types.DevZero)
		}
	default:
		result.InpFile, err = os.Open(types.DevZero)
	}
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}

	// stdout
	openOutput := func(spec string) (*os.File, error) {
		switch {
		case spec == "null":
			return os.OpenFile(types.DevNull, os.O_WRONLY, 0)
		case strings.HasPrefix(spec, "file:"):
			fname := spec[len("file:"):]
			_ = os.MkdirAll(filepath.Dir(fname), 0o755)
			return os.Create(fname)
		case strings.HasPrefix(spec, "append:"):
			fname := spec[len("append:"):]
			_ = os.MkdirAll(filepath.Dir(fname), 0o755)
			return os.OpenFile(fname, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		}
		return nil, nil // use journal log
	}

	result.OutFile, err = openOutput(stdOut)
	if err != nil || result.OutFile == nil {
		result.OutFile, err = j.OpenLog(c)
		if err != nil {
			return nil, fmt.Errorf("stdout (journal): %w", err)
		}
		result.ErrFile = result.OutFile
	}

	if stdErr == "inherit" {
		result.ErrFile = result.OutFile
	} else {
		result.ErrFile, err = openOutput(stdErr)
		if err != nil || result.ErrFile == nil {
			result.ErrFile, err = j.OpenLog(c)
			if err != nil {
				return nil, fmt.Errorf("stderr (journal): %w", err)
			}
		}
	}
	return result, nil
}