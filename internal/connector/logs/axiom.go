package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// AxiomConfig configures an Axiom connector.
type AxiomConfig struct {
	// MCP is Axiom's MCP server, logged in with OAuth.
	MCP *mcpconn.Client
	// OrgID selects the organization when the login has several.
	OrgID string
	// MessageField is the field that holds the log line. When empty it is
	// detected per selector from common names (message, msg, body, ...).
	MessageField string
}

// Axiom queries Axiom with APL through Axiom's MCP server. A selector is the start of an APL query: a
// dataset, optionally followed by operators, e.g.
//
//	['logs'] | where ['service.name'] == "api"
type Axiom struct {
	cfg AxiomConfig
}

func NewAxiom(cfg AxiomConfig) (*Axiom, error) {
	if cfg.MCP == nil {
		return nil, fmt.Errorf("axiom: an MCP client is required")
	}
	return &Axiom{cfg: cfg}, nil
}

// axiomMessageFields are tried in order to find the field holding the log line.
var axiomMessageFields = []string{"message", "msg", "body", "log", "text", "_raw"}

// axiomServiceFields are tried in order when discovering services.
// fly.app.name is set by Fly's log shipper (flyio/log-shipper).
var axiomServiceFields = []string{"service.name", "resource.service.name", "service_name", "service", "app", "fly.app.name", "job", "container"}

// axiomLabelFields are copied into Line.Labels when present.
var axiomLabelFields = []string{"service.name", "service", "app", "fly.app.name", "fly.region", "level", "log.level", "severity", "severity_text", "host", "kubernetes.pod.name"}

func (a *Axiom) Query(ctx context.Context, q Query) ([]Line, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxLines {
		limit = maxLines
	}
	f, err := a.lineFields(ctx, q.Selector)
	if err != nil {
		return nil, err
	}
	apl := a.withFilter(q, f) + fmt.Sprintf(" | sort by _time desc | take %d", limit)
	res, err := a.query(ctx, apl, q.Start, q.End)
	if err != nil {
		return nil, err
	}
	var lines []Line
	for _, row := range res.rows() {
		l := Line{Time: parseAxiomTime(row["_time"]), Text: axiomText(row, f)}
		for _, f := range axiomLabelFields {
			if s, ok := row[f].(string); ok && s != "" {
				if l.Labels == nil {
					l.Labels = map[string]string{}
				}
				l.Labels[f] = s
			}
		}
		lines = append(lines, l)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.After(lines[j].Time) })
	return lines, nil
}

func (a *Axiom) Count(ctx context.Context, q Query) (int64, error) {
	var f axiomFields
	if q.Filter != "" {
		var err error
		if f, err = a.lineFields(ctx, q.Selector); err != nil {
			return 0, err
		}
	}
	res, err := a.query(ctx, a.withFilter(q, f)+" | summarize n = count()", q.Start, q.End)
	if err != nil {
		return 0, err
	}
	rows := res.rows()
	if len(rows) == 0 {
		return 0, nil
	}
	n, ok := rows[0]["n"].(float64)
	if !ok {
		return 0, fmt.Errorf("axiom: unexpected count value %v", rows[0]["n"])
	}
	return int64(n), nil
}

func (a *Axiom) Histogram(ctx context.Context, q Query, step time.Duration) ([]Bucket, error) {
	var f axiomFields
	if q.Filter != "" {
		var err error
		if f, err = a.lineFields(ctx, q.Selector); err != nil {
			return nil, err
		}
	}
	apl := fmt.Sprintf("%s | summarize n = count() by t = bin(_time, %ds)", a.withFilter(q, f), int64(step/time.Second))
	res, err := a.query(ctx, apl, q.Start, q.End)
	if err != nil {
		return nil, err
	}
	var out []Bucket
	for _, row := range res.rows() {
		n, _ := row["n"].(float64)
		if t := parseAxiomTime(row["t"]); !t.IsZero() {
			out = append(out, Bucket{Start: t, Count: int64(n)})
		}
	}
	return out, nil
}

// maxDiscoverDatasets bounds discovery on large organizations.
const maxDiscoverDatasets = 50

// Services proposes one service per value of a service field (service.name,
// app, ...) in each log dataset, or one per dataset when there is none.
func (a *Axiom) Services(ctx context.Context) ([]Service, error) {
	datasets, err := a.mcpDatasets(ctx)
	if err != nil {
		return nil, err
	}
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	var out []Service
	n := 0
	for _, ds := range datasets {
		if strings.Contains(ds.Kind, "metrics") || strings.Contains(ds.Kind, "traces") {
			continue
		}
		if n++; n > maxDiscoverDatasets {
			break
		}
		sel := aplIdent(ds.Name)
		fields, err := a.fields(ctx, sel, start, end)
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			continue // no events in the last 24h
		}
		field := ""
		for _, f := range axiomServiceFields {
			if fields[f] {
				field = f
				break
			}
		}
		if field == "" {
			out = append(out, Service{Name: ds.Name, Selector: sel})
			continue
		}
		ref := aplIdent(field)
		res, err := a.query(ctx, fmt.Sprintf("%s | where isnotempty(%s) | summarize count() by %s | take 500", sel, ref, ref), start, end)
		if err != nil {
			return nil, err
		}
		for _, row := range res.rows() {
			v, ok := row[field].(string)
			if !ok || v == "" {
				continue
			}
			out = append(out, Service{Name: v, Selector: fmt.Sprintf("%s | where %s == %s", sel, ref, strconv.Quote(v))})
		}
	}
	return out, nil
}

func (a *Axiom) Check(ctx context.Context) error {
	_, err := a.mcpDatasets(ctx)
	return err
}

type axiomDataset struct {
	Name string
	Kind string
}

// aplDatasetSpan returns the dataset an APL query starts with and the offset
// just after it, or "" and -1.
func aplDatasetSpan(apl string) (string, int) {
	off := len(apl) - len(strings.TrimLeft(apl, " \t\r\n"))
	s := apl[off:]
	if len(s) > 2 && s[0] == '[' && (s[1] == '\'' || s[1] == '"') {
		quote := s[1]
		var b strings.Builder
		for i := 2; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' && i+1 < len(s):
				i++
				b.WriteByte(s[i])
			case c == quote:
				if i+1 < len(s) && s[i+1] == ']' {
					return b.String(), off + i + 2
				}
				return "", -1
			default:
				b.WriteByte(c)
			}
		}
		return "", -1
	}
	end := strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.'
	})
	if end < 0 {
		end = len(s)
	}
	if end == 0 {
		return "", -1
	}
	return s[:end], off + end
}

// withFilter appends the regex filter. It matches "level=<level> <message>",
// the same text Query returns, so level-only errors (e.g. Fly's log.level)
// are caught by error filters.
func (a *Axiom) withFilter(q Query, f axiomFields) string {
	if q.Filter == "" {
		return q.Selector
	}
	target := fmt.Sprintf("tostring(%s)", aplIdent(f.msg))
	if f.level != "" {
		target = fmt.Sprintf(`strcat("level=", tostring(%s), " ", %s)`, aplIdent(f.level), target)
	}
	return fmt.Sprintf("%s | where %s matches regex %s", q.Selector, target, strconv.Quote(q.Filter))
}

// aplIdent quotes a dataset or field name: ['service.name'].
func aplIdent(name string) string {
	return "['" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name) + "']"
}

// axiomFields are the fields holding a selector's log line and level.
type axiomFields struct {
	msg, level string
}

// fieldCache remembers the detected fields per selector; connectors are
// rebuilt per request, so this lives at package level.
var fieldCache sync.Map // key -> cachedFields

type cachedFields struct {
	axiomFields
	until time.Time
}

// axiomLevelFields are tried in order to find the log level. log.level is set
// by Fly's log shipper.
var axiomLevelFields = []string{"level", "log.level", "severity_text", "severity"}

func (a *Axiom) lineFields(ctx context.Context, selector string) (axiomFields, error) {
	key := a.cfg.OrgID + "\x00" + a.cfg.MessageField + "\x00" + selector
	if c, ok := fieldCache.Load(key); ok && time.Now().Before(c.(cachedFields).until) {
		return c.(cachedFields).axiomFields, nil
	}
	end := time.Now()
	fields, err := a.fields(ctx, selector, end.Add(-7*24*time.Hour), end)
	if err != nil {
		return axiomFields{}, err
	}
	out := axiomFields{msg: a.cfg.MessageField}
	if len(fields) == 0 {
		// No data to detect from; don't cache.
		if out.msg == "" {
			out.msg = axiomMessageFields[0]
		}
		return out, nil
	}
	for _, f := range axiomMessageFields {
		if out.msg == "" && fields[f] {
			out.msg = f
		}
	}
	if out.msg == "" {
		return out, fmt.Errorf("axiom: no message field found for %s (tried %s); set message_field on the connector",
			selector, strings.Join(axiomMessageFields, ", "))
	}
	for _, f := range axiomLevelFields {
		if out.level == "" && fields[f] && f != out.msg {
			out.level = f
		}
	}
	fieldCache.Store(key, cachedFields{out, time.Now().Add(10 * time.Minute)})
	return out, nil
}

// fields returns the field names of the selector's events, empty when there
// are no events in the window.
func (a *Axiom) fields(ctx context.Context, selector string, start, end time.Time) (map[string]bool, error) {
	res, err := a.query(ctx, selector+" | take 1", start, end)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range res.Tables {
		if len(t.Columns) == 0 || len(t.Columns[0]) == 0 {
			continue
		}
		for _, f := range t.Fields {
			out[f.Name] = true
		}
	}
	return out, nil
}

// axiomResult holds tables in column-major order.
type axiomResult struct {
	Tables []struct {
		Fields []struct {
			Name string
		}
		Columns [][]any
	}
}

// rows converts the first (column-major) table into rows keyed by field name.
func (r *axiomResult) rows() []map[string]any {
	if len(r.Tables) == 0 {
		return nil
	}
	t := r.Tables[0]
	n := 0
	if len(t.Columns) > 0 {
		n = len(t.Columns[0])
	}
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = make(map[string]any, len(t.Fields))
		for j, f := range t.Fields {
			if j < len(t.Columns) && i < len(t.Columns[j]) {
				rows[i][f.Name] = t.Columns[j][i]
			}
		}
	}
	return rows
}

func (a *Axiom) query(ctx context.Context, apl string, start, end time.Time) (*axiomResult, error) {
	return a.mcpQuery(ctx, apl, start, end)
}

func parseAxiomTime(v any) time.Time {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// axiomText returns "level=<level> <message>", or the whole event as JSON when
// the message is empty.
func axiomText(row map[string]any, f axiomFields) string {
	var text string
	switch v := row[f.msg].(type) {
	case string:
		text = messageText(v)
	case nil:
	default:
		if b, err := json.Marshal(v); err == nil {
			text = string(b)
		}
	}
	if text == "" {
		ev := make(map[string]any, len(row))
		for k, v := range row {
			if v != nil && !strings.HasPrefix(k, "_") {
				ev[k] = v
			}
		}
		b, _ := json.Marshal(ev)
		return string(b)
	}
	if lvl, ok := row[f.level].(string); ok && lvl != "" && !strings.HasPrefix(text, "level=") &&
		!strings.Contains(strings.ToLower(text), strings.ToLower(lvl)) {
		text = "level=" + lvl + " " + text
	}
	return text
}
