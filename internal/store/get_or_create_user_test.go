package store

import (
	"context"
	"sync"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// TestGetOrCreateUser_Concurrent exercises the first-login race: many
// concurrent requests for a username that does not exist yet must all
// succeed and agree on a single user row (#244).
func TestGetOrCreateUser_Concurrent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			u, err := st.GetOrCreateUser(ctx, "race@bvbrc", model.ProviderBVBRC)
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = u.ID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("divergent user IDs: %q vs %q", ids[0], ids[i])
		}
	}
}
