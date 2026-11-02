package logs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// CloudWatchConfig configures a CloudWatch Logs connector. Without static
// keys the default AWS credential chain is used (env, shared config, IAM role).
type CloudWatchConfig struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// Endpoint overrides the API endpoint (e.g. LocalStack).
	Endpoint string
	// LogGroupPrefix limits discovery to log groups with this prefix.
	LogGroupPrefix string
	// ServiceField, when set, makes discovery propose one service per value
	// of this field inside each log group (e.g. fly.app.name for Fly's log
	// shipper, which writes every app to one group) instead of one per group.
	ServiceField string
}

// CloudWatch queries CloudWatch Logs Insights. A selector is one or more log
// groups, comma separated, optionally followed by Insights commands:
//
//	/ecs/prod-api
//	/ecs/prod, /ecs/prod-worker | filter service = "api"
type CloudWatch struct {
	client       *cloudwatchlogs.Client
	poll         time.Duration
	groupPrefix  string
	serviceField string
}

var insightsFieldRe = regexp.MustCompile(`^[A-Za-z_@][A-Za-z0-9_.@-]*$`)

func NewCloudWatch(ctx context.Context, cfg CloudWatchConfig) (*CloudWatch, error) {
	if cfg.ServiceField != "" && !insightsFieldRe.MatchString(cfg.ServiceField) {
		return nil, fmt.Errorf("cloudwatch: invalid service_field %q", cfg.ServiceField)
	}
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
			return nil, errors.New("cloudwatch: access_key_id and secret_access_key must be set together")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch: %w", err)
	}
	if awsCfg.Region == "" {
		return nil, errors.New("cloudwatch: region is required")
	}
	client := cloudwatchlogs.NewFromConfig(awsCfg, func(o *cloudwatchlogs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return &CloudWatch{client: client, poll: 500 * time.Millisecond, groupPrefix: cfg.LogGroupPrefix, serviceField: cfg.ServiceField}, nil
}

// insightsTime is the format of @timestamp and bin() values, in UTC.
const insightsTime = "2006-01-02 15:04:05.000"

// maxLogGroups is the Insights limit per query.
const maxLogGroups = 50

// parseCloudWatchSelector splits "group1, group2 | commands".
func parseCloudWatchSelector(sel string) (groups []string, commands string, err error) {
	head, commands, _ := strings.Cut(sel, "|")
	for _, g := range strings.Split(head, ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return nil, "", fmt.Errorf("cloudwatch: selector %q names no log group", sel)
	}
	if len(groups) > maxLogGroups {
		return nil, "", fmt.Errorf("cloudwatch: selector names %d log groups, max %d", len(groups), maxLogGroups)
	}
	return groups, strings.TrimSpace(commands), nil
}

// insightsQuery joins Insights commands, adding the selector's own commands
// and the regex filter on @message.
func insightsQuery(first, commands, filter string, rest ...string) string {
	var parts []string
	if first != "" {
		parts = append(parts, first)
	}
	if commands != "" {
		parts = append(parts, commands)
	}
	if filter != "" {
		parts = append(parts, "filter @message like /"+strings.ReplaceAll(filter, "/", `\/`)+"/")
	}
	return strings.Join(append(parts, rest...), " | ")
}

func (c *CloudWatch) Query(ctx context.Context, q Query) ([]Line, error) {
	groups, commands, err := parseCloudWatchSelector(q.Selector)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > maxLines {
		limit = maxLines
	}
	qs := insightsQuery("fields @timestamp, @message, @logStream, @log", commands, q.Filter,
		"sort @timestamp desc", "limit "+strconv.Itoa(limit))
	rows, err := c.run(ctx, groups, qs, q.Start, q.End, limit)
	if err != nil {
		return nil, err
	}
	lines := make([]Line, 0, len(rows))
	for _, r := range rows {
		l := Line{Text: messageText(r["@message"]), Labels: map[string]string{}}
		if t, err := time.Parse(insightsTime, r["@timestamp"]); err == nil {
			l.Time = t.UTC()
		}
		if v := r["@log"]; v != "" {
			// "@log" is "account-id:log-group".
			_, group, _ := strings.Cut(v, ":")
			l.Labels["log_group"] = group
		}
		if v := r["@logStream"]; v != "" {
			l.Labels["log_stream"] = v
		}
		lines = append(lines, l)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.After(lines[j].Time) })
	return lines, nil
}

func (c *CloudWatch) Count(ctx context.Context, q Query) (int64, error) {
	groups, commands, err := parseCloudWatchSelector(q.Selector)
	if err != nil {
		return 0, err
	}
	rows, err := c.run(ctx, groups, insightsQuery("", commands, q.Filter, "stats count(*) as n"), q.Start, q.End, 1)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	n, err := strconv.ParseFloat(rows[0]["n"], 64)
	if err != nil {
		return 0, fmt.Errorf("cloudwatch: unexpected count value %q", rows[0]["n"])
	}
	return int64(n), nil
}

func (c *CloudWatch) Histogram(ctx context.Context, q Query, step time.Duration) ([]Bucket, error) {
	groups, commands, err := parseCloudWatchSelector(q.Selector)
	if err != nil {
		return nil, err
	}
	bin := fmt.Sprintf("bin(%ds)", int64(step/time.Second))
	rows, err := c.run(ctx, groups, insightsQuery("", commands, q.Filter, "stats count(*) as n by "+bin), q.Start, q.End, 1000)
	if err != nil {
		return nil, err
	}
	var out []Bucket
	for _, r := range rows {
		t, err := time.Parse(insightsTime, r[bin])
		if err != nil {
			continue
		}
		n, _ := strconv.ParseFloat(r["n"], 64)
		out = append(out, Bucket{Start: t.UTC(), Count: int64(n)})
	}
	return out, nil
}

// maxDiscoverGroups bounds discovery on large accounts.
const maxDiscoverGroups = 1000

// Services proposes one service per log group, named after the last path
// segment (/aws/lambda/checkout -> checkout). With a service field, groups
// are split into one service per value of that field instead.
func (c *CloudWatch) Services(ctx context.Context) ([]Service, error) {
	var names []string
	in := &cloudwatchlogs.DescribeLogGroupsInput{}
	if c.groupPrefix != "" {
		in.LogGroupNamePrefix = aws.String(c.groupPrefix)
	}
	p := cloudwatchlogs.NewDescribeLogGroupsPaginator(c.client, in)
	for p.HasMorePages() && len(names) < maxDiscoverGroups {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloudwatch: %w", err)
		}
		for _, g := range page.LogGroups {
			if g.LogGroupName != nil {
				names = append(names, *g.LogGroupName)
			}
		}
	}
	perGroup := serviceNamesForGroups(names)
	if c.serviceField == "" {
		return perGroup, nil
	}

	// One Insights query per 50 groups over the last 24h.
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	values := map[string][]string{} // group -> service field values
	qs := fmt.Sprintf("filter ispresent(%[1]s) | stats count(*) as n by @log, %[1]s", c.serviceField)
	for i := 0; i < len(names); i += maxLogGroups {
		rows, err := c.run(ctx, names[i:min(i+maxLogGroups, len(names))], qs, start, end, 10000)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			_, group, _ := strings.Cut(r["@log"], ":") // "account-id:log-group"
			if v := r[c.serviceField]; group != "" && v != "" {
				values[group] = append(values[group], v)
			}
		}
	}
	var out []Service
	seen := map[string]bool{}
	for _, g := range perGroup {
		vs := values[g.Selector]
		if len(vs) == 0 {
			if !seen[g.Name] {
				seen[g.Name] = true
				out = append(out, g)
			}
			continue
		}
		sort.Strings(vs)
		for _, v := range vs {
			name := strings.ToLower(v)
			if seen[name] {
				name += "-" + g.Name
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Service{Name: name, Selector: fmt.Sprintf("%s | filter %s = %s", g.Selector, c.serviceField, strconv.Quote(v))})
		}
	}
	return out, nil
}

// serviceNamesForGroups names each group after its last path segment, using
// more segments when two groups would get the same name.
func serviceNamesForGroups(groups []string) []Service {
	name := func(g string, n int) string {
		segs := strings.FieldsFunc(g, func(r rune) bool { return r == '/' })
		if n > len(segs) {
			n = len(segs)
		}
		return strings.ToLower(strings.Join(segs[len(segs)-n:], "-"))
	}
	count := map[string]int{}
	for _, g := range groups {
		count[name(g, 1)]++
	}
	out := make([]Service, 0, len(groups))
	seen := map[string]bool{}
	for _, g := range groups {
		n := name(g, 1)
		if count[n] > 1 {
			n = name(g, 1<<10)
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, Service{Name: n, Selector: g})
	}
	return out
}

func (c *CloudWatch) Check(ctx context.Context) error {
	_, err := c.client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("cloudwatch: %w", err)
	}
	return nil
}

// run starts an Insights query and polls until it finishes.
func (c *CloudWatch) run(ctx context.Context, groups []string, qs string, start, end time.Time, limit int) ([]map[string]string, error) {
	if !end.After(start) {
		end = start.Add(time.Second)
	}
	started, err := c.client.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupIdentifiers: groups,
		QueryString:         aws.String(qs),
		StartTime:           aws.Int64(start.Unix()),
		EndTime:             aws.Int64(end.Unix()),
		Limit:               aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("cloudwatch: start query: %w", err)
	}
	id := started.QueryId
	for {
		select {
		case <-ctx.Done():
			// Free the concurrent-query slot; the caller's context is gone.
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			c.client.StopQuery(stopCtx, &cloudwatchlogs.StopQueryInput{QueryId: id})
			cancel()
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
		res, err := c.client.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: id})
		if err != nil {
			return nil, fmt.Errorf("cloudwatch: get query results: %w", err)
		}
		switch res.Status {
		case types.QueryStatusComplete:
			rows := make([]map[string]string, 0, len(res.Results))
			for _, r := range res.Results {
				row := make(map[string]string, len(r))
				for _, f := range r {
					if f.Field != nil && f.Value != nil {
						row[*f.Field] = *f.Value
					}
				}
				rows = append(rows, row)
			}
			return rows, nil
		case types.QueryStatusFailed, types.QueryStatusCancelled, types.QueryStatusTimeout:
			return nil, fmt.Errorf("cloudwatch: query %s", strings.ToLower(string(res.Status)))
		}
	}
}
