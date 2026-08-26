package bvbrc

import (
	"errors"
	"testing"
)

func TestError_Error(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{name: "wrapped, message is the error text", err: WrapError("Op", boom), want: "Op: boom"},
		{name: "wrapped, no message", err: &Error{Op: "Op", Err: boom}, want: "Op: boom"},
		{name: "wrapped with its own message", err: &Error{Op: "Op", Message: "context", Err: boom}, want: "Op: context: boom"},
		{name: "rpc code", err: FromRPCError("Op", &RPCError{Code: -32603, Message: "internal"}), want: "Op: [-32603] internal"},
		{name: "plain message", err: NewError("Op", "nothing to do"), want: "Op: nothing to do"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}

	if !errors.Is(WrapError("Op", boom), boom) {
		t.Error("WrapError must still unwrap to the underlying error")
	}
}
