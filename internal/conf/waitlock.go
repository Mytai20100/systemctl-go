package conf

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"systemctl-go/internal/paths"
	"systemctl-go/internal/types"
)

// Waitlock implements a file-lock-based mutex so that parallel systemctl
// invocations cannot simultaneously operate on the same unit.
type Waitlock struct {
	conf       *Conf
	opened     int
	lockfolder string
}

// NewWaitlock creates a Waitlock for the given conf.
// The lock folder is created immediately if it does not exist.
func NewWaitlock(conf *Conf) *Waitlock {
	root := true
	if conf != nil {
		root = conf.RootMode()
	}
	folder := paths.ExpandPath(types.NotifySocketFolder, root)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		logg.Warnf("oops >> %v", err)
	}
	return &Waitlock{conf: conf, opened: -1, lockfolder: folder}
}

func (w *Waitlock) lockfile() string {
	unit := ""
	if w.conf != nil {
		unit = w.conf.Name()
	}
	name := unit
	if name == "" {
		name = "global"
	}
	return filepath.Join(w.lockfolder, name+".lock")
}

// Lock acquires the file lock, blocking up to DefaultMaximumTimeout seconds.
// Returns true on success.
func (w *Waitlock) Lock() bool {
	lockfile := w.lockfile()
	lockname := filepath.Base(lockfile)
	fd, err := syscall.Open(lockfile, syscall.O_RDWR|syscall.O_CREAT, 0o600)
	if err != nil {
		logg.Warnf("[%d] oops %T >> %v", os.Getpid(), err, err)
		return false
	}
	w.opened = fd
	maxWait := types.DefaultMaximumTimeout

	for attempt := 0; attempt < maxWait; attempt++ {
		logg.Log(types.DebugFlock, "[%d] trying %s _______", os.Getpid(), lockname)

		// Non-blocking exclusive lock attempt
		err := unix.FcntlFlock(uintptr(fd), unix.F_SETLK, &unix.Flock_t{
			Type:   unix.F_WRLCK,
			Whence: 0,
			Start:  0,
			Len:    0,
		})
		if err != nil {
			// Lock held by someone else
			buf := make([]byte, 4096)
			n, _ := syscall.Read(fd, buf)
			logg.Infof("[%d] systemctl locked by %s", os.Getpid(), string(buf[:n]))
			_, _ = syscall.Seek(fd, 0, 0)
			time.Sleep(time.Second)
			continue
		}

		// Verify the inode still exists (wasn't deleted while we waited)
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil || stat.Nlink == 0 {
			logg.Log(types.DebugFlock, "[%d] %s got deleted, reopening", os.Getpid(), lockname)
			_ = syscall.Close(fd)
			fd, err = syscall.Open(lockfile, syscall.O_RDWR|syscall.O_CREAT, 0o600)
			if err != nil {
				return false
			}
			w.opened = fd
			continue
		}

		content := fmt.Sprintf("{ 'systemctl': %d, 'lock': '%s' }\n", os.Getpid(), lockname)
		_, _ = syscall.Write(fd, []byte(content))
		logg.Log(types.DebugFlock, "[%d] holding lock on %s", os.Getpid(), lockname)
		return true
	}
	logg.Errorf("[%d] not able to get the lock to %s", os.Getpid(), lockname)
	return false
}

// Unlock releases the file lock.
func (w *Waitlock) Unlock() {
	if w.opened < 0 {
		return
	}
	_, _ = syscall.Seek(w.opened, 0, 0)
	_ = syscall.Ftruncate(w.opened, 0)
	_ = unix.FcntlFlock(uintptr(w.opened), unix.F_SETLK, &unix.Flock_t{
		Type:   unix.F_UNLCK,
		Whence: 0,
		Start:  0,
		Len:    0,
	})
	_ = syscall.Close(w.opened)
	w.opened = -1
}
