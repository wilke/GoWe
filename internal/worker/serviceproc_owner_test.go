package worker

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// procFixture is an injected /proc for checkListenerOwnership.
type procFixture struct {
	tcp, tcp6 *string          // nil: the table cannot be read
	ppid      map[int]int      // pid -> ppid of every process
	fds       map[int][]string // pid -> /proc/<pid>/fd link targets
	fdErr     map[int]error    // pid -> error reading /proc/<pid>/fd
}

func (f procFixture) source() procSource {
	return procSource{
		netTable: func(name string) ([]byte, error) {
			t := f.tcp
			if name == "tcp6" {
				t = f.tcp6
			}
			if t == nil {
				return nil, &fs.PathError{Op: "open", Path: "/proc/net/" + name, Err: fs.ErrNotExist}
			}
			return []byte(*t), nil
		},
		pids: func() ([]int, error) {
			var out []int
			for pid := range f.ppid {
				out = append(out, pid)
			}
			sort.Ints(out)
			return out, nil
		},
		stat: func(pid int) (procStat, error) {
			pp, ok := f.ppid[pid]
			if !ok {
				return procStat{}, fs.ErrNotExist
			}
			return procStat{ppid: pp, pgrp: pid, startTime: 1}, nil
		},
		fdLinks: func(pid int) ([]string, error) {
			return f.fds[pid], f.fdErr[pid]
		},
	}
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// listenRow is a /proc/net/tcp{,6} row of a LISTEN socket on addr:port.
func listenRow(addr string, port int, inode uint64) string {
	zero := strings.Repeat("0", len(addr))
	return fmt.Sprintf("   0: %s:%04X %s:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 %d 1 0000000000000000 100 0 0 10 0\n",
		addr, port, zero, inode)
}

func ptr(s string) *string { return &s }

const (
	v4Any      = "00000000"
	v4Loopback = "0100007F"
	v6Loopback = "00000000000000000000000001000000"
)

func TestCheckListenerOwnership(t *testing.T) {
	const port = 20000
	// The service: root 100, its child 101, grandchild 102. Unrelated: 500.
	tree := map[int]int{100: 1, 101: 100, 102: 101, 500: 1}
	permDenied := &fs.PathError{Op: "open", Path: "/proc/100/fd", Err: fs.ErrPermission}
	tests := []struct {
		name    string
		fx      procFixture
		want    listenerVerdict
		wantErr string
	}{
		{
			name: "one listener, held by a descendant",
			fx: procFixture{
				tcp:  ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6: ptr(tcpHeader),
				fds:  map[int][]string{102: {"pipe:[9]", "socket:[111]"}},
			},
			want: listenerOwned,
		},
		{
			name: "one owned, one on tcp6 not held by the tree",
			fx: procFixture{
				tcp:  ptr(tcpHeader + listenRow(v4Loopback, port, 111)),
				tcp6: ptr(tcpHeader + listenRow(v6Loopback, port, 222)),
				fds:  map[int][]string{101: {"socket:[111]"}, 500: {"socket:[222]"}},
			},
			want: listenerForeign,
		},
		{
			name: "two listeners, both held by the tree",
			fx: procFixture{
				tcp:  ptr(tcpHeader + listenRow(v4Loopback, port, 111)),
				tcp6: ptr(tcpHeader + listenRow(v6Loopback, port, 222)),
				fds:  map[int][]string{100: {"socket:[222]"}, 101: {"socket:[111]"}},
			},
			want: listenerOwned,
		},
		{
			name: "no listener on the port",
			fx: procFixture{
				tcp:  ptr(tcpHeader + listenRow(v4Any, port+1, 111)),
				tcp6: ptr(tcpHeader),
				fds:  map[int][]string{100: {"socket:[111]"}},
			},
			want: listenerNone,
		},
		{
			name: "the only listener held by an unrelated process",
			fx: procFixture{
				tcp:  ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6: ptr(tcpHeader),
				fds:  map[int][]string{500: {"socket:[111]"}},
			},
			want: listenerForeign,
		},
		{
			name: "tcp6 missing (IPv6 disabled): tcp alone decides",
			fx: procFixture{
				tcp: ptr(tcpHeader + listenRow(v4Any, port, 111)),
				fds: map[int][]string{100: {"socket:[111]"}},
			},
			want: listenerOwned,
		},
		{
			name:    "no socket table readable",
			fx:      procFixture{},
			wantErr: "/proc/net/tcp6",
		},
		{
			name: "nothing owned and a process of the tree unreadable: cannot verify",
			fx: procFixture{
				tcp:   ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6:  ptr(tcpHeader),
				fdErr: map[int]error{100: permDenied},
			},
			wantErr: "cannot read /proc/100/fd: permission denied",
		},
		{
			name: "an unreadable process outside the tree changes nothing",
			fx: procFixture{
				tcp:   ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6:  ptr(tcpHeader),
				fdErr: map[int]error{500: permDenied},
			},
			want: listenerForeign,
		},
		{
			name: "a process of the tree gone meanwhile (not a permission error)",
			fx: procFixture{
				tcp:   ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6:  ptr(tcpHeader),
				fdErr: map[int]error{100: &fs.PathError{Op: "open", Path: "/proc/100/fd", Err: fs.ErrNotExist}},
			},
			want: listenerForeign,
		},
		{
			name: "unreadable root, all listeners held by a readable child",
			fx: procFixture{
				tcp:   ptr(tcpHeader + listenRow(v4Any, port, 111)),
				tcp6:  ptr(tcpHeader),
				fds:   map[int][]string{101: {"socket:[111]"}},
				fdErr: map[int]error{100: permDenied},
			},
			want: listenerOwned,
		},
		{
			name: "unreadable root, one listener owned and one not: foreign",
			fx: procFixture{
				tcp:   ptr(tcpHeader + listenRow(v4Loopback, port, 111)),
				tcp6:  ptr(tcpHeader + listenRow(v6Loopback, port, 222)),
				fds:   map[int][]string{101: {"socket:[111]"}},
				fdErr: map[int]error{100: permDenied},
			},
			want: listenerForeign,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fx.ppid = tree
			got, err := checkListenerOwnership(tt.fx.source(), 100, port)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, %v; want an error containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	if _, err := checkListenerOwnership(procFixture{}.source(), 0, port); err == nil {
		t.Error("no error without a process id")
	}
}

// serviceCheck: an unverifiable ownership is a failed check that is not
// foreign (it never counts towards the 3 checks that end a service FAILED);
// a listener not held by the service is foreign.
func TestServiceCheck_OwnershipOutcomes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { fmt.Fprintln(rw, "ok") }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	checker := newHealthChecker("http", port, model.ServiceHealth{Path: "/health"})

	tests := []struct {
		name        string
		verdict     listenerVerdict
		err         error
		wantOK      bool
		wantForeign bool
		wantDetail  string
	}{
		{"owned", listenerOwned, nil, true, false, ""},
		{"another listener", listenerForeign, nil, false, true, fmt.Sprintf("port %d also has a listener that is not this service", port)},
		{"no listener", listenerNone, nil, false, true, fmt.Sprintf("port %d is answered by a process that is not this service", port)},
		{"cannot verify", 0, fmt.Errorf("cannot read /proc/%d/fd: %w", 4242, fs.ErrPermission), false, false,
			fmt.Sprintf("cannot verify the listener of port %d: cannot read /proc/4242/fd: permission denied", port)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verify := func(int, int) (listenerVerdict, error) { return tt.verdict, tt.err }
			r := serviceCheck(context.Background(), checker, verify, 4242, port)
			if r.ok != tt.wantOK || r.foreign != tt.wantForeign || r.detail != tt.wantDetail {
				t.Errorf("got %+v, want ok %v foreign %v detail %q", r, tt.wantOK, tt.wantForeign, tt.wantDetail)
			}
		})
	}
}
