// Package listen implements the socket-activation listener that runs as a
// goroutine parallel to the main init loop.
package listen

import (
	"os"
	"sync"
	"time"

	"github.com/gdraheim/systemctl-go/internal/logger"
	"github.com/gdraheim/systemctl-go/internal/types"
)

var logg = logger.GetLogger("listen")

// SocketItem is the minimal interface the listener needs from a socket object.
type SocketItem interface {
	// Fileno returns the underlying file descriptor number.
	Fileno() int
	// Name returns the unit name.
	Name() string
	// Addr returns the listen address string.
	Addr() string
	// Listen activates the socket for accepting connections.
	Listen()
	// Close shuts down the socket.
	Close()
}

// Acceptor is called by the listener when a connection arrives on a socket.
type Acceptor interface {
	DoAcceptSocketFrom(sock SocketItem)
}

// Controller exposes the pieces of Systemctl that the listener needs.
type Controller interface {
	Acceptor
	SocketList() []SocketItem
	LoopSleep() int
	LoopLock() *sync.Mutex
}

// ListenThread runs the socket-activation poll loop in a background goroutine.
type ListenThread struct {
	ctrl    Controller
	stopped chan struct{}
}

// New creates a ListenThread backed by the given controller.
func New(ctrl Controller) *ListenThread {
	return &ListenThread{ctrl: ctrl, stopped: make(chan struct{})}
}

// Stop signals the listen loop to exit.
func (lt *ListenThread) Stop() { close(lt.stopped) }

// IsStopped returns true after Stop has been called.
func (lt *ListenThread) IsStopped() bool {
	select {
	case <-lt.stopped:
		return true
	default:
		return false
	}
}

// Run executes the listen loop; call this in a goroutine.
func (lt *ListenThread) Run() {
	me := os.Getpid()
	logg.Log(types.DebugInitLoop, "[%d] listen: new goroutine", me)

	socketList := lt.ctrl.SocketList()
	if len(socketList) == 0 {
		return
	}
	logg.Log(types.DebugInitLoop, "[%d] listen: starting with %d sockets", me, len(socketList))

	for _, sock := range socketList {
		sock.Listen()
		logg.Debugf("[%d] listen: %s :%s", me, sock.Name(), sock.Addr())
	}

	minimumYield := types.MinimumYield
	timestamp := time.Now()

	for !lt.IsStopped() {
		// Sleep logic: wait up to loop_sleep seconds, breaking early for signals.
		sleepSec := float64(lt.ctrl.LoopSleep()) - time.Since(timestamp).Seconds()
		if sleepSec < minimumYield {
			sleepSec = minimumYield
		}
		sleeping := sleepSec
		for sleeping > 2 {
			time.Sleep(time.Second)
			sleeping = float64(lt.ctrl.LoopSleep()) - time.Since(timestamp).Seconds()
			if sleeping < minimumYield {
				sleeping = minimumYield
				break
			}
		}
		time.Sleep(time.Duration(sleeping * float64(time.Second)))

		logg.Log(types.DebugInitLoop, "[%d] listen: poll", me)

		// Non-blocking check for readable sockets using a poll-style select.
		// Go's net package does not expose raw poll; we use a channel-based approach.
		timestamp = time.Now()
		for _, sock := range socketList {
			if lt.IsStopped() {
				break
			}
			// Try to accept in a non-blocking goroutine
			done := make(chan struct{}, 1)
			go func(s SocketItem) {
				defer func() { done <- struct{}{} }()
				lt.ctrl.LoopLock().Lock()
				defer lt.ctrl.LoopLock().Unlock()
				logg.Debugf("[%d] listen: accept %s :%d", me, s.Name(), s.Fileno())
				lt.ctrl.DoAcceptSocketFrom(s)
			}(sock)
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	// Clean up on exit
	for _, sock := range socketList {
		sock.Close()
	}
}
