// Package fly is a client for the Fly.io Machines API
// (https://fly.io/docs/machines/api/): it lists an app's machines for the
// brief, and runs the few machine actions opensre can execute once a human
// approves them.
package fly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MachinesURL is the public Machines API endpoint.
const MachinesURL = "https://api.machines.dev"

// Authorization turns a Fly token into an Authorization header value. Tokens
// from `fly tokens create` start with "FlyV1 " (or are bare fm1/fm2 macaroons)
// and are sent as FlyV1; others (from `fly auth token`) are bearer tokens.
func Authorization(token string) string {
	token = strings.TrimSpace(token)
	switch {
	case strings.HasPrefix(token, "FlyV1 "), strings.HasPrefix(token, "Bearer "):
		return token
	case strings.HasPrefix(token, "fm1"), strings.HasPrefix(token, "fm2"):
		return "FlyV1 " + token
	}
	return "Bearer " + token
}

// Machines talks to the Machines API of one organisation.
type Machines struct {
	base string
	org  string
	auth string
	http *http.Client
}

// NewMachines returns a Machines API client. baseURL defaults to MachinesURL.
func NewMachines(org, token, baseURL string) (*Machines, error) {
	if token == "" {
		return nil, errors.New("fly: token is required")
	}
	if baseURL == "" {
		baseURL = MachinesURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("fly: invalid url: %w", err)
	}
	return &Machines{
		base: strings.TrimRight(baseURL, "/"),
		org:  org,
		auth: Authorization(token),
		http: &http.Client{Timeout: 90 * time.Second},
	}, nil
}

// Machine is the subset of a machine the brief and the actions use.
type Machine struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Region     string    `json:"region"`
	InstanceID string    `json:"instance_id"`
	ImageRef   ImageRef  `json:"image_ref"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Config is kept raw so a clone copies it exactly.
	Config json.RawMessage `json:"config"`
}

type ImageRef struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

// Image renders the image reference like `fly image show` does.
func (r ImageRef) Image() string {
	s := r.Repository
	if r.Registry != "" {
		s = r.Registry + "/" + s
	}
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// machineConfig is the part of a machine config the actions inspect.
type machineConfig struct {
	Mounts   []Mount           `json:"mounts"`
	Metadata map[string]string `json:"metadata"`
	Guest    struct {
		CPUKind  string `json:"cpu_kind"`
		CPUs     int    `json:"cpus"`
		MemoryMB int    `json:"memory_mb"`
	} `json:"guest"`
}

func (m Machine) config() machineConfig {
	var c machineConfig
	_ = json.Unmarshal(m.Config, &c)
	return c
}

// ProcessGroup is the fly.toml process group of the machine, if any.
func (m Machine) ProcessGroup() string { return m.config().Metadata["fly_process_group"] }

// Mount is a volume mounted by a machine.
type Mount struct {
	Volume string `json:"volume"`
	Path   string `json:"path"`
	SizeGB int    `json:"size_gb"`
}

// HasVolumes reports whether the machine mounts a volume.
func (m Machine) HasVolumes() bool { return len(m.config().Mounts) > 0 }

// Volumes returns the volumes the machine mounts.
func (m Machine) Volumes() []Mount { return m.config().Mounts }

// Size describes the machine's guest, e.g. "shared-cpu-1x, 512MB".
func (m Machine) Size() string {
	g := m.config().Guest
	if g.CPUs == 0 && g.MemoryMB == 0 {
		return ""
	}
	kind := g.CPUKind
	if kind == "" {
		kind = "shared"
	}
	return fmt.Sprintf("%s-cpu-%dx, %dMB", kind, max(g.CPUs, 1), g.MemoryMB)
}

// Check verifies the token by listing the organisation's apps.
func (c *Machines) Check(ctx context.Context) error {
	if c.org == "" {
		return errors.New("fly: org is required to test the connection")
	}
	return c.do(ctx, http.MethodGet, "/v1/apps?org_slug="+url.QueryEscape(c.org), nil, nil)
}

// List returns the app's machines.
func (c *Machines) List(ctx context.Context, app string) ([]Machine, error) {
	var out []Machine
	err := c.do(ctx, http.MethodGet, appPath(app, ""), nil, &out)
	return out, err
}

// Get returns one machine.
func (c *Machines) Get(ctx context.Context, app, id string) (Machine, error) {
	var m Machine
	err := c.do(ctx, http.MethodGet, appPath(app, id), nil, &m)
	return m, err
}

// Restart restarts a machine in place (same ID, image and config).
func (c *Machines) Restart(ctx context.Context, app, id string) error {
	return c.do(ctx, http.MethodPost, appPath(app, id)+"/restart", nil, nil)
}

// Stop stops a machine; Start starts it again.
func (c *Machines) Stop(ctx context.Context, app, id string) error {
	return c.do(ctx, http.MethodPost, appPath(app, id)+"/stop", nil, nil)
}

func (c *Machines) Start(ctx context.Context, app, id string) error {
	return c.do(ctx, http.MethodPost, appPath(app, id)+"/start", nil, nil)
}

// Cordon stops the Fly proxy from routing requests to a machine, which keeps
// running; Uncordon routes traffic to it again.
func (c *Machines) Cordon(ctx context.Context, app, id string) error {
	return c.do(ctx, http.MethodPost, appPath(app, id)+"/cordon", nil, nil)
}

func (c *Machines) Uncordon(ctx context.Context, app, id string) error {
	return c.do(ctx, http.MethodPost, appPath(app, id)+"/uncordon", nil, nil)
}

// ErrHasVolumes is returned when cloning a machine that mounts a volume: a
// clone would start with an empty volume, which is not a safe mitigation.
var ErrHasVolumes = errors.New("machine mounts a volume: a clone would start with an empty volume, clone it by hand with `fly machine clone` if that is what you want")

// Clone creates a new machine with the source machine's config, in region
// (the source's region when empty). It is what `fly machine clone` does.
func (c *Machines) Clone(ctx context.Context, app, id, region string) (Machine, error) {
	src, err := c.Get(ctx, app, id)
	if err != nil {
		return Machine{}, err
	}
	if src.HasVolumes() {
		return Machine{}, ErrHasVolumes
	}
	if region == "" {
		region = src.Region
	}
	body := map[string]any{"config": src.Config, "region": region}
	var out Machine
	err = c.do(ctx, http.MethodPost, appPath(app, ""), body, &out)
	return out, err
}

// WaitStarted waits up to timeout for a machine to reach the started state.
func (c *Machines) WaitStarted(ctx context.Context, app, id string, timeout time.Duration) error {
	q := url.Values{"state": {"started"}, "timeout": {fmt.Sprint(int(timeout.Seconds()))}}
	return c.do(ctx, http.MethodGet, appPath(app, id)+"/wait?"+q.Encode(), nil, nil)
}

func appPath(app, id string) string {
	p := "/v1/apps/" + url.PathEscape(app) + "/machines"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

// maxBody bounds a Machines API response.
const maxBody = 8 << 20

func (c *Machines) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fly machines: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("fly machines: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("fly machines: %s %s: %s: %s", method, strings.SplitN(path, "?", 2)[0], resp.Status, msg)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("fly machines: decode response: %w", err)
	}
	return nil
}
