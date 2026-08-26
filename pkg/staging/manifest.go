package staging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// OutputManifestName is the file written next to a submission's delivered
// outputs describing them.
const OutputManifestName = "_gowe_outputs.json"

// UploadOutputManifest writes the submission's outputs as a JSON manifest
// (workflow output IDs, types, ws:// locations) into baseDest in the
// workspace, giving users a machine-readable record of what each delivered
// file is. Shared by the scheduler's post-stage and the admin re-delivery
// endpoint so both write the same document. Returns the manifest path.
func UploadOutputManifest(ctx context.Context, stager *WorkspaceStager, sub *model.Submission, baseDest string) (string, error) {
	manifest := map[string]any{
		"submission_id": sub.ID,
		"workflow_id":   sub.WorkflowID,
		"workflow_name": sub.WorkflowName,
		"state":         string(sub.State),
		"outputs":       sub.Outputs,
	}
	if sub.CompletedAt != nil {
		manifest["completed_at"] = sub.CompletedAt.Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}

	destPath := strings.TrimRight(baseDest, "/") + "/" + OutputManifestName
	if _, err := stager.UploadContent(ctx, destPath, string(data), StageOptions{}); err != nil {
		return "", fmt.Errorf("upload manifest: %w", err)
	}
	return destPath, nil
}
