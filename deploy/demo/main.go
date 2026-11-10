// Command demo pushes synthetic logs for a small fake system to Loki, so
// stromboli can be tried without a real stack. After DEMO_INCIDENT_AFTER it
// simulates a connection pool exhaustion in db-proxy that breaks api.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

func main() {
	lokiURL := env("LOKI_URL", "http://localhost:3100")
	after, err := time.ParseDuration(env("DEMO_INCIDENT_AFTER", "3m"))
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	log.Printf("pushing demo logs to %s; incident starts in %s", lokiURL, after)

	for range time.Tick(time.Second) {
		incident := time.Since(start) > after
		now := time.Now()
		var streams []stream
		add := func(service, level, line string) {
			ts := strconv.FormatInt(now.Add(time.Duration(rand.IntN(1000))*time.Millisecond).UnixNano(), 10)
			streams = append(streams, stream{
				Stream: map[string]string{"service_name": service, "level": level, "env": "demo"},
				Values: [][2]string{{ts, line}},
			})
		}

		for range 3 {
			add("web", "info", fmt.Sprintf(`level=info msg="GET /" status=200 duration=%dms`, 20+rand.IntN(40)))
		}
		for range 4 {
			status, dur := 200, 15+rand.IntN(30)
			if incident && rand.IntN(3) == 0 {
				status, dur = 500, 5000
				add("api", "error", fmt.Sprintf(`level=error msg="query failed" err="pq: sorry, too many clients already" request_id=%x`, rand.Uint64()))
			}
			add("api", "info", fmt.Sprintf(`level=info msg="GET /v1/monitors" status=%d duration=%dms`, status, dur))
		}
		if rand.IntN(10) == 0 {
			add("api", "warn", fmt.Sprintf(`level=warn msg="slow query" duration=%dms`, 300+rand.IntN(200)))
		}
		add("checker", "info", fmt.Sprintf(`level=info msg="check completed" region=ams latency=%dms`, 80+rand.IntN(100)))
		if incident {
			add("db-proxy", "error", fmt.Sprintf(`level=error msg="connection pool exhausted" active=100 max=100 waiting=%d`, 20+rand.IntN(40)))
		} else {
			add("db-proxy", "info", fmt.Sprintf(`level=info msg="pool stats" active=%d max=100`, 10+rand.IntN(15)))
		}

		if err := push(lokiURL, streams); err != nil {
			log.Printf("push: %v", err)
		}
	}
}

func push(base string, streams []stream) error {
	body, err := json.Marshal(map[string]any{"streams": streams})
	if err != nil {
		return err
	}
	resp, err := http.Post(base+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("loki returned %s", resp.Status)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
