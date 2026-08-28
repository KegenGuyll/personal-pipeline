package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"text/template"
	"time"
)

// NotifyData is the context available to notification templates and the shape
// of the default JSON envelope.
type NotifyData struct {
	Event      string `json:"event"`
	Project    string `json:"project"`
	Image      string `json:"image"`
	Tag        string `json:"tag"`
	Sha        string `json:"sha"`
	Repo       string `json:"repo"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error"`
}

type notifier struct {
	urls        []string
	tpl         *template.Template
	contentType string
	client      *http.Client
}

func newNotifier(cfg *Config) *notifier {
	n := &notifier{
		urls:        cfg.NotifyWebhookURLs,
		contentType: cfg.NotifyContentType,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
	if cfg.NotifyTemplate != "" {
		if t, err := template.New("notify").Parse(cfg.NotifyTemplate); err == nil {
			n.tpl = t
		} else {
			logEvent("notify.template_error", map[string]any{"error": err.Error()})
		}
	}
	return n
}

// Send posts the notification to every configured URL and returns a
// human-readable per-URL status. Send failures are non-fatal to deploys.
func (n *notifier) Send(data NotifyData) []string {
	if len(n.urls) == 0 {
		return nil
	}
	body, err := n.render(data)
	if err != nil {
		return []string{"render:fail:" + err.Error()}
	}
	var out []string
	for _, u := range n.urls {
		if err := n.post(u, body); err != nil {
			out = append(out, u+":fail:"+err.Error())
		} else {
			out = append(out, u+":ok")
		}
	}
	return out
}

func (n *notifier) render(data NotifyData) ([]byte, error) {
	if n.tpl != nil {
		var b bytes.Buffer
		if err := n.tpl.Execute(&b, data); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	return json.Marshal(data)
}

func (n *notifier) post(url string, body []byte) error {
	ct := n.contentType
	if ct == "" {
		ct = "application/json"
	}
	resp, err := n.client.Post(url, ct, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
