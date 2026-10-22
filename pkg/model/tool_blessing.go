package model

import (
	"strings"
	"time"
)

// DefaultTokenEnvNames are the environment variable names a blessed tool
// receives the user's token under when the blessing does not name others.
var DefaultTokenEnvNames = []string{"BVBRC_TOKEN", "KB_AUTH_TOKEN"}

// ScriptEntry is one piece of executable content found in a tool
// (InitialWorkDirRequirement entry, argument, shell command) that a reviewer
// must read before blessing.
type ScriptEntry struct {
	// Source says where the content was found, e.g. "InitialWorkDirRequirement:run.py".
	Source string `json:"source"`
	// Content is the verbatim text.
	Content string `json:"content"`
}

// ToolSummary is the human review aid stored with a blessing and returned by
// the inspect endpoint. It is derived from the tool document and is NOT part
// of the hash.
type ToolSummary struct {
	Class       string   `json:"class,omitempty"`
	Label       string   `json:"label,omitempty"`
	BaseCommand []string `json:"base_command,omitempty"`
	Arguments   []string `json:"arguments,omitempty"`
	ImageRef    string   `json:"image_ref,omitempty"`
	// RequirementClasses / HintClasses list the (sorted) classes present on the tool.
	RequirementClasses []string `json:"requirement_classes,omitempty"`
	HintClasses        []string `json:"hint_classes,omitempty"`
	EnvVars            []string `json:"env_vars,omitempty"`
	NetworkAccess      bool     `json:"network_access,omitempty"`
	Inputs             []string `json:"inputs,omitempty"`
	Outputs            []string `json:"outputs,omitempty"`
	// Scripts holds every InitialWorkDirRequirement entry / shell script verbatim.
	Scripts []ScriptEntry `json:"scripts,omitempty"`
	// Flags are review warnings (declared stdout/stderr outputs, stream
	// redirection, verbose/debug flags, scripts printing env or headers,
	// input-derived executable content, NetworkAccess).
	Flags []string `json:"flags,omitempty"`
}

// ToolBlessing records an administrator's decision that a specific tool
// (identified by its content hash) may receive the submitting user's token.
// Blessings are never deleted: revocation sets RevokedAt. They do not expire.
type ToolBlessing struct {
	ID       string `json:"id"`
	ToolHash string `json:"tool_hash"`
	ToolName string `json:"tool_name"`
	// EnvNames are the env var names the token is injected under; empty means DefaultTokenEnvNames.
	EnvNames []string `json:"env_names"`
	// WorkerGroups optionally restricts which worker groups may run the tool; empty means any.
	WorkerGroups []string `json:"worker_groups"`
	// ImageRef is the image reference the tool launches (part of the hash).
	ImageRef string `json:"image_ref"`
	// ImageSHA256s are the allowed image digests (hex, no prefix), one per
	// host build of the same recipe. At least one is required.
	ImageSHA256s []string    `json:"image_sha256s"`
	BlessedBy    string      `json:"blessed_by"`
	BlessedAt    time.Time   `json:"blessed_at"`
	Note         string      `json:"note,omitempty"`
	Summary      ToolSummary `json:"summary"`

	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokedBy    string     `json:"revoked_by,omitempty"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
}

// Active reports whether the blessing has not been revoked.
func (b *ToolBlessing) Active() bool {
	return b != nil && b.RevokedAt == nil
}

// AllowsGroup reports whether a worker group may run the blessed tool.
// An empty WorkerGroups list means no extra restriction.
func (b *ToolBlessing) AllowsGroup(group string) bool {
	if b == nil {
		return false
	}
	if len(b.WorkerGroups) == 0 {
		return true
	}
	if group == "" {
		group = "default"
	}
	for _, g := range b.WorkerGroups {
		if g == group {
			return true
		}
	}
	return false
}

// NormalizeSHA256 lowercases a digest and strips an optional "sha256:" prefix.
func NormalizeSHA256(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(s, "sha256:")
}

// IsValidSHA256 reports whether s (after normalisation) is 64 lowercase hex chars.
func IsValidSHA256(s string) bool {
	s = NormalizeSHA256(s)
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// AllowsImage reports whether sha (hex, optional "sha256:" prefix) is one of
// the blessing's pinned image digests.
func (b *ToolBlessing) AllowsImage(sha string) bool {
	if b == nil {
		return false
	}
	sha = NormalizeSHA256(sha)
	if sha == "" {
		return false
	}
	for _, d := range b.ImageSHA256s {
		if NormalizeSHA256(d) == sha {
			return true
		}
	}
	return false
}

// EffectiveEnvNames returns EnvNames, or DefaultTokenEnvNames when empty.
func (b *ToolBlessing) EffectiveEnvNames() []string {
	if b == nil || len(b.EnvNames) == 0 {
		return append([]string(nil), DefaultTokenEnvNames...)
	}
	return append([]string(nil), b.EnvNames...)
}
