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
	replayCompleted(t, "ValidateRecordRequiredFields", ValidateRecordRequiredFields)
}

// Same guard for the plain-array check. Expected to refuse nothing: a scalar
// where an array is declared appears in 0 of 81 array-valued inputs across
// every COMPLETED submission, and in 15 of 32 across the failures.
func TestHistoryReplay_ArrayShape(t *testing.T) {
	replayCompleted(t, "ValidateArrayShape", ValidateArrayShape)
}

// Reports how many COMPLETED submissions would carry a declared-type warning.
// It does NOT assert zero, because a warning is not an error and the count
// legitimately moves as CWLs are corrected -- fixing GenomeAssembly's
// genome_size to string? removes 5 of them.
//
// This measurement is why the type check reports instead of rejecting. Run
// against a strict version before anything was wired in, it refused 22:
//
//	14  Metagenomic Read Mapping  srr_ids     passed as a list
//	 5  GenomeAssembly            genome_size "5M"
//	 2  RNASeq                    contrasts   []
//	 1  GenomeAnnotation          contigs     an object
//
// All ran to completion on BV-BRC. **If anyone promotes these warnings to a
// gate, add it to replayCompleted above and it must come back zero first.**
func TestHistoryReplay_InputTypeWarningCount(t *testing.T) {
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
	counts := map[string]int{}
	total, flagged := 0, 0
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
		if w := InputTypeWarnings(tool, inputs); len(w) > 0 {
			flagged++
			counts[name+": "+w[0]]++
		}
	}
	t.Logf("%d of %d COMPLETED submissions carry a declared-type warning", flagged, total)
	for k, n := range counts {
		t.Logf("   (%d) %s", n, k)
	}
}

func replayCompleted(t *testing.T, label string, check func(*cwl.CommandLineTool, map[string]any) error) {
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
		if err := check(tool, inputs); err != nil {
			rejected++
			byReason[name+": "+err.Error()]++
		}
	}
	t.Logf("replayed %d COMPLETED submissions through %s", total, label)
	t.Logf("rejected: %d", rejected)
	for r, n := range byReason {
		t.Logf("   (%d) %s", n, r)
	}
	if rejected > 0 {
		t.Errorf("%d payload(s) that COMPLETED on BV-BRC would now be refused by %s "+
			"-- the rule is too strict", rejected, label)
	}
}
