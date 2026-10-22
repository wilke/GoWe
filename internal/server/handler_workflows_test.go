package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestComputeContentHash pins the dedup hash helper introduced by #201: the
// hash must depend on both the parser schema version and the CWL text, so a
// version bump alone (independent of content) is enough to miss dedup
// against pre-bump rows.
func TestComputeContentHash(t *testing.T) {
	t.Run("deterministic for same inputs", func(t *testing.T) {
		h1 := computeContentHash("1", "cwlVersion: v1.2\n")
		h2 := computeContentHash("1", "cwlVersion: v1.2\n")
		if h1 != h2 {
			t.Errorf("hash not deterministic: %q vs %q", h1, h2)
		}
	})

	t.Run("differs when cwl differs", func(t *testing.T) {
		h1 := computeContentHash("1", "a")
		h2 := computeContentHash("1", "b")
		if h1 == h2 {
			t.Error("expected different hashes for different CWL content")
		}
	})

	t.Run("differs when version differs, same content", func(t *testing.T) {
		cwl := "cwlVersion: v1.2\n$graph: []\n"
		h1 := computeContentHash("1", cwl)
		h2 := computeContentHash("2", cwl)
		if h1 == h2 {
			t.Error("expected different hashes for different parseSchemaVersion with identical CWL")
		}
	})
}

// TestCreateWorkflow_DedupFlag pins the response-shape contract added in
// #201: a dedup hit must return the same id AND set deduplicated:true, while
// a genuinely new registration returns deduplicated:false (or omitted/false
// via zero value) so `gowe register` can tell them apart.
func TestCreateWorkflow_DedupFlag(t *testing.T) {
	srv := testServer()
	cwlStr := loadPackedCWL(t)
	body, _ := json.Marshal(map[string]string{"name": "dedup-flag-wf", "cwl": cwlStr})

	w1, env1 := doPost(t, srv, "/api/v1/workflows/", string(body))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first POST: status=%d, want 201, body=%s", w1.Code, w1.Body.String())
	}
	var data1 map[string]any
	json.Unmarshal(env1.Data, &data1)
	if dedup, _ := data1["deduplicated"].(bool); dedup {
		t.Error("first registration: deduplicated = true, want false")
	}
	id1, _ := data1["id"].(string)

	w2, env2 := doPost(t, srv, "/api/v1/workflows/", string(body))
	if w2.Code != http.StatusOK {
		t.Fatalf("second POST: status=%d, want 200 (dedup), body=%s", w2.Code, w2.Body.String())
	}
	var data2 map[string]any
	json.Unmarshal(env2.Data, &data2)
	if dedup, _ := data2["deduplicated"].(bool); !dedup {
		t.Error("second registration: deduplicated = false, want true")
	}
	id2, _ := data2["id"].(string)
	if id1 != id2 {
		t.Errorf("dedup hit id = %s, want %s (same as first)", id2, id1)
	}
}

// TestCreateWorkflow_Force pins the --force escape hatch (#201): force:true
// must bypass the dedup lookup entirely and always create a new row, even
// when identical content was already registered.
func TestCreateWorkflow_Force(t *testing.T) {
	srv := testServer()
	cwlStr := loadPackedCWL(t)

	body1, _ := json.Marshal(map[string]string{"name": "force-wf", "cwl": cwlStr})
	w1, env1 := doPost(t, srv, "/api/v1/workflows/", string(body1))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first POST: status=%d, want 201, body=%s", w1.Code, w1.Body.String())
	}
	var data1 map[string]any
	json.Unmarshal(env1.Data, &data1)
	id1, _ := data1["id"].(string)

	// Same content, force:true — must create a new row, not dedup.
	body2, _ := json.Marshal(map[string]any{"name": "force-wf", "cwl": cwlStr, "force": true})
	w2, env2 := doPost(t, srv, "/api/v1/workflows/", string(body2))
	if w2.Code != http.StatusCreated {
		t.Fatalf("forced POST: status=%d, want 201 (force bypasses dedup), body=%s", w2.Code, w2.Body.String())
	}
	var data2 map[string]any
	json.Unmarshal(env2.Data, &data2)
	id2, _ := data2["id"].(string)
	if dedup, _ := data2["deduplicated"].(bool); dedup {
		t.Error("forced registration: deduplicated = true, want false")
	}
	if id1 == id2 {
		t.Errorf("forced registration got same id as original: %s", id1)
	}

	// Both rows should be listed.
	env := doGet(t, srv, "/api/v1/workflows/")
	if env.Pagination.Total != 2 {
		t.Errorf("total workflows = %d, want 2 (original + forced)", env.Pagination.Total)
	}
}

// TestUpdateWorkflow_CWL_CreatesNewRow pins the #201 retrieval-contract fix:
// PUT with a non-empty cwl field must publish a NEW row (new id) rather than
// mutating the row addressed by {id} in place. The original row must be
// left byte-for-byte untouched, so anything already pinned to its id (e.g. a
// RUNNING submission) keeps seeing the original definition.
func TestUpdateWorkflow_CWL_CreatesNewRow(t *testing.T) {
	srv, st := testServerWithStore()
	id := createTestWorkflow(t, srv)

	// RawCWL isn't exposed via the API (json:"-"), so pin it directly
	// through the store as well as the observable content_hash/steps
	// fields returned over HTTP.
	origFromStore, err := st.GetWorkflow(context.Background(), id)
	if err != nil || origFromStore == nil {
		t.Fatalf("get original workflow from store: %v", err)
	}
	origRawCWL := origFromStore.RawCWL

	origEnv := doGet(t, srv, "/api/v1/workflows/"+id)
	var orig map[string]any
	json.Unmarshal(origEnv.Data, &orig)
	origHash, _ := orig["content_hash"].(string)

	// A different, single-step CWL document.
	newCWL := `cwlVersion: v1.2
$graph:
  - id: echo
    class: CommandLineTool
    baseCommand: [echo]
    inputs:
      msg: { type: string, inputBinding: { position: 1 } }
    outputs:
      out: { type: stdout }
  - id: main
    class: Workflow
    inputs:
      message: string
    outputs:
      result:
        type: File
        outputSource: step1/out
    steps:
      step1:
        run: "#echo"
        in:
          msg: message
        out: [out]
`
	body, _ := json.Marshal(map[string]string{"cwl": newCWL})
	w, env := doPut(t, srv, "/api/v1/workflows/"+id, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT with cwl: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var updated map[string]any
	json.Unmarshal(env.Data, &updated)
	newID, _ := updated["id"].(string)
	if newID == "" {
		t.Fatal("updated workflow missing id")
	}
	if newID == id {
		t.Errorf("PUT with cwl returned same id %s, want a NEW id (BEHAVIOR CHANGE #201)", id)
	}
	newHash, _ := updated["content_hash"].(string)
	if newHash == "" || newHash == origHash {
		t.Errorf("new row content_hash = %q, want non-empty and different from original %q", newHash, origHash)
	}
	newSteps, _ := updated["steps"].([]any)
	if len(newSteps) != 1 {
		t.Errorf("new row steps = %d, want 1 (single-step CWL)", len(newSteps))
	}

	// Original row at the URL id must be unchanged.
	afterEnv := doGet(t, srv, "/api/v1/workflows/"+id)
	var after map[string]any
	json.Unmarshal(afterEnv.Data, &after)
	afterHash, _ := after["content_hash"].(string)
	if afterHash != origHash {
		t.Errorf("original row content_hash changed: %q -> %q, want unchanged", origHash, afterHash)
	}
	afterSteps, _ := after["steps"].([]any)
	if len(afterSteps) != 2 {
		t.Errorf("original row steps = %d, want 2 (unchanged pipeline-packed.cwl)", len(afterSteps))
	}
	afterFromStore, err := st.GetWorkflow(context.Background(), id)
	if err != nil || afterFromStore == nil {
		t.Fatalf("get original workflow from store after PUT: %v", err)
	}
	if afterFromStore.RawCWL != origRawCWL {
		t.Error("original row's RawCWL changed after PUT with cwl — original row must be immutable (#201)")
	}

	// Both rows should now be listed.
	listEnv := doGet(t, srv, "/api/v1/workflows/")
	if listEnv.Pagination.Total != 2 {
		t.Errorf("total workflows = %d, want 2 (original + new version)", listEnv.Pagination.Total)
	}
}

// TestUpdateWorkflow_MetadataOnly_UpdatesInPlace pins the other half of the
// #201 contract: a PUT with no cwl field (name/description/labels only)
// keeps updating the existing row in place — same id, RawCWL/content_hash
// untouched.
func TestUpdateWorkflow_MetadataOnly_UpdatesInPlace(t *testing.T) {
	srv := testServer()
	id := createTestWorkflow(t, srv)

	origEnv := doGet(t, srv, "/api/v1/workflows/"+id)
	var orig map[string]any
	json.Unmarshal(origEnv.Data, &orig)
	origHash, _ := orig["content_hash"].(string)
	origSteps, _ := orig["steps"].([]any)

	body, _ := json.Marshal(map[string]any{
		"description": "updated via metadata-only PUT",
		"labels":      map[string]string{"env": "test"},
	})
	w, env := doPut(t, srv, "/api/v1/workflows/"+id, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT metadata-only: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var updated map[string]any
	json.Unmarshal(env.Data, &updated)

	if got, _ := updated["id"].(string); got != id {
		t.Errorf("metadata-only PUT id = %q, want unchanged %q", got, id)
	}
	if got, _ := updated["description"].(string); got != "updated via metadata-only PUT" {
		t.Errorf("description = %q, want updated", got)
	}
	if got, _ := updated["content_hash"].(string); got != origHash {
		t.Errorf("content_hash changed on metadata-only PUT: %q -> %q, want unchanged", origHash, got)
	}
	newSteps, _ := updated["steps"].([]any)
	if len(newSteps) != len(origSteps) {
		t.Errorf("steps count changed on metadata-only PUT: %d -> %d", len(origSteps), len(newSteps))
	}

	// Only one workflow should exist — no new row was created.
	listEnv := doGet(t, srv, "/api/v1/workflows/")
	if listEnv.Pagination.Total != 1 {
		t.Errorf("total workflows = %d, want 1 (metadata-only PUT must not create a new row)", listEnv.Pagination.Total)
	}
}
