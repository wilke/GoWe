package worker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Health checks of a service (gowe:Service health), always on 127.0.0.1:
// the service runs on this host with the host network.

// serviceHealthTimeout bounds one health request. A variable so tests can
// shrink it.
var serviceHealthTimeout = 5 * time.Second

// serviceHealthBodyLimit is the most of a response body a check reads.
const serviceHealthBodyLimit = 1 << 20

// healthResult is the outcome of one check: ok, or why not. foreign marks a
// check whose HTTP part passed while a listener on the port did not belong to
// the service (another process holds the port, or answered); a check whose
// ownership could not be verified is failed but not foreign.
type healthResult struct {
	ok      bool
	foreign bool
	detail  string
}

// listenerVerifier decides whether the listeners on port belong to the
// process tree of rootPID: listenerOwnedBy, or a fixture in tests.
type listenerVerifier func(rootPID, port int) (listenerVerdict, error)

// serviceCheck runs one health check of the service whose process tree has
// root pid: the HTTP checks of checker and, when they pass, the check that
// every listener on port is held by that tree (verify, normally
// listenerOwnedBy). It passes only if both do. A listener not held by the
// tree (or no listener at all) makes it foreign; when ownership cannot be
// verified (non-Linux, /proc unreadable, a process of the tree whose
// descriptors cannot be read) it fails closed, without being foreign.
func serviceCheck(ctx context.Context, checker *healthChecker, verify listenerVerifier, pid, port int) healthResult {
	r := checker.check(ctx)
	if !r.ok {
		return r
	}
	v, err := verify(pid, port)
	switch {
	case err != nil:
		return healthResult{detail: fmt.Sprintf("cannot verify the listener of port %d: %v", port, err)}
	case v == listenerForeign:
		return healthResult{foreign: true, detail: fmt.Sprintf("port %d also has a listener that is not this service", port)}
	case v != listenerOwned:
		return healthResult{foreign: true, detail: fmt.Sprintf("port %d is answered by a process that is not this service", port)}
	}
	return r
}

// healthChecker runs the health check of one service: GET Path, which must
// answer 200, then (if set) GET ReadyPath, which must answer 200 and contain
// ReadyContains (if set).
type healthChecker struct {
	client *http.Client
	base   string
	spec   model.ServiceHealth
}

func newHealthChecker(scheme string, port int, spec model.ServiceHealth) *healthChecker {
	if scheme == "" {
		scheme = model.DefaultServiceScheme
	}
	transport := &http.Transport{
		// Never through a proxy: the service is on this host.
		Proxy: nil,
		// https: no certificate verification. The check connects only to
		// 127.0.0.1, the service's own loopback endpoint, whose certificate
		// names the public host if anything.
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback only, see above
		DisableKeepAlives: true,
	}
	return &healthChecker{
		client: &http.Client{
			Timeout:   serviceHealthTimeout,
			Transport: transport,
			// A redirect is an answer, not followed: it is not 200.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		base: scheme + "://127.0.0.1:" + strconv.Itoa(port),
		spec: spec,
	}
}

// check runs one health check.
func (c *healthChecker) check(ctx context.Context) healthResult {
	if _, err := c.get(ctx, c.spec.Path); err != nil {
		return healthResult{detail: err.Error()}
	}
	if c.spec.ReadyPath == "" {
		return healthResult{ok: true}
	}
	body, err := c.get(ctx, c.spec.ReadyPath)
	if err != nil {
		return healthResult{detail: err.Error()}
	}
	if c.spec.ReadyContains != "" && !strings.Contains(body, c.spec.ReadyContains) {
		return healthResult{detail: fmt.Sprintf("GET %s: response does not contain %q", c.spec.ReadyPath, c.spec.ReadyContains)}
	}
	return healthResult{ok: true}
}

// get requests path and returns the body (at most serviceHealthBodyLimit
// bytes) of a 200 answer; any other answer is an error.
func (c *healthChecker) get(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, serviceHealthBodyLimit))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	if err != nil {
		return "", fmt.Errorf("GET %s: read body: %w", path, err)
	}
	return string(body), nil
}

// healthTracker turns check results into the service state: starting until
// the first good check, then ready; failureThreshold consecutive failed
// checks after that make it unhealthy, the next good one ready again.
type healthTracker struct {
	threshold int
	state     string
	failures  int
	readyAt   *time.Time
}

func newHealthTracker(threshold int) *healthTracker {
	return &healthTracker{threshold: max(1, threshold), state: model.ServiceStateStarting}
}

// observe records one check and returns the state and whether it changed.
func (t *healthTracker) observe(ok bool, now time.Time) (state string, changed bool) {
	prev := t.state
	if ok {
		t.failures = 0
		t.state = model.ServiceStateReady
		if t.readyAt == nil {
			at := now.UTC()
			t.readyAt = &at
		}
	} else {
		t.failures++
		if t.state == model.ServiceStateReady && t.failures >= t.threshold {
			t.state = model.ServiceStateUnhealthy
		}
	}
	return t.state, t.state != prev
}

// everReady reports whether the service has been ready at least once.
func (t *healthTracker) everReady() bool {
	return t.readyAt != nil
}
