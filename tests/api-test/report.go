package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Report holds the complete test run results.
type Report struct {
	Gateway   string        `json:"gateway"`
	Generated time.Time     `json:"generated"`
	Duration  time.Duration `json:"duration_ms"`
	Results   []TestResult  `json:"results"`
}

// WriteJSONReport writes the full report as JSON.
func WriteJSONReport(r Report, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// WriteMarkdownReport generates a human-readable markdown report.
func WriteMarkdownReport(r Report, path string) error {
	var b strings.Builder

	passed, failed, skipped := countResults(r.Results)
	total := len(r.Results)
	passRate := 0.0
	if total-skipped > 0 {
		passRate = float64(passed) / float64(total-skipped) * 100
	}

	b.WriteString("# Settla Tenant API Test Report\n\n")
	b.WriteString(fmt.Sprintf("Generated: %s | Gateway: %s | Duration: %s\n\n",
		r.Generated.Format(time.RFC3339), r.Gateway, r.Duration.Round(time.Millisecond)))

	// Summary table
	b.WriteString("## Summary\n\n")
	b.WriteString("| Metric  | Value |\n")
	b.WriteString("|---------|-------|\n")
	b.WriteString(fmt.Sprintf("| Total   | %d    |\n", total))
	b.WriteString(fmt.Sprintf("| Passed  | %d    |\n", passed))
	b.WriteString(fmt.Sprintf("| Failed  | %d    |\n", failed))
	b.WriteString(fmt.Sprintf("| Skipped | %d    |\n", skipped))
	b.WriteString(fmt.Sprintf("| Pass %%  | %.1f%% |\n", passRate))
	b.WriteString("\n")

	// Results by category
	categories := categorize(r.Results)
	b.WriteString("## Results by Category\n\n")
	for _, cat := range categories {
		cp, cf, cs := countResults(cat.results)
		b.WriteString(fmt.Sprintf("### %s (%d passed, %d failed, %d skipped)\n\n", cat.name, cp, cf, cs))
		b.WriteString("| Endpoint | Status | Expected | Result | Duration |\n")
		b.WriteString("|----------|--------|----------|--------|----------|\n")
		for _, tr := range cat.results {
			icon := "PASS"
			if tr.Skipped {
				icon = "SKIP"
			} else if !tr.Passed {
				icon = "FAIL"
			}
			b.WriteString(fmt.Sprintf("| %s %s | %d | %d | %s | %s |\n",
				tr.Method, tr.Path, tr.StatusCode, tr.ExpectedStatus, icon,
				tr.Duration.Round(time.Millisecond)))
		}
		b.WriteString("\n")
	}

	// Failed tests detail
	if failed > 0 {
		b.WriteString("## Failed Tests (Detail)\n\n")
		for _, tr := range r.Results {
			if tr.Passed || tr.Skipped {
				continue
			}
			b.WriteString(fmt.Sprintf("### %s %s\n\n", tr.Method, tr.Path))
			b.WriteString(fmt.Sprintf("- **Category:** %s\n", tr.Category))
			b.WriteString(fmt.Sprintf("- **Expected:** %d\n", tr.ExpectedStatus))
			b.WriteString(fmt.Sprintf("- **Got:** %d\n", tr.StatusCode))
			if tr.Error != "" {
				b.WriteString(fmt.Sprintf("- **Error:** %s\n", tr.Error))
			}
			if tr.RequestBody != nil {
				b.WriteString("\n**Request Body:**\n```json\n")
				writeJSON(&b, tr.RequestBody)
				b.WriteString("\n```\n")
			}
			if tr.ResponseBody != nil {
				b.WriteString("\n**Response Body:**\n```json\n")
				writeJSON(&b, tr.ResponseBody)
				b.WriteString("\n```\n")
			}
			b.WriteString("\n")
		}
	}

	// Full request/response dump
	b.WriteString("## All Requests & Responses\n\n")
	for i, tr := range r.Results {
		b.WriteString(fmt.Sprintf("<details>\n<summary>%d. %s %s — %d (%s)</summary>\n\n",
			i+1, tr.Method, tr.Path, tr.StatusCode, tr.Description))
		if tr.RequestBody != nil {
			b.WriteString("**Request:**\n```json\n")
			writeJSON(&b, tr.RequestBody)
			b.WriteString("\n```\n\n")
		}
		if tr.ResponseBody != nil {
			b.WriteString("**Response:**\n```json\n")
			writeJSON(&b, tr.ResponseBody)
			b.WriteString("\n```\n\n")
		}
		if tr.Skipped {
			b.WriteString(fmt.Sprintf("*Skipped: %s*\n\n", tr.SkipReason))
		}
		b.WriteString("</details>\n\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type categoryGroup struct {
	name    string
	results []TestResult
}

func categorize(results []TestResult) []categoryGroup {
	seen := map[string]int{}
	var groups []categoryGroup
	for _, r := range results {
		cat := r.Category
		if cat == "" {
			cat = "Uncategorized"
		}
		if idx, ok := seen[cat]; ok {
			groups[idx].results = append(groups[idx].results, r)
		} else {
			seen[cat] = len(groups)
			groups = append(groups, categoryGroup{name: cat, results: []TestResult{r}})
		}
	}
	return groups
}

func countResults(results []TestResult) (passed, failed, skipped int) {
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	return
}

func writeJSON(b *strings.Builder, v any) {
	switch val := v.(type) {
	case json.RawMessage:
		var buf bytes.Buffer
		if json.Indent(&buf, val, "", "  ") == nil {
			b.WriteString(buf.String())
		} else {
			b.Write(val)
		}
	default:
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			fmt.Fprintf(b, "%v", v)
		} else {
			b.Write(data)
		}
	}
}
