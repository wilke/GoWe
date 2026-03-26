package toolexec

import "testing"

func TestResolveApptainerImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		imageDir string
		want     string
	}{
		{
			name:  "registry image gets docker prefix",
			image: "dxkb/esmfold:latest",
			want:  "docker://dxkb/esmfold:latest",
		},
		{
			name:  "registry image with no tag",
			image: "ubuntu",
			want:  "docker://ubuntu",
		},
		{
			name:  "absolute sif path used as-is",
			image: "/scout/containers/all.sif",
			want:  "/scout/containers/all.sif",
		},
		{
			name:     "absolute sif ignores imageDir",
			image:    "/scout/containers/all.sif",
			imageDir: "/other/dir",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif resolved against imageDir",
			image:    "all.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif with subdirectory",
			image:    "gpu/predict.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/gpu/predict.sif",
		},
		{
			name:  "relative sif without imageDir passed through",
			image: "all.sif",
			want:  "all.sif",
		},
		{
			name:     "registry image ignores imageDir",
			image:    "dxkb/esmfold:latest",
			imageDir: "/scout/containers",
			want:     "docker://dxkb/esmfold:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveApptainerImage(tt.image, tt.imageDir)
			if got != tt.want {
				t.Errorf("resolveApptainerImage(%q, %q) = %q, want %q", tt.image, tt.imageDir, got, tt.want)
			}
		})
	}
}
