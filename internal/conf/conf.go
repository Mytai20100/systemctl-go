package conf

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ── Conf ───────────────────────────────────────────────────────────────────

// Conf is the runtime wrapper around a parsed unit file.
// It holds the parsed data plus runtime state (env, status, masked path …).
type Conf struct {
	Data          *ConfigParser
	Env           map[string]string
	Status        map[string]string // runtime status key=value pairs
	Masked        string            // non-empty when the unit is masked
	Module        string            // the unit name as requested by the caller
	NonloadedPath string            // path when masked
	DropInFiles   map[string]string // name -> full path of drop-in .conf files
	Root          string
	UserMode      bool
}

// NewConf creates a Conf wrapping the given parser.
func NewConf(data *ConfigParser, module string) *Conf {
	return &Conf{
		Data:        data,
		Env:         map[string]string{},
		DropInFiles: map[string]string{},
		Module:      module,
	}
}

// RootMode returns true when operating in system (root) mode.
func (c *Conf) RootMode() bool { return !c.UserMode }

// IsLoaded returns the loading state string: "masked", "loaded", or "".
func (c *Conf) IsLoaded() string {
	if c.Masked != "" {
		return "masked"
	}
	if len(c.Data.Filenames()) > 0 {
		return "loaded"
	}
	return ""
}

// Filename returns the primary filename that was parsed (first in the list).
func (c *Conf) Filename() string {
	files := c.Data.Filenames()
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// Overrides returns drop-in file paths sorted by name (not full path).
func (c *Conf) Overrides() []string {
	names := make([]string, 0, len(c.DropInFiles))
	for n := range c.DropInFiles {
		names = append(names, n)
	}
	// sort alphabetically by name
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	result := make([]string, len(names))
	for i, n := range names {
		result[i] = c.DropInFiles[n]
	}
	return result
}

// Name returns the unit id; falls back to the basename of the first file.
func (c *Conf) Name() string {
	if c.Module != "" {
		return c.Module
	}
	f := c.Filename()
	if f != "" {
		return filepath.Base(f)
	}
	return ""
}

// Set delegates to the underlying ConfData.
func (c *Conf) Set(section, name string, value *string) { c.Data.Set(section, name, value) }

// SetStr delegates to the underlying ConfData.
func (c *Conf) SetStr(section, name, value string) { c.Data.SetStr(section, name, value) }

// Get returns the first value or the default; always returns a string.
func (c *Conf) Get(section, name, defaultVal string, allowNoValue bool) string {
	return c.Data.GetStr(section, name, defaultVal, allowNoValue)
}

// GetList returns all values or the default slice.
func (c *Conf) GetList(section, name string, defaultVals []string, allowNoValue bool) []string {
	vals, err := c.Data.GetList(section, name, defaultVals, allowNoValue)
	if err != nil {
		return defaultVals
	}
	return vals
}

// GetBool interprets the first value of the option as a boolean.
func (c *Conf) GetBool(section, name, defaultVal string) bool {
	val, _ := c.Data.Get(section, name, defaultVal, true)
	if val == "" {
		val = defaultVal
	}
	if len(val) > 0 {
		if strings.ContainsRune("TtYy123456789", rune(val[0])) {
			return true
		}
	}
	return false
}

// ── Socket ────────────────────────────────────────────────────────────────

// Socket wraps a net.Listener/net.PacketConn together with the unit conf.
type Socket struct {
	Conf net.Listener // may be nil for datagram sockets
	Sock interface{}  // net.Listener or net.PacketConn
	Skip bool
	conf *Conf
}

// NewSocket creates a Socket associated with the given Conf.
func NewSocket(c *Conf, listener interface{}, skip bool) *Socket {
	return &Socket{conf: c, Sock: listener, Skip: skip}
}

// Name returns the unit name.
func (s *Socket) Name() string { return s.conf.Name() }

// Addr returns the listen address from the unit configuration.
func (s *Socket) Addr() string {
	stream := s.conf.Get("Socket", "ListenStream", "", true)
	dgram  := s.conf.Get("Socket", "ListenDatagram", "", true)
	if stream != "" {
		return stream
	}
	return dgram
}

// Close closes the underlying socket.
func (s *Socket) Close() {
	if l, ok := s.Sock.(net.Listener); ok {
		_ = l.Close()
	}
}

// Fileno returns the file descriptor number of the underlying socket, or -1.
func (s *Socket) Fileno() int {
	type filer interface {
		File() (*os.File, error)
	}
	if l, ok := s.Sock.(filer); ok {
		f, err := l.File()
		if err == nil {
			defer f.Close()
			return int(f.Fd())
		}
	}
	return -1
}

// Listen is a no-op for sockets already bound by CreateSocket.
func (s *Socket) Listen() {}
