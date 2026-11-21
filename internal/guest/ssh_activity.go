package guest

import (
	"io"
	"sync"
	"time"
)

// sshActivity holds only the latest application event from the SSH server.
type sshActivity struct {
	mu sync.Mutex
	at time.Time
}

func (a *sshActivity) record() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.at = time.Now().UTC()
}

func (a *sshActivity) latest() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.at
}

// sshActivityWriter observes data accepted by the application without retaining
// any of the contents or counting SSH protocol messages and server output.
type sshActivityWriter struct {
	dst      io.Writer
	activity *sshActivity
}

func (w sshActivityWriter) Write(data []byte) (int, error) {
	n, err := w.dst.Write(data)
	if n > 0 {
		w.activity.record()
	}
	return n, err
}
