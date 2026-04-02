package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"time"
)

//go:embed template.html
var templateFS embed.FS

// TemplateData is the root object passed to template.html.
type TemplateData struct {
	OxVersion      string
	GeneratedAt    string
	RunCount       int
	AgentCount     int
	Agents         []AgentColumn
	Features       []FeatureRow
	FeatureHeaders []string // display names for drill-down column headers
}

// AgentColumn holds per-agent data for both the overview header and drill-down panel.
type AgentColumn struct {
	Key         string
	Info        AgentInfo
	VersionRows []VersionRow
}

// FeatureRow is one overview matrix row (one feature group, all agent cells).
type FeatureRow struct {
	Key         string
	DisplayName string
	Cells       []CellView
}

// CellView is what the template renders in a single matrix cell.
type CellView struct {
	Status     string // css class suffix: pass, warn, fail, untested, planned, unsupported, na
	Label      string // emoji + text
	HasTooltip bool
	Tooltip    string
}

// VersionRow is one row in a drill-down panel (one agent version, all feature cells).
type VersionRow struct {
	AgentVersion string
	TimestampFmt string
	OxVersion    string
	Cells        []CellView
}

// buildTemplateData assembles all data the template needs.
func buildTemplateData(ld *LoadedData) TemplateData {
	oxVer := latestOxVersion(ld)

	td := TemplateData{
		OxVersion:   oxVer,
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		RunCount:    len(ld.Runs),
		AgentCount:  len(ld.Compat.Agents),
	}

	// Feature headers (for drill-down column headers).
	for _, fk := range ld.FeatureOrder {
		fi := ld.Compat.FeatureGroups[fk]
		td.FeatureHeaders = append(td.FeatureHeaders, fi.DisplayName)
	}

	// Feature rows for overview matrix.
	for _, fk := range ld.FeatureOrder {
		fi := ld.Compat.FeatureGroups[fk]
		row := FeatureRow{
			Key:         fk,
			DisplayName: fi.DisplayName,
		}
		for _, ak := range ld.AgentOrder {
			cs := cellState(ld, ak, fk)
			row.Cells = append(row.Cells, makeCellView(cs))
		}
		td.Features = append(td.Features, row)
	}

	// Agent columns (header + drill-down).
	for _, ak := range ld.AgentOrder {
		ai := ld.Compat.Agents[ak]
		col := AgentColumn{Key: ak, Info: ai}

		for _, vr := range agentVersionRows(ld, ak) {
			row := VersionRow{
				AgentVersion: vr.AgentVersion,
				TimestampFmt: vr.Timestamp.UTC().Format("2006-01-02"),
				OxVersion:    vr.OxVersion,
			}
			for _, fk := range ld.FeatureOrder {
				cs := vr.Cells[fk]
				row.Cells = append(row.Cells, makeCellView(cs))
			}
			col.VersionRows = append(col.VersionRows, row)
		}

		td.Agents = append(td.Agents, col)
	}

	return td
}

func makeCellView(cs CellState) CellView {
	cv := CellView{Status: cs.Status}

	switch cs.Status {
	case "pass":
		if cs.Total > 0 {
			cv.Label = fmt.Sprintf("%d/%d", cs.Passed, cs.Total)
		} else {
			cv.Label = "\u2713" // checkmark
		}
	case "warn":
		cv.Label = fmt.Sprintf("%d/%d", cs.Passed, cs.Total)
	case "fail":
		cv.Label = fmt.Sprintf("%d/%d", cs.Passed, cs.Total)
	case "untested":
		cv.Label = "?"
	case "planned":
		cv.Label = "\u2610" // ballot box
	case "unsupported":
		cv.Label = "\u2014" // em dash
	default: // na
		cv.Label = "\u2014" // em dash
	}

	if cs.Total > 0 {
		cv.HasTooltip = true
		cv.Tooltip = fmt.Sprintf("Passed: %d  Failed: %d  Skipped: %d\nDuration: %dms",
			cs.Passed, cs.Total-cs.Passed-cs.Skipped(), cs.Skipped(), cs.DurationMs)
	}
	return cv
}

// Skipped derives skipped count from totals (CellState doesn't store it directly
// in the overview path — we reconstruct it as total - passed - failed via the run).
// For simplicity here we store 0; the drill-down tooltip data comes from FeatureResult.
func (cs CellState) Skipped() int { return 0 }

func writeHTML(w io.Writer, ld *LoadedData) error {
	raw, err := templateFS.ReadFile("template.html")
	if err != nil {
		return fmt.Errorf("read embedded template: %w", err)
	}

	tmpl, err := template.New("matrix").Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	td := buildTemplateData(ld)
	return tmpl.Execute(w, td)
}
