// render_am_tmpl.go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"
)

/*
Loads helpers.tmpl (your Alertmanager template file), registers a few
Alertmanager-like funcs (toUpper, title, join), builds synthetic data,
and renders:
  - __discord_subject with the full context
  - __discord_alert_list with .Alerts
*/

type NameValue struct {
	Name  string
	Value string
}

type SortedPairs struct {
	Pairs  []NameValue
	Values []string
}

type KV map[string]string

type Alert struct {
	Labels       KV
	Annotations  KV
	GeneratorURL string
	DashboardURL string
	PanelURL     string
	Value        string
	StartsAt     time.Time
}

type AMData struct {
	Receiver string
	Status   string
	Alerts   []Alert

	GroupLabels       KV
	CommonLabels      KV
	CommonAnnotations KV
}

func toSortedPairs(m map[string]string) SortedPairs {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	pairs := make([]NameValue, 0, len(ks))
	values := make([]string, 0, len(ks))
	for _, k := range ks {
		pairs = append(pairs, NameValue{Name: k, Value: m[k]})
		values = append(values, m[k])
	}
	return SortedPairs{Pairs: pairs, Values: values}
}

func makeKV(m map[string]string) KV {
	return m
}

// ----- Template funcs -----

// toUpper: same semantics as Alertmanager's toUpper
func toUpper(s string) string { return strings.ToUpper(s) }

func tz(s string, t time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(s)
	if err != nil {
		return time.Time{}, err
	}
	return t.In(loc), nil
}

// title: simple Unicode-aware title-casing.
// - Lowercases everything first
// - Uppercases the first letter after start or a separator (space, -, _, /)
func title(s string) string {
	rs := []rune(strings.ToLower(s))
	shouldCap := true
	for i, r := range rs {
		if shouldCap && unicode.IsLetter(r) {
			rs[i] = unicode.ToUpper(r)
			shouldCap = false
			continue
		}
		switch r {
		case ' ', '-', '_', '/':
			shouldCap = true
		default:
			// keep going
		}
	}
	return string(rs)
}

// join: {{ list | join " " }}
func join(list []string, sep string) string { return strings.Join(list, sep) }

var funcMap = template.FuncMap{
	"toUpper": toUpper,
	"title":   title,
	"join":    join,
	"tz":      tz,
}

// helper to visualize whitespace
func showWS(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ':
			b.WriteRune('·') // space
		case '\t':
			b.WriteRune('→') // tab
		case '\r':
			b.WriteString("␍") // carriage return (rare)
		case '\n':
			b.WriteString("¦⏎\n") // end-of-line marker then newline
		default:
			b.WriteRune(r)
		}
	}
	// if the string doesn't end with newline, still show final EOL marker
	if !strings.HasSuffix(s, "\n") {
		b.WriteString("¦")
	}
	return b.String()
}

func main() {
	tmplPath := flag.String("tmpl", "helpers.tmpl", "path to helpers.tmpl")
	dumpJSON := flag.Bool("json", false, "dump synthetic data as JSON and exit")
	flag.Parse()

	data := makeSampleData()

	if *dumpJSON {
		b, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(b))
		return
	}

	// Load your helpers file verbatim
	tplBytes, err := os.ReadFile(*tmplPath)
	if err != nil {
		log.Fatalf("read %s: %v", *tmplPath, err)
	}

	// Wrap your named templates with small entry templates
	subjectWrapper := `{{ template "__discord_subject" . }}`
	messageWrapper := `{{ template "__discord_alert_list" . }}`

	helpers := template.Must(template.New("helpers.tmpl").Option("missingkey=zero").Funcs(funcMap).Parse(string(tplBytes)))
	subject := template.Must(template.Must(helpers.Clone()).Parse(subjectWrapper))
	message := template.Must(template.Must(helpers.Clone()).Parse(messageWrapper))

	fmt.Println("=== SUBJECT (visible whitespace) ===")
	{
		var buf bytes.Buffer
		if err := subject.Execute(&buf, data); err != nil {
			log.Fatalf("render subject: %v", err)
		}
		fmt.Println(showWS(buf.String()))
	}

	fmt.Println("\n=== MESSAGE (visible whitespace) ===")
	{
		var buf bytes.Buffer
		if err := message.Execute(&buf, data); err != nil {
			log.Fatalf("render message: %v", err)
		}
		fmt.Println(showWS(buf.String()))
	}
}

// ----- tweak this to mirror your real alerts -----
func makeSampleData() AMData {
	now := time.Now()

	groupLabels := map[string]string{
		"alertname": "HighLatency",
		"namespace": "payments",
		"severity":  "warning",
		"job":       "api",
	}
	commonLabels := map[string]string{
		"alertname": "HighLatency",
		"severity":  "warning",
	}

	a1 := Alert{
		Labels:       makeKV(map[string]string{"pod": "api-77c4bdb6f7-xw9d7", "instance": "10.0.0.12:8080", "region": "us-west-2"}),
		Annotations:  makeKV(map[string]string{"summary": "p99 latency > 2s", "description": "Gateway p99 above SLO", "runbook_url": "https://runbooks/msng.to/gw-latency"}),
		GeneratorURL: "https://prometheus/graph?expr=histogram_quantile(...)",
		DashboardURL: "https://grafana/d/latency",
		PanelURL:     "https://grafana/d/latency?viewPanel=12",
		Value:        "2.34",
		StartsAt:     now.Add(-7 * time.Minute),
	}

	a2 := Alert{
		Labels:       makeKV(map[string]string{"pod": "api-77c4bdb6f7-km4bx", "instance": "10.0.0.9:8080", "region": "us-west-2"}),
		Annotations:  makeKV(map[string]string{"summary": "p99 latency back to normal"}),
		GeneratorURL: "https://prometheus/graph?expr=histogram_quantile(...)",
		Value:        "0.42",
		StartsAt:     now.Add(-30 * time.Minute),
	}

	var data AMData
	data.Status = "firing"
	data.GroupLabels = groupLabels
	data.CommonLabels = commonLabels
	data.Alerts = []Alert{a1, a2}
	return data
}
