package app

import (
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestGuestNetworkWaitsForUTMBridge simulates the first VM boot, when the
// bridge address isn't available as the controller prepares enrollment URLs.
func TestGuestNetworkWaitsForUTMBridge(t *testing.T) {
	const bridge = "192.168.64.1"
	for _, port := range []string{"19443", "0"} {
		t.Run(port, func(t *testing.T) {
			var ready atomic.Bool
			firstRetry := make(chan struct{}, 1)
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			reservation := &guestNetworkTestListener{address: "127.0.0.1:52341"}
			endpoint := &guestNetworkTestListener{address: net.JoinHostPort(bridge, "52341"), connection: server}
			address := net.JoinHostPort(bridge, port)
			want := address
			if port == "0" {
				want = endpoint.address
			}
			open := func(_, bind string) (net.Listener, error) {
				switch bind {
				case "127.0.0.1:0":
					if port == "0" {
						return reservation, nil
					}
				case want:
					if ready.Load() {
						return endpoint, nil
					}
					select {
					case firstRetry <- struct{}{}:
					default:
					}
					return nil, syscall.EADDRNOTAVAIL
				case address:
					return nil, syscall.EADDRNOTAVAIL
				}
				t.Errorf("unexpected bind address %q, want UTM bridge or loopback port reservation", bind)
				return nil, syscall.EINVAL
			}
			listener, err := listenGuestNetwork(address, open)
			if err != nil {
				t.Fatalf("listenGuestNetwork(%q) = %v, want a deferred listener", address, err)
			}
			defer listener.Close()
			if got := listener.Addr().String(); got != want {
				t.Errorf("Addr() = %q, want %q before the bridge appears", got, want)
			}
			select {
			case <-firstRetry:
			default:
			}
			result := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil && conn != server {
					err = errors.New("accepted connection did not come from the bridge listener")
				}
				result <- err
			}()
			select {
			case <-firstRetry:
			case <-time.After(3 * time.Second):
				t.Fatal("Accept did not try the bridge bind")
			}
			ready.Store(true)
			select {
			case err := <-result:
				if err != nil {
					t.Errorf("Accept after the bridge appeared = %v, want success", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Accept did not use the newly available bridge")
			}
			if port == "0" && !reservation.closed.Load() {
				t.Error("temporary port reservation remained open after the bridge bound")
			}
		})
	}
}

// TestGuestNetworkDoesNotHideBindErrors keeps unrelated missing addresses and
// conflicting ports as startup errors, and allows shutdown while waiting.
func TestGuestNetworkDoesNotHideBindErrors(t *testing.T) {
	for _, tc := range []struct {
		address string
		cause   error
	}{
		{"192.168.64.1:19443", syscall.EADDRINUSE},
		{"192.0.2.1:19443", syscall.EADDRNOTAVAIL},
	} {
		_, err := listenGuestNetwork(tc.address, func(string, string) (net.Listener, error) {
			return nil, tc.cause
		})
		if !errors.Is(err, tc.cause) {
			t.Errorf("listenGuestNetwork(%q) = %v, want %v", tc.address, err, tc.cause)
		}
	}
	unavailable := make(chan struct{}, 1)
	listener, err := listenGuestNetwork("192.168.64.1:19443", func(string, string) (net.Listener, error) {
		select {
		case unavailable <- struct{}{}:
		default:
		}
		return nil, syscall.EADDRNOTAVAIL
	})
	if err != nil {
		t.Fatal(err)
	}
	<-unavailable
	result := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		result <- err
	}()
	select {
	case <-unavailable:
	case <-time.After(3 * time.Second):
		t.Fatal("Accept did not try the bridge bind")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not release Accept while the bridge was unavailable")
	}
}

type guestNetworkTestListener struct {
	address    string
	connection net.Conn
	closed     atomic.Bool
}

func (l *guestNetworkTestListener) Accept() (net.Conn, error) { return l.connection, nil }
func (l *guestNetworkTestListener) Addr() net.Addr {
	addr, _ := net.ResolveTCPAddr("tcp", l.address)
	return addr
}
func (l *guestNetworkTestListener) Close() error {
	l.closed.Store(true)
	return nil
}
