package logs

import (
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// AxiomMCPURL is Axiom's remote MCP server (US). It requires OAuth; the EU
// server is https://mcp.eu.axiom.co/mcp.
const AxiomMCPURL = "https://mcp.axiom.co/mcp"

// AxiomMCPTools are the Axiom MCP tools stromboli may call.
var AxiomMCPTools = []string{"listDatasets", "queryDataset"}

// AxiomMCP returns the MCP config of an axiom connector; token returns the current OAuth access token.
func AxiomMCP(endpoint string, token func(context.Context) (string, error)) mcpconn.Config {
	return mcpconn.Config{
		Name:            "axiom",
		Endpoint:        cmp.Or(endpoint, AxiomMCPURL),
		Token:           token,
		Allow:           AxiomMCPTools,
		RequireReadOnly: true,
		// queryDataset is not marked read-only because saveResults stores a
		// result snapshot; stromboli always sends saveResults: false.
		ReadOnlyUse: []string{"queryDataset"},
	}
}

func (a *Axiom) mcpArgs(args map[string]any) map[string]any {
	if a.cfg.OrgID != "" {
		args["orgId"] = a.cfg.OrgID
	}
	return args
}

// mcpQuery runs APL through queryDataset. The time range is also applied in
// the query: Axiom widens it to whole bins for bin() summaries.
func (a *Axiom) mcpQuery(ctx context.Context, apl string, start, end time.Time) (*axiomResult, error) {
	args := map[string]any{"saveResults": false, "truncate": false}
	var conds []string
	if !start.IsZero() {
		args["startTime"] = start.UTC().Format(time.RFC3339Nano)
		conds = append(conds, fmt.Sprintf("_time >= datetime(%s)", args["startTime"]))
	}
	if !end.IsZero() {
		args["endTime"] = end.UTC().Format(time.RFC3339Nano)
		conds = append(conds, fmt.Sprintf("_time <= datetime(%s)", args["endTime"]))
	}
	if len(conds) > 0 {
		_, at := aplDatasetSpan(apl)
		if at < 0 {
			return nil, fmt.Errorf("axiom: the query must start with a dataset: %s", apl)
		}
		apl = apl[:at] + " | where " + strings.Join(conds, " and ") + apl[at:]
	}
	args["apl"] = apl
	var text string
	if err := a.cfg.MCP.Call(ctx, "queryDataset", a.mcpArgs(args), &text); err != nil {
		return nil, err
	}
	return parseAxiomMCPResult(text)
}

func (a *Axiom) mcpDatasets(ctx context.Context) ([]axiomDataset, error) {
	var text string
	if err := a.cfg.MCP.Call(ctx, "listDatasets", a.mcpArgs(map[string]any{"truncate": false}), &text); err != nil {
		return nil, err
	}
	res, err := parseAxiomMCPResult(text)
	if err != nil {
		return nil, err
	}
	var out []axiomDataset
	for _, row := range res.rows() {
		name, _ := row["datasetName"].(string)
		kind, _ := row["kind"].(string)
		if name != "" {
			out = append(out, axiomDataset{Name: name, Kind: kind})
		}
	}
	return out, nil
}

// parseAxiomMCPResult reads the first CSV section of an Axiom MCP answer:
//
//	# Query results
//
//	## results.csv (or # datasets.csv)
//
//	Effective range (_time): ... to ....
//
//	_time,body,severity_text
//	2026-11-12T20:00:00Z,timeout,ERROR
//
// "No data: ..." in place of the CSV means no rows. Numbers become float64
// and empty cells nil.
func parseAxiomMCPResult(text string) (*axiomResult, error) {
	// The section starts after a heading such as "## results.csv" or
	// "# datasets.csv".
	var body string
	for rest := text; rest != ""; {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		if strings.HasPrefix(line, "#") && strings.HasSuffix(strings.TrimSpace(line), ".csv") {
			body = rest
			break
		}
	}
	if body == "" {
		return nil, fmt.Errorf("axiom: unexpected MCP result: %.200s", text)
	}
	// Skip the blank and "Effective range" lines before the header.
	for {
		line, rest, _ := strings.Cut(body, "\n")
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "Effective range") {
			break
		}
		if rest == "" {
			return &axiomResult{}, nil
		}
		body = rest
	}
	if strings.HasPrefix(strings.TrimSpace(body), "No data") {
		return &axiomResult{}, nil
	}
	r := csv.NewReader(strings.NewReader(body))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("axiom: read MCP result header: %w", err)
	}
	res := &axiomResult{Tables: make([]struct {
		Fields []struct {
			Name string
		}
		Columns [][]any
	}, 1)}
	t := &res.Tables[0]
	for _, h := range header {
		t.Fields = append(t.Fields, struct {
			Name string
		}{h})
	}
	t.Columns = make([][]any, len(header))
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("axiom: read MCP result: %w", err)
		}
		// The section ends at the next heading or the "---" footer.
		if len(rec) != len(header) && (strings.HasPrefix(rec[0], "## ") || strings.TrimSpace(rec[0]) == "---") {
			break
		}
		for j := range header {
			var v any
			if j < len(rec) && rec[j] != "" {
				v = rec[j]
				if f, err := strconv.ParseFloat(rec[j], 64); err == nil {
					v = f
				}
			}
			t.Columns[j] = append(t.Columns[j], v)
		}
	}
	return res, nil
}
