package validate

import "testing"

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeWarn, false},
		{"warn", ModeWarn, false},
		{"enforce", ModeEnforce, false},
		{"off", ModeOff, false},
		{"strict", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMode(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("ParseMode(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
			}
		})
	}
	if Mode("").Effective() != ModeWarn {
		t.Fatal(`empty mode must be effective warn`)
	}
}
