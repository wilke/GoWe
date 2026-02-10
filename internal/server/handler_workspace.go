package server

import "net/http"

func (s *Server) handleListWorkspace(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/user@bvbrc/home/"
	}

	respondOK(w, reqID, map[string]any{
		"path": path,
		"objects": []map[string]any{
			{"name": "sample1_R1.fastq.gz", "type": "reads", "size": 1048576000, "created": "2026-02-01T10:00:00Z"},
			{"name": "sample1_R2.fastq.gz", "type": "reads", "size": 1073741824, "created": "2026-02-01T10:00:00Z"},
			{"name": "sample2/", "type": "folder", "size": 0, "created": "2026-02-05T14:30:00Z"},
		},
	})
}
