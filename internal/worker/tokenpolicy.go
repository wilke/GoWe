package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/me/gowe/internal/validate"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Provider-token policy (worker side).
//
// The worker holds the submitter's token only to stage data in and out, and
// drops it when the task ends. It places the token in a tool's environment
// only when the scheduler marked the task as blessed (TokenBlessingID, set
// from a server-side blessing, never from the tool's own hints) AND the image
// the worker is about to run hashes to one of the blessed digests.

// taskToken returns the delegated staging token for a task, or "".
func taskToken(hints *model.RuntimeHints) string {
	if hints == nil || hints.StagerOverrides == nil || hints.StagerOverrides.HTTPCredential == nil {
		return ""
	}
	return hints.StagerOverrides.HTTPCredential.Token
}

// wantsTokenInjection reports whether the task should receive the token in the
// tool environment: the scheduler requested injection, attached a blessing id,
// and a credential is present. Image verification is a separate, later step.
// A request without a blessing id (old scheduler, group-level flag) is
// refused with a WARN and the task continues without the token.
func wantsTokenInjection(hints *model.RuntimeHints, logger *slog.Logger, taskID string) bool {
	if hints == nil || !hints.InjectBVBRCToken {
		return false
	}
	if hints.TokenBlessingID == "" {
		if logger != nil {
			logger.Warn("token injection requested without blessing; refusing", "task_id", taskID)
		}
		return false
	}
	return taskToken(hints) != ""
}

// injectBVBRCTokenEnv returns secretEnvVars with the submitter's token added
// under hints.TokenEnvNames (model.DefaultTokenEnvNames when empty) when, and
// only when, hints.InjectBVBRCToken is set, a blessing id is present and a
// token exists. It deliberately never reads the tool's own hints. The image
// digest check (verifyImageForToken) is the caller's responsibility and must
// have passed before this is applied. secretEnvVars is never mutated in place
// (it may be the worker's shared secrets map).
func injectBVBRCTokenEnv(secretEnvVars map[string]string, hints *model.RuntimeHints) map[string]string {
	if hints == nil || !hints.InjectBVBRCToken || hints.TokenBlessingID == "" {
		return secretEnvVars
	}
	token := taskToken(hints)
	if token == "" {
		return secretEnvVars
	}
	names := hints.TokenEnvNames
	if len(names) == 0 {
		names = model.DefaultTokenEnvNames
	}
	copied := make(map[string]string, len(secretEnvVars)+len(names))
	for k, v := range secretEnvVars {
		copied[k] = v
	}
	for _, n := range names {
		copied[n] = token
	}
	return copied
}

var sha256Ref = regexp.MustCompile(`@sha256:([0-9a-fA-F]{64})(?:$|[^0-9a-zA-Z])`)

// digestAllowed reports whether digest (hex) is in allowed, case-insensitively.
func digestAllowed(digest string, allowed []string) bool {
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(a), "sha256:"), digest) {
			return true
		}
	}
	return false
}

// hashFileSHA256 streams the file through sha256 and returns the hex digest.
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// digestCache remembers sha256 digests of image files keyed on (path, dev,
// ino, size, mtime, ctime), so a multi-GB image is hashed once per worker
// rather than once per task. Any replacement or in-place modification changes
// the key and forces a re-hash. A nil *digestCache hashes every time.
type digestCache struct {
	mu   sync.Mutex
	m    map[string]string
	hash func(string) (string, error) // defaults to hashFileSHA256
}

func (c *digestCache) digest(path string) (string, error) {
	hash := hashFileSHA256
	if c != nil && c.hash != nil {
		hash = c.hash
	}
	if c == nil {
		return hash(path)
	}
	key, ok := fileIdentity(path)
	if !ok {
		return hash(path)
	}
	key = path + "|" + key
	c.mu.Lock()
	d, hit := c.m[key]
	c.mu.Unlock()
	if hit {
		return d, nil
	}
	d, err := hash(path)
	if err != nil {
		return "", err
	}
	// Re-check identity after hashing: if the file changed while being read,
	// do not cache a digest that may describe neither version.
	if key2, ok2 := fileIdentity(path); !ok2 || path+"|"+key2 != key {
		return d, nil
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]string)
	}
	c.m[key] = d
	c.mu.Unlock()
	return d, nil
}

// verifyImageForToken checks that the image a token-bearing task is about to
// run matches a blessed digest. runtime is "docker" or "apptainer"; anything
// else (local execution, no image) cannot carry a token. It returns the exact
// image reference the caller must launch ("" for docker, which pulls the
// digest-pinned ref as given; the symlink-resolved, hashed path for local .sif
// files). Errors never contain the token.
func verifyImageForToken(runtime, imageRef, imageDir string, allowed []string, cache *digestCache) (string, error) {
	if runtime != "docker" && runtime != "apptainer" {
		return "", fmt.Errorf("token injection requires container execution with a blessed image (runtime %q, image %q)", runtime, imageRef)
	}
	if imageRef == "" {
		return "", fmt.Errorf("token injection requires a container image")
	}
	if len(allowed) == 0 {
		return "", fmt.Errorf("token injection refused: blessing lists no image digests (image %q)", imageRef)
	}
	expected := strings.Join(allowed, ",")

	isLocalSIF := runtime == "apptainer" && strings.HasSuffix(imageRef, ".sif")
	if !isLocalSIF {
		// Registry reference: must be pinned by digest, and the digest blessed.
		m := sha256Ref.FindStringSubmatch(imageRef + " ")
		if m == nil {
			return "", fmt.Errorf("token injection refused: image %q is not pinned by @sha256:<digest>; expected one of [%s]", imageRef, expected)
		}
		if !digestAllowed(m[1], allowed) {
			return "", fmt.Errorf("token injection refused: image %q digest %s is not blessed; expected one of [%s]", imageRef, strings.ToLower(m[1]), expected)
		}
		if runtime == "apptainer" {
			return toolexec.ResolveApptainerImage(imageRef, imageDir), nil
		}
		return "", nil
	}

	resolved := toolexec.ResolveApptainerImage(imageRef, imageDir)
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("token injection refused: resolve image %q: %w", resolved, err)
	}
	digest, err := cache.digest(real)
	if err != nil {
		return "", fmt.Errorf("token injection refused: hash image %q: %w", real, err)
	}
	if !digestAllowed(digest, allowed) {
		return "", fmt.Errorf("token injection refused: image %q has sha256 %s, expected one of [%s]", real, digest, expected)
	}
	return real, nil
}

// prepareTokenImage runs the image check for a task that will receive the
// token and records the verified image on cfg so execution launches the very
// path that was hashed. It also pins cfg.ContainerRuntime to the runtime that
// was checked.
func prepareTokenImage(cfg *cwltool.Config, tool *cwl.CommandLineTool, hints *model.RuntimeHints, cache *digestCache) error {
	rt := ""
	if !cfg.NoContainer && cwltool.HasDockerRequirement(tool) {
		rt = cfg.ContainerRuntime
		if rt == "" {
			rt = cwltool.DetectContainerRuntime()
		}
	}
	resolved, err := verifyImageForToken(rt, cwltool.GetDockerImage(tool), cfg.ImageDir, hints.ImageSHA256s, cache)
	if err != nil {
		return err
	}
	cfg.ContainerRuntime = rt
	cfg.ResolvedImage = resolved
	return nil
}

// scrubTaskCredentials drops the task's credentials once the worker is done
// with it. Go strings cannot be zeroed; this removes the last references held
// through the task so the values become unreachable and collectable. It runs
// on every exit path (success, failure, cancellation, panic).
func scrubTaskCredentials(task *model.Task) {
	if task == nil || task.RuntimeHints == nil {
		return
	}
	task.RuntimeHints.ScrubSecrets()
	if so := task.RuntimeHints.StagerOverrides; so != nil &&
		so.HTTPCredential == nil && len(so.HTTPHeaders) == 0 && so.HTTPTimeoutSeconds == nil {
		task.RuntimeHints.StagerOverrides = nil
	}
}

// redactTokenInOutputs returns outputs with every occurrence of token inside
// any string value replaced by "***REDACTED***", and whether anything was
// replaced. outputs is not mutated.
func redactTokenInOutputs(outputs map[string]any, token string) (map[string]any, bool) {
	if token == "" || len(outputs) == 0 {
		return outputs, false
	}
	found := false
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			if strings.Contains(t, token) {
				found = true
				return strings.ReplaceAll(t, token, "***REDACTED***")
			}
			return t
		case map[string]any:
			m := make(map[string]any, len(t))
			for k, e := range t {
				if strings.Contains(k, token) {
					found = true
					k = strings.ReplaceAll(k, token, "***REDACTED***")
				}
				m[k] = walk(e)
			}
			return m
		case []any:
			s := make([]any, len(t))
			for i, e := range t {
				s[i] = walk(e)
			}
			return s
		case []map[string]any:
			s := make([]map[string]any, len(t))
			for i, e := range t {
				s[i], _ = walk(e).(map[string]any)
			}
			return s
		case []string:
			s := make([]any, len(t))
			for i, e := range t {
				s[i] = walk(e)
			}
			return s
		default:
			return v
		}
	}
	res, _ := walk(outputs).(map[string]any)
	if !found {
		return outputs, false
	}
	return res, true
}

// redactedError carries a redacted message while preserving the wrapped
// error's identity for errors.Is/As (e.g. validate.ErrInputValidation, which
// makes a failure permanent). Error() never contains the masked values.
type redactedError struct {
	msg     string
	wrapped error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.wrapped }

// newRedactedError returns err unchanged when masking changes nothing.
func newRedactedError(err error, mask map[string]string) error {
	if err == nil {
		return nil
	}
	msg := redactSecrets(err.Error(), mask)
	if msg == err.Error() {
		return err
	}
	// Wrap only the sentinel (like cwltool.maskedError), never the original
	// error: Unwrap must not expose the pre-redaction text.
	var wrapped error
	if errors.Is(err, validate.ErrInputValidation) {
		wrapped = validate.ErrInputValidation
	} else if errors.Is(err, validate.ErrOutputValidation) {
		wrapped = validate.ErrOutputValidation
	}
	return &redactedError{msg: msg, wrapped: wrapped}
}
