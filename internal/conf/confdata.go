// Package conf contains the unit-file configuration parser and the runtime
// wrapper types that hold the parsed data together with unit state.
package conf

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gdraheim/systemctl-go/internal/logger"
	"github.com/gdraheim/systemctl-go/internal/types"
	"github.com/gdraheim/systemctl-go/internal/utils"
)

var logg = logger.GetLogger("conf")

// ── ConfData ───────────────────────────────────────────────────────────────

// ConfData stores the raw parsed data from a unit file.
// Each section maps option names to a list of values (multiple assignments
// accumulate as slices; an empty reset resets to nil).
type ConfData struct {
	defaults map[string]string
	conf     map[string]map[string][]string // section -> option -> values
	files    []string
}

// NewConfData creates an empty ConfData.
func NewConfData(defaults map[string]string) *ConfData {
	if defaults == nil {
		defaults = map[string]string{}
	}
	return &ConfData{
		defaults: defaults,
		conf:     map[string]map[string][]string{},
	}
}

func (c *ConfData) Defaults() map[string]string { return c.defaults }

func (c *ConfData) Sections() []string {
	keys := make([]string, 0, len(c.conf))
	for k := range c.conf {
		keys = append(keys, k)
	}
	return keys
}

func (c *ConfData) AddSection(section string) {
	if _, ok := c.conf[section]; !ok {
		c.conf[section] = map[string][]string{}
	}
}

func (c *ConfData) HasSection(section string) bool {
	_, ok := c.conf[section]
	return ok
}

func (c *ConfData) HasOption(section, option string) bool {
	s, ok := c.conf[section]
	if !ok {
		return false
	}
	_, ok = s[option]
	return ok
}

// Set appends value to the option list; a nil value resets the list.
func (c *ConfData) Set(section, option string, value *string) {
	if _, ok := c.conf[section]; !ok {
		c.conf[section] = map[string][]string{}
	}
	if value == nil {
		c.conf[section][option] = nil
	} else if _, exists := c.conf[section][option]; !exists {
		c.conf[section][option] = []string{*value}
	} else {
		c.conf[section][option] = append(c.conf[section][option], *value)
	}
}

// SetStr is a convenience wrapper that takes a plain string value.
func (c *ConfData) SetStr(section, option, value string) {
	c.Set(section, option, &value)
}

// Get returns the first value for the option or the provided default.
// Returns ("", false) when the option is not present and no default given.
func (c *ConfData) Get(section, option, defaultVal string, allowNoValue bool) (string, error) {
	s, ok := c.conf[section]
	if !ok {
		if defaultVal != "" || allowNoValue {
			return defaultVal, nil
		}
		logg.Warnf("section %s does not exist (have %v)", section, c.Sections())
		return "", fmt.Errorf("section %s does not exist", section)
	}
	vals, ok := s[option]
	if !ok {
		if defaultVal != "" || allowNoValue {
			return defaultVal, nil
		}
		return "", fmt.Errorf("option %s in %s does not exist", option, section)
	}
	if len(vals) == 0 {
		if defaultVal != "" || allowNoValue {
			return defaultVal, nil
		}
		return "", fmt.Errorf("option %s in %s is empty", option, section)
	}
	return vals[0], nil
}

// GetStr always returns a string (uses utils.StrE on the default).
func (c *ConfData) GetStr(section, option, defaultVal string, allowNoValue bool) string {
	v, err := c.Get(section, option, utils.StrE(defaultVal), allowNoValue)
	if err != nil {
		return utils.StrE(defaultVal)
	}
	return v
}

// GetList returns all accumulated values for the option.
func (c *ConfData) GetList(section, option string, defaultVals []string, allowNoValue bool) ([]string, error) {
	s, ok := c.conf[section]
	if !ok {
		if defaultVals != nil || allowNoValue {
			return defaultVals, nil
		}
		logg.Warnf("section %s does not exist (have %v)", section, c.Sections())
		return nil, fmt.Errorf("section %s does not exist", section)
	}
	vals, ok := s[option]
	if !ok {
		if defaultVals != nil || allowNoValue {
			return defaultVals, nil
		}
		return nil, fmt.Errorf("option %s in %s does not exist", option, section)
	}
	return vals, nil
}

func (c *ConfData) Filenames() []string { return c.files }

// ── ConfigParser ───────────────────────────────────────────────────────────

// ConfigParser extends ConfData with unit-file and sysv file readers.
type ConfigParser struct {
	*ConfData
}

// NewConfigParser creates a ConfigParser backed by a new ConfData.
func NewConfigParser() *ConfigParser {
	return &ConfigParser{ConfData: NewConfData(nil)}
}

// Read is an alias for ReadUnitFile.
func (p *ConfigParser) Read(filename string) (*ConfigParser, error) {
	return p.ReadUnitFile(filename)
}

// ReadUnitFile parses a systemd unit file (INI-like format with multi-line support).
func (p *ConfigParser) ReadUnitFile(filename string) (*ConfigParser, error) {
	section := "GLOBAL"
	nextLine := false
	var name, text string

	if st, err := os.Stat(filename); err == nil && !st.IsDir() {
		p.files = append(p.files, filename)
	}

	f, err := os.Open(filename)
	if err != nil {
		return p, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		origLine := scanner.Text()
		if nextLine {
			text += origLine + "\n"
			if strings.HasSuffix(strings.TrimRight(text, "\n"), "\\") {
				// continuation continues
			} else {
				p.SetStr(section, name, text)
				nextLine = false
			}
			continue
		}
		line := strings.TrimSpace(origLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, ".include") {
			logg.Errorf("the '.include' syntax is deprecated; use drop-in files")
			includefile := strings.TrimSpace(regexp.MustCompile(`^\.include\s*`).ReplaceAllString(line, ""))
			if !filepath.IsAbs(includefile) {
				includefile = filepath.Join(filepath.Dir(filename), includefile)
			}
			if _, err := p.ReadUnitFile(includefile); err != nil {
				return p, fmt.Errorf("include %s: %w", includefile, err)
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			if x := strings.Index(line, "]"); x > 0 {
				section = line[1:x]
				p.AddSection(section)
			}
			continue
		}
		m := regexp.MustCompile(`^(\w+)\s*=(.*)`).FindStringSubmatch(line)
		if m == nil {
			logg.Warnf("bad ini line: %s", line)
			return p, fmt.Errorf("bad ini line: %s", line)
		}
		name = m[1]
		text = strings.TrimSpace(m[2])
		if strings.HasSuffix(text, "\\") {
			nextLine = true
			text += "\n"
		} else {
			if text != "" {
				p.SetStr(section, name, text)
			} else {
				p.Set(section, name, nil) // empty value resets the list
			}
		}
	}
	if nextLine && name != "" {
		p.SetStr(section, name, text)
	}
	return p, scanner.Err()
}

// ReadSysvFile parses a SysV init script LSB header.
func (p *ConfigParser) ReadSysvFile(filename string) (*ConfigParser, error) {
	if st, err := os.Stat(filename); err == nil && !st.IsDir() {
		p.files = append(p.files, filename)
	}

	f, err := os.Open(filename)
	if err != nil {
		return p, err
	}
	defer f.Close()

	section := "GLOBAL"
	initInfo := false

	re := regexp.MustCompile(`\S+\s*(\w[\w_-]*):\s*(.*)`)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			if strings.Contains(line, " BEGIN INIT INFO") {
				initInfo = true
				section = "init.d"
			}
			if strings.Contains(line, " END INIT INFO") {
				initInfo = false
			}
			if initInfo {
				if m := re.FindStringSubmatch(line); m != nil {
					p.SetStr(section, m[1], strings.TrimSpace(m[2]))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return p, err
	}
	p.systemdSysvGenerator(filename)
	return p, nil
}

var sysVMappings = map[string]string{
	"$local_fs":  "local-fs.target",
	"$network":   "network.target",
	"$remote_fs": "remote-fs.target",
	"$timer":     "timers.target",
}

var runlevelMappings = map[string]string{
	"0": "poweroff.target",
	"1": "rescue.target",
	"2": "multi-user.target",
	"3": "multi-user.target",
	"4": "multi-user.target",
	"5": "graphical.target",
	"6": "reboot.target",
}

func strP(s string) *string { return &s }

func (p *ConfigParser) systemdSysvGenerator(filename string) {
	p.Set(types.SectionUnit, "SourcePath", strP(filename))
	if desc, _ := p.ConfData.Get("init.d", "Description", "", true); desc != "" {
		p.SetStr(types.SectionUnit, "Description", desc)
	}
	if check, _ := p.ConfData.Get("init.d", "Required-Start", "", true); check != "" {
		for _, item := range strings.Fields(check) {
			if target, ok := sysVMappings[item]; ok {
				p.SetStr(types.SectionUnit, "Requires", target)
			}
		}
	}
	if provides, _ := p.ConfData.Get("init.d", "Provides", "", true); provides != "" {
		p.SetStr(types.SectionInstall, "Alias", provides)
	}
	runlevels, _ := p.ConfData.Get("init.d", "Default-Start", "3 5", true)
	for _, item := range strings.Fields(runlevels) {
		if target, ok := runlevelMappings[item]; ok {
			p.SetStr(types.SectionInstall, "WantedBy", target)
		}
	}
	defaultTimeout := fmt.Sprintf("%d", types.DefaultMaximumTimeout)
	p.SetStr(types.SectionService, "Restart", "no")
	p.SetStr(types.SectionService, "TimeoutSec", defaultTimeout)
	p.SetStr(types.SectionService, "KillMode", "process")
	p.SetStr(types.SectionService, "GuessMainPID", "no")
	p.SetStr(types.SectionService, "ExecStart", filename+" start")
	p.SetStr(types.SectionService, "ExecStop", filename+" stop")
	if desc, _ := p.ConfData.Get("init.d", "Description", "", true); desc != "" {
		p.SetStr(types.SectionService, "ExecReload", filename+" reload")
	}
	p.SetStr(types.SectionService, "Type", "forking")
}

// ReadEnvFile yields (name, value) pairs from an environment file.
func ReadEnvFile(filename, root string) ([][2]string, error) {
	var path string
	if root != "" {
		path = filepath.Join(root, strings.TrimPrefix(filename, "/"))
	} else {
		path = filename
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var results [][2]string
	reSingle := regexp.MustCompile(`(?:export\s+)?([\w_]+)='([^']*)'`)
	reDouble := regexp.MustCompile(`(?:export\s+)?([\w_]+)="([^"]*)"`)
	rePlain  := regexp.MustCompile(`(?:export\s+)?([\w_]+)=(.*)`)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reSingle.FindStringSubmatch(line); m != nil {
			results = append(results, [2]string{m[1], m[2]})
			continue
		}
		if m := reDouble.FindStringSubmatch(line); m != nil {
			results = append(results, [2]string{m[1], m[2]})
			continue
		}
		if m := rePlain.FindStringSubmatch(line); m != nil {
			results = append(results, [2]string{m[1], m[2]})
		}
	}
	return results, scanner.Err()
}
