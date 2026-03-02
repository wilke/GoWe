package staging

import (
	"testing"
)

func TestParseS3URI(t *testing.T) {
	tests := []struct {
		name       string
		uri        string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{
			name:       "simple",
			uri:        "s3://mybucket/mykey",
			wantBucket: "mybucket",
			wantKey:    "mykey",
		},
		{
			name:       "with path",
			uri:        "s3://mybucket/path/to/file.txt",
			wantBucket: "mybucket",
			wantKey:    "path/to/file.txt",
		},
		{
			name:       "with special chars",
			uri:        "s3://mybucket/path/file%20with%20spaces.txt",
			wantBucket: "mybucket",
			wantKey:    "path/file with spaces.txt",
		},
		{
			name:       "deep path",
			uri:        "s3://data-bucket/2024/01/15/experiment-results.json",
			wantBucket: "data-bucket",
			wantKey:    "2024/01/15/experiment-results.json",
		},
		{
			name:    "missing bucket",
			uri:     "s3:///key",
			wantErr: true,
		},
		{
			name:    "missing key",
			uri:     "s3://mybucket",
			wantErr: true,
		},
		{
			name:    "missing key with slash",
			uri:     "s3://mybucket/",
			wantErr: true,
		},
		{
			name:    "wrong scheme",
			uri:     "http://mybucket/key",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, key, err := parseS3URI(tt.uri)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
			}
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
		})
	}
}

func TestS3Stager_Supports(t *testing.T) {
	// Create a stager (won't actually connect to S3).
	stager := &S3Stager{}

	tests := []struct {
		scheme string
		want   bool
	}{
		{"s3", true},
		{"S3", false}, // Case-sensitive
		{"file", false},
		{"http", false},
		{"", false},
	}

	for _, tt := range tests {
		got := stager.Supports(tt.scheme)
		if got != tt.want {
			t.Errorf("Supports(%q) = %v, want %v", tt.scheme, got, tt.want)
		}
	}
}

func TestS3Config_Defaults(t *testing.T) {
	cfg := S3Config{}

	// NewS3Stager should set defaults.
	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("NewS3Stager failed: %v", err)
	}

	if stager.config.Region != "us-east-1" {
		t.Errorf("Region = %q, want %q", stager.config.Region, "us-east-1")
	}

	if stager.config.StageOutPrefix != "outputs/{taskID}/" {
		t.Errorf("StageOutPrefix = %q, want %q", stager.config.StageOutPrefix, "outputs/{taskID}/")
	}
}

func TestS3Config_CustomEndpoint(t *testing.T) {
	cfg := S3Config{
		Endpoint:        "minio.example.com:9000",
		AccessKeyID:     "minioadmin",
		SecretAccessKey: "minioadmin",
		UsePathStyle:    true,
		DisableSSL:      true,
	}

	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("NewS3Stager failed: %v", err)
	}

	if stager.client == nil {
		t.Error("client is nil")
	}
}

// Integration tests would go in s3_integration_test.go with build tag.
// They require MinIO testcontainer.
