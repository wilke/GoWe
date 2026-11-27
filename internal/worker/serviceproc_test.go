package worker

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	// Fields 3.. of a real stat line; field 4 (ppid) 1234, 5 (pgrp) 5678,
	// 22 (starttime) 987654321.
	tail := " S 1234 5678 5678 0 -1 4194560 1000 0 0 0 10 5 0 0 20 0 3 0 987654321 123456789 2000 18446744073709551615"
	tests := []struct {
		name string
		line string
	}{
		{"plain comm", "4242 (python3)" + tail},
		{"comm with spaces", "4242 (my model server)" + tail},
		{"comm with parentheses and spaces", "4242 (a) b (c) ) x)" + tail},
		{"comm that looks like fields", "4242 (S 1 2 3 4 5)" + tail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := parseProcStat(tt.line + "\n")
			if err != nil {
				t.Fatalf("parseProcStat: %v", err)
			}
			if want := (procStat{ppid: 1234, pgrp: 5678, startTime: 987654321}); st != want {
				t.Errorf("got %+v, want %+v", st, want)
			}
		})
	}
	for _, bad := range []string{
		"",
		"4242 python3 S 1 2",
		"4242 (python3) S 1 2 3",
		"4242 (python3) S x 5678" + strings.Repeat(" 0", 20),
		// starttime (field 22) not a number
		"4242 (python3) S 1234 5678" + strings.Repeat(" 0", 16) + " notanumber" + strings.Repeat(" 0", 3),
	} {
		if _, err := parseProcStat(bad); err == nil {
			t.Errorf("parseProcStat(%q) succeeded", bad)
		}
	}
}

func TestParseListenInodes(t *testing.T) {
	// Port 20000 = 0x4E20, port 20001 = 0x4E21.
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:4E20 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:4E20 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222222 1 0000000000000000 100 0 0 10 0
   2: 0100007F:4E20 0100007F:9C40 01 00000000:00000000 00:00000000 00000000  1000        0 333333 1 0000000000000000 20 4 30 10 -1
   3: 00000000:4E21 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444444 1 0000000000000000 100 0 0 10 0
   4: 0100007F:4E20 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 0 1 0000000000000000 100 0 0 10 0
   5: garbage
`
	tcp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:4E20 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555555 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:4E20 00000000000000000000000001000000:B2C8 01 00000000:00000000 00:00000000 00000000  1000        0 666666 1 0000000000000000 20 4 30 10 -1
`
	tests := []struct {
		name string
		data string
		port int
		want []uint64
	}{
		{"tcp: listeners on any address, not connections, not inode 0", tcp, 20000, []uint64{111111, 222222}},
		{"tcp: other port", tcp, 20001, []uint64{444444}},
		{"tcp6", tcp6, 20000, []uint64{555555}},
		{"no listener", tcp, 20002, nil},
		{"header only", strings.SplitN(tcp, "\n", 2)[0], 20000, nil},
		{"no trailing newline", strings.TrimRight(tcp6, "\n"), 20000, []uint64{555555}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseListenInodes(tt.data, tt.port); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSocketInode(t *testing.T) {
	tests := []struct {
		link string
		ino  uint64
		ok   bool
	}{
		{"socket:[555555]", 555555, true},
		{"socket:[]", 0, false},
		{"socket:[12x]", 0, false},
		{"pipe:[555555]", 0, false},
		{"/dev/null", 0, false},
		{"anon_inode:[eventpoll]", 0, false},
	}
	for _, tt := range tests {
		ino, ok := socketInode(tt.link)
		if ino != tt.ino || ok != tt.ok {
			t.Errorf("socketInode(%q) = %d, %v; want %d, %v", tt.link, ino, ok, tt.ino, tt.ok)
		}
	}
}
