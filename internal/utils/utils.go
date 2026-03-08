// Package utils contains pure helper functions with no side effects.
package utils

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// ── string helpers ─────────────────────────────────────────────────────────

// StrINET converts a socket type integer to a protocol name string.
func StrINET(value int) string {
	switch value {
	case int(syscall.SOCK_DGRAM):
		return "UDP"
	case int(syscall.SOCK_STREAM):
		return "TCP"
	case int(syscall.SOCK_RAW):
		return "RAW"
	case int(syscall.SOCK_RDM):
		return "RDM"
	case int(syscall.SOCK_SEQPACKET):
		return "SEQ"
	}
	return "<?>"
}

// StrYes returns "yes" / "no" for boolean-like values.
func StrYes(value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case string:
		if v == "" {
			return "no"
		}
		return v
	case nil:
		return "no"
	}
	return fmt.Sprintf("%v", value)
}

// StrE converts nil/false/"" to empty string, true to "*", anything else to its string form.
func StrE(part any) string {
	if part == nil {
		return ""
	}
	switch v := part.(type) {
	case bool:
		if !v {
			return ""
		}
		return "*"
	case string:
		return v
	case int:
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	}
	return fmt.Sprintf("%v", part)
}

// StrQ quotes a string with single quotes; returns empty string for nil.
func StrQ(part any) string {
	if part == nil {
		return ""
	}
	switch v := part.(type) {
	case int:
		return strconv.Itoa(v)
	case string:
		return "'" + v + "'"
	}
	return fmt.Sprintf("'%v'", part)
}

// ShellCmd returns a human-readable shell-command string from a slice.
func ShellCmd(cmd []string) string {
	parts := make([]string, len(cmd))
	for i, p := range cmd {
		parts[i] = StrQ(p)
	}
	return strings.Join(parts, " ")
}

// ── numeric helpers ────────────────────────────────────────────────────────

// ToIntN converts a string to *int; returns nil/default on failure.
func ToIntN(value string, defaultVal *int) *int {
	if value == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		return defaultVal
	}
	return &i
}

// ToInt converts a string to int with a fallback.
func ToInt(value any, defaultVal int) int {
	switch v := value.(type) {
	case int:
		return v
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return defaultVal
		}
		return i
	}
	return defaultVal
}

// IntMode parses an octal permission string (e.g. "0755").
func IntMode(value string) (os.FileMode, bool) {
	i, err := strconv.ParseInt(value, 8, 64)
	if err != nil {
		return 0, false
	}
	return os.FileMode(i), true
}

// ── list helpers ───────────────────────────────────────────────────────────

// ToList converts various inputs to []string.
func ToList(value any) []string {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return strings.Split(v, ",")
	}
	return nil
}

// Wordlist splits each element of the input on spaces and returns a flat list of non-empty tokens.
func Wordlist(values []string) []string {
	var result []string
	for _, val := range values {
		if val == "" {
			continue
		}
		for _, elem := range strings.Fields(val) {
			name := strings.TrimSpace(elem)
			if name != "" {
				result = append(result, name)
			}
		}
	}
	return result
}

// Commalist splits each element on commas and returns a flat list of non-empty tokens.
func Commalist(values []string) []string {
	var result []string
	for _, val := range values {
		if val == "" {
			continue
		}
		for _, elem := range strings.Split(strings.TrimSpace(val), ",") {
			name := strings.TrimSpace(elem)
			if name != "" {
				result = append(result, name)
			}
		}
	}
	return result
}

// ── unit name helpers ──────────────────────────────────────────────────────

// UnitOf appends ".service" if the module name has no extension.
func UnitOf(module string) string {
	if !strings.Contains(module, ".") {
		return module + ".service"
	}
	return module
}

// GetUnitType returns the unit type (file extension without dot) if the extension
// is at least 5 characters long, otherwise nil.
func GetUnitType(module string) string {
	name := filepath.Base(module)
	ext := filepath.Ext(name)
	if len(ext) > 5 { // e.g. ".service" = 8 chars
		return ext[1:]
	}
	return ""
}

// UnitNameEscape escapes a unit name according to the systemd spec.
func UnitNameEscape(text string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9:_./]`)
	esc := re.ReplaceAllStringFunc(text, func(s string) string {
		return fmt.Sprintf("\\x%02x", s[0])
	})
	return strings.ReplaceAll(esc, "/", "-")
}

// UnitNameUnescape reverses UnitNameEscape.
func UnitNameUnescape(text string) string {
	esc := strings.ReplaceAll(text, "-", "/")
	re := regexp.MustCompile(`\\x(..)`)
	return re.ReplaceAllStringFunc(esc, func(s string) string {
		n, err := strconv.ParseInt(s[2:], 16, 32)
		if err != nil {
			return s
		}
		return string(rune(n))
	})
}

// ── truncation helpers ─────────────────────────────────────────────────────

// O22 truncates a string to 22 visible characters.
func O22(part string) string {
	if len(part) <= 22 {
		return part
	}
	return part[:5] + "..." + part[len(part)-14:]
}

// O44 truncates a string to 44 visible characters.
func O44(part string) string {
	if len(part) <= 44 {
		return part
	}
	return part[:10] + "..." + part[len(part)-31:]
}

// O77 truncates a string to 77 visible characters.
func O77(part string) string {
	if len(part) <= 77 {
		return part
	}
	return part[:20] + "..." + part[len(part)-54:]
}

// Delayed returns a human-readable attempt indicator used in lock-wait logging.
func Delayed(attempt int, suffix string) string {
	if suffix == "" {
		suffix = "."
	}
	if attempt == 0 {
		return ".." + suffix
	}
	if attempt < 10 {
		return fmt.Sprintf("%+d%s", attempt, suffix)
	}
	return fmt.Sprintf("%d%s", attempt, suffix)
}

// ── glob / fnmatch helper ──────────────────────────────────────────────────

// FnMatched returns true if text matches any of the given glob patterns.
// An empty patterns list always matches.
func FnMatched(text string, patterns ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "" {
			return true
		}
		matched, err := filepath.Match(p, text)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// ── unit parsing ───────────────────────────────────────────────────────────

// UnitName holds the parsed components of a unit file name.
type UnitName struct {
	FullName  string
	Name      string
	Prefix    string
	Instance  string
	Suffix    string
	Component string
}

// ParseUnit splits a full unit name into its constituent parts.
func ParseUnit(fullname string) UnitName {
	name, suffix := fullname, ""
	if idx := strings.LastIndex(fullname, "."); idx > 0 {
		name = fullname[:idx]
		suffix = fullname[idx+1:]
	}
	prefix, instance := name, ""
	if idx := strings.Index(name, "@"); idx > 0 {
		prefix = name[:idx]
		instance = name[idx+1:]
	}
	component := ""
	if idx := strings.LastIndex(prefix, "-"); idx > 0 {
		component = prefix[idx+1:]
	}
	return UnitName{
		FullName:  fullname,
		Name:      name,
		Prefix:    prefix,
		Instance:  instance,
		Suffix:    suffix,
		Component: component,
	}
}

// ── time conversion ────────────────────────────────────────────────────────

// TimeToSeconds converts a systemd time string (e.g. "1min 30s") to seconds.
func TimeToSeconds(text string, maximum float64) float64 {
	var value float64
	for _, part := range strings.Fields(text) {
		item := strings.TrimSpace(part)
		if item == "infinity" {
			return maximum
		}
		var val string
		var multiplier float64
		switch {
		case strings.HasSuffix(item, "min"):
			val = item[:len(item)-3]
			multiplier = 60
		case strings.HasSuffix(item, "ms"):
			val = item[:len(item)-2]
			multiplier = 0.001
		case strings.HasSuffix(item, "m"):
			val = item[:len(item)-1]
			multiplier = 60
		case strings.HasSuffix(item, "s"):
			val = item[:len(item)-1]
			multiplier = 1
		default:
			val = item
			multiplier = 1
		}
		if val == "" {
			continue
		}
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			n = math.Pow(10, float64(len(val))) - 1
		}
		value += n * multiplier
	}
	if value > maximum {
		return maximum
	}
	if value == 0 && strings.TrimSpace(text) == "0" {
		return 0
	}
	if value == 0 {
		return 1
	}
	return value
}

// SecondsToTime converts a float seconds value to a human-readable string.
func SecondsToTime(seconds float64) string {
	mins := int(seconds) / 60
	secs := int(seconds) - mins*60
	msecs := int(seconds*1000) - (secs*1000 + mins*60000)
	switch {
	case mins > 0 && secs > 0 && msecs > 0:
		return fmt.Sprintf("%dmin %ds %dms", mins, secs, msecs)
	case mins > 0 && secs > 0:
		return fmt.Sprintf("%dmin %ds", mins, secs)
	case secs > 0 && msecs > 0:
		return fmt.Sprintf("%ds %dms", secs, msecs)
	case mins > 0 && msecs > 0:
		return fmt.Sprintf("%dmin %dms", mins, msecs)
	case mins > 0:
		return fmt.Sprintf("%dmin", mins)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// ── exec / load path prefix parsing ───────────────────────────────────────

// ExecMode holds the parsed prefix flags from an ExecStart= value.
type ExecMode struct {
	Mode     string
	Check    bool // absence of "-" means errors are fatal
	NoUser   bool // "+" or "!" prefix
	NoExpand bool // ":" prefix
	Argv0    bool // "@" prefix
}

// Checkprefix splits an exec command into its prefix characters and the command itself.
func Checkprefix(cmd string) (string, string) {
	prefix := ""
	for i, c := range cmd {
		if strings.ContainsRune("-+!@:|", c) {
			prefix += string(c)
		} else {
			return prefix, cmd[i:]
		}
	}
	return prefix, ""
}

// ExecPath parses the leading prefix of an ExecXxx= value and returns the mode and clean path.
func ExecPath(cmd string) (ExecMode, string) {
	prefix, newCmd := Checkprefix(cmd)
	return ExecMode{
		Mode:     prefix,
		Check:    !strings.Contains(prefix, "-"),
		NoUser:   strings.ContainsAny(prefix, "+!"),
		NoExpand: strings.Contains(prefix, ":"),
		Argv0:    strings.Contains(prefix, "@"),
	}, newCmd
}

// LoadMode holds the parsed prefix flags from a load path.
type LoadMode struct {
	Mode  string
	Check bool
}

// LoadPath parses the leading "-" characters from a load reference.
func LoadPath(ref string) (LoadMode, string) {
	prefix, filename := "", ref
	for strings.HasPrefix(filename, "-") {
		prefix += "-"
		filename = filename[1:]
	}
	return LoadMode{Mode: prefix, Check: !strings.Contains(prefix, "-")}, filename
}

// ── path helpers ───────────────────────────────────────────────────────────

// PathReplaceExtension replaces an old extension with a new one.
func PathReplaceExtension(path, old, new string) string {
	if strings.HasSuffix(path, old) {
		path = path[:len(path)-len(old)]
	}
	return path + new
}

// GetExistPath returns the first path in the list that exists on the filesystem.
func GetExistPath(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
