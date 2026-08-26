package bvbrc

import "net/http"

// UploadHTTPClient exposes the client's Shock upload HTTP client to the
// package's external tests (the integration test drives the raw PUT protocol
// through the same client the library uses).
func UploadHTTPClient(c *Client) *http.Client { return c.uploadClient }
