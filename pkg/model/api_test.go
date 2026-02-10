package model

import "testing"

func TestListOptions_Clamp(t *testing.T) {
	tests := []struct {
		name      string
		input     ListOptions
		wantLimit int
		wantOffset int
	}{
		{"defaults", ListOptions{Limit: 0, Offset: 0}, 20, 0},
		{"negative limit", ListOptions{Limit: -5, Offset: 0}, 20, 0},
		{"over max", ListOptions{Limit: 200, Offset: 0}, 100, 0},
		{"negative offset", ListOptions{Limit: 10, Offset: -3}, 10, 0},
		{"valid", ListOptions{Limit: 50, Offset: 10}, 50, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.Clamp()
			if tt.input.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", tt.input.Limit, tt.wantLimit)
			}
			if tt.input.Offset != tt.wantOffset {
				t.Errorf("Offset = %d, want %d", tt.input.Offset, tt.wantOffset)
			}
		})
	}
}

func TestDefaultListOptions(t *testing.T) {
	opts := DefaultListOptions()
	if opts.Limit != 20 {
		t.Errorf("Limit = %d, want 20", opts.Limit)
	}
	if opts.Offset != 0 {
		t.Errorf("Offset = %d, want 0", opts.Offset)
	}
}
