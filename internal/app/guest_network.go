package app

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
)

// listenGuestNetwork defers only a missing UTM bridge bind. On a cold start,
// UTM can create the bridge as the first guest boots and begins enrollment.
func listenGuestNetwork(address string, open func(string, string) (net.Listener, error)) (net.Listener, error) {
	listener, err := open("tcp", address)
	if !errors.Is(err, syscall.EADDRNOTAVAIL) {
		return listener, err
	}
	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil || host != config.UTMSharedHost {
		return nil, err
	}
	bind, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil {
		return nil, err
	}
	var reservation net.Listener
	if bind.Port == 0 {
		// Platform validation needs to put a port in the seed before booting
		// its VM. Reserve an ephemeral port locally without serving on it.
		reservation, err = open("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		local, ok := reservation.Addr().(*net.TCPAddr)
		if !ok || local.Port == 0 {
			return nil, errors.Join(errors.New("could not reserve an ephemeral validation port"), reservation.Close())
		}
		bind.Port = local.Port
	}
	return &bridgeListener{address: bind, open: open, reservation: reservation, done: make(chan struct{})}, nil
}

// bridgeListener publishes a fixed address while it waits to bind. The
// server's Accept loop owns the retry and Close releases it on shutdown.
type bridgeListener struct {
	address *net.TCPAddr
	open    func(string, string) (net.Listener, error)
	done    chan struct{}

	mu          sync.Mutex
	network     net.Listener
	reservation net.Listener
}

func (l *bridgeListener) Accept() (net.Conn, error) {
	for {
		network, err := l.bind()
		if err == nil {
			return network.Accept()
		}
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return nil, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-l.done:
			timer.Stop()
			return nil, net.ErrClosed
		case <-timer.C:
		}
	}
}

func (l *bridgeListener) Addr() net.Addr { return l.address }

func (l *bridgeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed() {
		return net.ErrClosed
	}
	close(l.done)
	return errors.Join(closeGuestNetwork(l.network), closeGuestNetwork(l.reservation))
}

func (l *bridgeListener) bind() (net.Listener, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed() {
		return nil, net.ErrClosed
	}
	if l.network != nil {
		return l.network, nil
	}
	network, err := l.open("tcp4", l.address.String())
	if err != nil {
		return nil, err
	}
	l.network = network
	err = closeGuestNetwork(l.reservation)
	l.reservation = nil
	return network, err
}

func (l *bridgeListener) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func closeGuestNetwork(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}
