//go:build linux

package worker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// listenV6OnlyLoopback opens a listener on [::1]:port with IPV6_V6ONLY set,
// so it does not conflict with a listener on 127.0.0.1:port. It skips the
// test when that cannot be done on this host (no IPv6 loopback).
func listenV6OnlyLoopback(t *testing.T, port int) net.Listener {
	t.Helper()
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	l, err := lc.Listen(context.Background(), "tcp6", "[::1]:"+strconv.Itoa(port))
	if err != nil {
		t.Skipf("cannot listen on [::1]:%d with IPV6_V6ONLY on this host: %v", port, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// Item 1: every listener on the port must be the service's. The fake model
// listens on 127.0.0.1 only and answers the health check there; a second
// listener on the same port on [::1], held by this test process (not a
// descendant of the service), makes the service never ready (FAILED after 3
// checks) or, once ready, unhealthy.
func TestService_SecondListenerOnAnotherAddress(t *testing.T) {
	t.Run("before ready: never ready, FAILED", func(t *testing.T) {
		shrinkServiceTimers(t)
		stub := installFakeModelApptainer(t)
		fs := newFakeServer(t, regRespService, serviceTask("svc_v6", "loopback-only", nil))
		var sawReady atomic.Bool
		fs.setOnHeartbeat(func(body []byte) {
			for _, s := range heartbeatServices(body) {
				if s.State == model.ServiceStateReady {
					sawReady.Store(true)
				}
			}
		})
		logs := &syncBuffer{}
		w := serviceTestWorker(t, fs, 1, logs)
		// The [::1] listener exists before the reservation, so the
		// all-interfaces bind test would refuse the port.
		w.services.canBind = func(int) bool { return true }
		port := w.services.lo
		listenV6OnlyLoopback(t, port)
		run := startWorker(t, w)

		waitLong(t, "report", func() bool { _, ok := fs.result("svc_v6"); return ok })
		res, _ := fs.result("svc_v6")
		if want := fmt.Sprintf("another process holds port %d", port); res.State != model.TaskStateFailed || !strings.HasPrefix(res.Stderr, want) {
			t.Errorf("result = %s %q, want FAILED %q", res.State, res.Stderr, want)
		}
		if sawReady.Load() || strings.Contains(logs.String(), `msg="service ready"`) {
			t.Error("the service was reported ready while a second listener held its port")
		}
		if want := fmt.Sprintf("port %d also has a listener that is not this service", port); !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
		pids := stubPIDs(stub)
		if len(pids) != 1 || !groupGone(pids[0]) {
			t.Errorf("service process left running (pids %v)", pids)
		}
		_ = run.stop()
	})

	t.Run("after ready: unhealthy, not stopped", func(t *testing.T) {
		shrinkServiceTimers(t)
		installFakeModelApptainer(t)
		fs := newFakeServer(t, regRespService, serviceTask("svc_v6r", "loopback-only", nil))
		w := serviceTestWorker(t, fs, 1, &syncBuffer{})
		run := startWorker(t, w)
		waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_v6r"); return s == model.ServiceStateReady })
		_, port := registryState(w, "svc_v6r")
		listenV6OnlyLoopback(t, port)

		want := fmt.Sprintf("port %d also has a listener that is not this service", port)
		waitLong(t, "unhealthy with the foreign-listener detail", func() bool {
			var state, detail string
			w.services.update("svc_v6r", func(e *serviceEntry) { state, detail = e.state, e.detail })
			return state == model.ServiceStateUnhealthy && detail == want
		})
		if _, ok := fs.result("svc_v6r"); ok {
			t.Error("a service that was ready was stopped for a foreign listener; it should only be unhealthy")
		}
		_ = run.stop()
	})
}
