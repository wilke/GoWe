// Package metafixture is scanned (never built into the binary: testdata) by
// TestCredentialWriterScanner_Self. Each write in writes must be reported; the
// lookalike types must not be.
package metafixture

import "github.com/me/gowe/pkg/model"

type lookalikeHints struct {
	InjectBVBRCToken bool
	TokenBlessingID  string
}

type lookalikeOverrides struct {
	HTTPCredential *model.HTTPCredential
}

func writes(h *model.RuntimeHints, so *model.StagerOverrides) {
	h.InjectBVBRCToken = true                                 // assignment
	h.TokenBlessingID = "x"                                   // assignment
	so.HTTPCredential = &model.HTTPCredential{Type: "bearer"} // assignment
	_ = model.RuntimeHints{InjectBVBRCToken: true}            // composite literal
	_ = &model.StagerOverrides{HTTPCredential: nil}           // composite literal
	p := &h.InjectBVBRCToken                                  // address-of
	_ = p
}

func lookalikes(a *lookalikeHints, b *lookalikeOverrides) {
	a.InjectBVBRCToken = true
	a.TokenBlessingID = "y"
	b.HTTPCredential = nil
	_ = lookalikeHints{InjectBVBRCToken: true}
}
