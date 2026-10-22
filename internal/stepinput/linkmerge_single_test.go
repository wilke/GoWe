package stepinput

import (
	"reflect"
	"testing"
)

func TestResolveInputsSingleSourceExplicitLinkMerge(t *testing.T) {
	file := map[string]any{"class": "File", "location": "whale.txt"}
	arr := []any{map[string]any{"class": "File", "location": "a"}, map[string]any{"class": "File", "location": "b"}}
	tests := []struct {
		name      string
		value     any
		linkMerge string
		want      any
	}{
		{"no linkMerge keeps value", file, "", file},
		{"merge_nested wraps scalar", file, "merge_nested", []any{file}},
		{"merge_nested wraps array", arr, "merge_nested", []any{arr}},
		{"merge_flattened keeps scalar as list", file, "merge_flattened", []any{file}},
		{"merge_flattened flattens array", arr, "merge_flattened", arr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputs := []InputDef{{ID: "file1", Sources: []string{"file1"}, LinkMerge: tt.linkMerge}}
			got, err := ResolveInputs(inputs, map[string]any{"file1": tt.value}, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["file1"], tt.want) {
				t.Fatalf("got %#v, want %#v", got["file1"], tt.want)
			}
		})
	}
}
