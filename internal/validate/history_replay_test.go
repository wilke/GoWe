package validate

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	_ "modernc.org/sqlite"
)

// Replays every COMPLETED submission in gowe.db through the record checks, so
// the regression impact of a validation change is known BEFORE the binary is
// deployed. A payload that BV-BRC ran to completion is the strongest evidence
// available that a rejection is wrong.
func TestHistoryReplay_RequiredRecordFields(t *testing.T) {
	db := os.Getenv("GOWE_DB")
	if db == "" {
		t.Skip("set GOWE_DB to replay submission history")
	}
	conn, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Skipf("open: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query(`SELECT w.name, w.raw_cwl, s.inputs
		FROM submissions s JOIN workflows w ON w.id = s.workflow_id
		WHERE s.state = 'COMPLETED' AND COALESCE(w.raw_cwl,'') != ''`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	p := parser.New(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	var total, rejected int
	byReason := map[string]int{}

	for rows.Next() {
		var name, raw, inputsJSON string
		if err := rows.Scan(&name, &raw, &inputsJSON); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var inputs map[string]any
		if json.Unmarshal([]byte(inputsJSON), &inputs) != nil {
			continue
		}
		doc, err := p.ParseGraph([]byte(raw))
		if err != nil {
			continue
		}
		var tool *cwl.CommandLineTool
		for _, clt := range doc.Tools {
			tool = clt
			break
		}
		if tool == nil {
			continue
		}
		total++
		ApplyRecordFieldDefaults(tool, inputs)
		if err := ValidateRecordRequiredFields(tool, inputs); err != nil {
			rejected++
			byReason[name+": "+err.Error()]++
		}
	}
	t.Logf("replayed %d COMPLETED submissions through ValidateRecordRequiredFields", total)
	t.Logf("rejected: %d", rejected)
	for r, n := range byReason {
		t.Logf("   (%d) %s", n, r)
	}
	if rejected > 0 {
		t.Errorf("%d payload(s) that COMPLETED on BV-BRC would now be refused -- "+
			"the required-field rule is too strict", rejected)
	}
}
