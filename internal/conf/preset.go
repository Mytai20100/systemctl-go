package conf

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
)

// PresetFile scans *.preset files to determine the default enable/disable
// status for unit files.
type PresetFile struct {
	files []string
	lines []string
}

// NewPresetFile creates an empty PresetFile.
func NewPresetFile() *PresetFile { return &PresetFile{} }

// Filename returns the most recently read file, or empty string.
func (p *PresetFile) Filename() string {
	if len(p.files) == 0 {
		return ""
	}
	return p.files[len(p.files)-1]
}

// Read appends the contents of filename to the internal line list.
func (p *PresetFile) Read(filename string) (*PresetFile, error) {
	p.files = append(p.files, filename)
	f, err := os.Open(filename)
	if err != nil {
		return p, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		p.lines = append(p.lines, scanner.Text())
	}
	return p, scanner.Err()
}

var presetRe = regexp.MustCompile(`^(enable|disable)\s+(\S+)`)

// GetPreset returns "enable" or "disable" for the unit, or "" if no rule matches.
func (p *PresetFile) GetPreset(unit string) string {
	for _, line := range p.lines {
		m := presetRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		status, pattern := m[1], m[2]
		matched, err := filepath.Match(pattern, unit)
		if err == nil && matched {
			logg.Debugf("%s %s => %s %s", status, pattern, unit, p.Filename())
			return status
		}
	}
	return ""
}
