package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// logEvent writes a single structured JSON line to stdout (captured by
// `docker logs`). It must never be used to log secrets.
func logEvent(event string, fields map[string]any) {
	m := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "event": event}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		fmt.Printf(`{"ts":%q,"event":%q,"log_error":%q}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), event, err.Error())
		return
	}
	fmt.Println(string(b))
}
