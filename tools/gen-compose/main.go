// Command gen-compose renders the full N-DOT demo fleet's docker compose
// overlay, NATS confs, cabinet sim configs, topology inventories, and
// ClickHouse dimension seed SQL from deploy/topology/fleet.yaml — the fleet
// SSOT.
//
// Usage:
//
//	go run ./tools/gen-compose -fleet deploy/topology/fleet.yaml -out deploy/compose
//
// -out names the compose output directory (conventionally deploy/compose).
// Its PARENT directory is treated as this repo's fixed `deploy/` root, so:
//   - deploy/compose/nats/*.conf, deploy/compose/sims/*.yaml, and
//     deploy/compose/docker-compose.generated.yml land under -out itself;
//   - deploy/topology/inventories/{dot}-cabinets.json lands under
//     <parent-of-out>/topology/inventories/;
//   - deploy/clickhouse/seed.generated.sql lands under
//     <parent-of-out>/clickhouse/.
//
// All generated output is gitignored and rebuilt by `make gen-compose`.
//
// SCALE=1 in the environment restricts every DOT to its corridor cabinet(s)
// — those with a non-empty fleet.yaml "corridor" field, in practice exactly
// one per DOT — instead of all three, for a lighter/faster bring-up. This is
// a filter on the corridor field, not a positional truncation, so it doesn't
// depend on fleet.yaml listing the corridor cabinet first.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

func main() {
	fleetPath := flag.String("fleet", "", "path to fleet.yaml")
	outDir := flag.String("out", "", "compose output directory (e.g. deploy/compose)")
	flag.Parse()

	if *fleetPath == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, "usage: gen-compose -fleet deploy/topology/fleet.yaml -out deploy/compose")
		os.Exit(2)
	}

	scale1 := os.Getenv("SCALE") == "1"
	if err := run(*fleetPath, *outDir, scale1); err != nil {
		log.Fatalf("gen-compose: %v", err)
	}
}

// ---------------------------------------------------------------------------
// fleet.yaml model
// ---------------------------------------------------------------------------

// Fleet is the top-level shape of deploy/topology/fleet.yaml.
type Fleet struct {
	Region string `yaml:"region"`
	Dots   []DOT  `yaml:"dots"`
}

// DOT is one department-of-transportation island in the fleet.
type DOT struct {
	Dot      string    `yaml:"dot"`
	District string    `yaml:"district"`
	Cabinets []Cabinet `yaml:"cabinets"`
}

// Cabinet is one roadside cabinet's identity data.
type Cabinet struct {
	ID       string `yaml:"id"`
	Vendor   string `yaml:"vendor"`
	Corridor string `yaml:"corridor"`
	// Route is the physical roadway a cabinet sits on ('i85', 'i75s', or ''
	// for an arterial) — a dimension for corridor views (e.g. the GDOT I-85
	// perception corridor). Distinct from Corridor, which marks the single
	// federation-shared hero cabinet per DOT.
	Route   string  `yaml:"route"`
	Lat     float64 `yaml:"lat"`
	Lon     float64 `yaml:"lon"`
	Seed    int64   `yaml:"seed"`
	BaseVPH float64 `yaml:"base_vph"`
	// Expose forces a host-mapped sim HTTP port on a NON-hero cabinet so the
	// demo tour can reach its /inject and /healthz endpoints (e.g. the
	// signal-fault phase's cab-002). Hero cabinets always get a host port
	// regardless; this is only for the handful of non-hero cabinets the tour
	// interacts with directly. Without it, non-hero cabinets are internal
	// (vikasa-network) only — which is what keeps ~100 cabinets from binding
	// ~100 host ports.
	Expose bool `yaml:"expose"`
	// Reversible marks the one I-75 South cabinet that also runs a scheduled
	// reversible express-lane segment (adds a ReversibleLane sim device). It
	// only flips a flag in the cabinet's sim YAML.
	Reversible bool `yaml:"reversible"`
}

// portMapped reports whether a cabinet gets a host-mapped sim HTTP port:
// every hero (corridor) cabinet, plus any non-hero cabinet flagged Expose.
// tools/gen-compose and internal/tour.LoadFleetInfo MUST agree on this
// predicate and walk cabinets in the same fleet.yaml order, or the tour's
// cabinet->port table drifts from the generated compose port mappings.
func portMapped(c Cabinet) bool { return c.Corridor != "" || c.Expose }

func loadFleet(path string) (Fleet, error) {
	var fleet Fleet
	data, err := os.ReadFile(path)
	if err != nil {
		return fleet, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &fleet); err != nil {
		return fleet, fmt.Errorf("parse %s: %w", path, err)
	}
	return fleet, nil
}

// ---------------------------------------------------------------------------
// naming — the CRITICAL naming consistency every artifact below shares.
// ---------------------------------------------------------------------------

// jsDomain is the cabinet's JetStream domain / topology-inventory id:
// "<dot>-<district>-<id>", e.g. "gdot-d1-cab-i85-001".
func jsDomain(dot, district, id string) string {
	return fmt.Sprintf("%s-%s-%s", dot, district, id)
}

// subjectFilter is the cabinet's inventory filter subject:
// "vikasa.<dot>.<district>.<id>.>".
func subjectFilter(dot, district, id string) string {
	return fmt.Sprintf("vikasa.%s.%s.%s.>", dot, district, id)
}

// leafService is the compose service name of a cabinet's leaf NATS
// container: "<dot>-<id>-nats".
func leafService(dot, id string) string {
	return fmt.Sprintf("%s-%s-nats", dot, id)
}

// simService is the compose service name of a cabinet's sim: "<id>-sim".
func simService(id string) string {
	return id + "-sim"
}

// cabinetNet is the cabinet's private compose network: "<id>-net".
func cabinetNet(id string) string {
	return id + "-net"
}

// confName is the filename (no directory) of a per-cabinet or per-tier NATS
// conf: "<dot>-<id-or-tier>.conf".
func confName(dot, suffix string) string {
	return fmt.Sprintf("%s-%s.conf", dot, suffix)
}

// simConfName is the filename (no directory) of a cabinet's sim YAML:
// "<dot>-<id>.yaml".
func simConfName(dot, id string) string {
	return fmt.Sprintf("%s-%s.yaml", dot, id)
}

// centralStreamName is the central-tier JetStream stream a DOT's sink
// drains, as rendered by vikasa-infra's cmd/gen from the topology spec's
// district id + single partition id "<district>/0" (confirmed against
// deploy/topology/rendered/gdot/clusters/core/streams/*.json:
// "VIKASA_GDOT_CENTRAL_D1_D1_0" for district "d1"). This assumes every
// DOT's topology spec has exactly one partition named "<district>/0", true
// for the committed gdot.json and for the ncdot.json/scdot.json Task 14
// copies from it.
func centralStreamName(dot, district string) string {
	d := strings.ToUpper(district)
	return fmt.Sprintf("VIKASA_%s_CENTRAL_%s_%s_0", strings.ToUpper(dot), d, d)
}

// sinkFilter is a DOT sink's NATS subject filter: "vikasa.<dot>.>".
func sinkFilter(dot string) string {
	return fmt.Sprintf("vikasa.%s.>", dot)
}

// chDatabase is a DOT's ClickHouse database name: "vikasa_<dot>".
func chDatabase(dot string) string {
	return "vikasa_" + dot
}

// ---------------------------------------------------------------------------
// run — top-level orchestration
// ---------------------------------------------------------------------------

// run loads fleet.yaml, applies SCALE=1 if requested, and writes every
// generated artifact under outDir (compose-related) and outDir's parent
// (topology inventories, ClickHouse seed).
func run(fleetPath, outDir string, scale1 bool) error {
	fleet, err := loadFleet(fleetPath)
	if err != nil {
		return err
	}
	if scale1 {
		for i := range fleet.Dots {
			var corridor []Cabinet
			for _, c := range fleet.Dots[i].Cabinets {
				if c.Corridor != "" {
					corridor = append(corridor, c)
				}
			}
			fleet.Dots[i].Cabinets = corridor
		}
	}

	deployDir := filepath.Dir(outDir)
	natsDir := filepath.Join(outDir, "nats")
	simsDir := filepath.Join(outDir, "sims")
	invDir := filepath.Join(deployDir, "topology", "inventories")
	chDir := filepath.Join(deployDir, "clickhouse")

	// natsDir/simsDir hold one file per cabinet (or per DOT for tier
	// confs); wipe them before regenerating so switching SCALE on/off (or
	// editing fleet.yaml to drop a cabinet) can't leave a stale conf/sim
	// file behind from a previous run with a different cabinet set.
	// invDir/chDir/outDir's own files are always written wholesale in full
	// (one file per DOT, or one combined file), so no such staleness risk
	// exists there — RemoveAll would just be redundant.
	for _, dir := range []string{natsDir, simsDir} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("clean %s: %w", dir, err)
		}
	}

	for _, dir := range []string{outDir, natsDir, simsDir, invDir, chDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := writeNATSConfs(fleet, natsDir); err != nil {
		return err
	}
	if err := writeSimConfigs(fleet, simsDir); err != nil {
		return err
	}
	if err := writeInventories(fleet, invDir); err != nil {
		return err
	}
	if err := writeSeedSQL(fleet, filepath.Join(chDir, "seed.generated.sql")); err != nil {
		return err
	}
	if err := writeCompose(fleet, filepath.Join(outDir, "docker-compose.generated.yml")); err != nil {
		return err
	}
	promDir := filepath.Join(deployDir, "prometheus")
	if err := os.MkdirAll(promDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", promDir, err)
	}
	if err := writePrometheusTargets(fleet, filepath.Join(promDir, "targets.generated.json")); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// NATS confs — Task 7 patterns, generalized across every DOT/cabinet.
// ---------------------------------------------------------------------------

var cabinetConfTmpl = template.Must(template.New("cabinet.conf").Parse(
	`server_name: {{.Domain}}
port: 4222
http_port: 8222
jetstream { domain: "{{.Domain}}", store_dir: "/data" }
leafnodes {
  remotes = [ { urls: ["nats://{{.Dot}}-regional:7422"] } ]
}
`))

var regionalConfTmpl = template.Must(template.New("regional.conf").Parse(
	`server_name: {{.Dot}}-regional
port: 4222
http_port: 8222
jetstream { domain: "d1a", store_dir: "/data" }
leafnodes {
  port: 7422
  remotes = [ { urls: ["nats://{{.Dot}}-central:7422"] } ]
}
`))

var centralConfTmpl = template.Must(template.New("central.conf").Parse(
	`server_name: {{.Dot}}-central
port: 4222
http_port: 8222
jetstream { domain: "core", store_dir: "/data" }
leafnodes { port: 7422 }
`))

var dmzConfTmpl = template.Must(template.New("dmz.conf").Parse(
	`server_name: {{.Dot}}-dmz
port: 4222
http_port: 8222
jetstream { domain: "dmz", store_dir: "/data" }
leafnodes {
  remotes = [ { urls: ["nats://{{.Dot}}-central:7422"] } ]
}
`))

type cabinetConfData struct {
	Dot    string
	Domain string
}

type tierConfData struct {
	Dot string
}

func writeNATSConfs(fleet Fleet, natsDir string) error {
	for _, d := range fleet.Dots {
		if err := renderTo(regionalConfTmpl, tierConfData{Dot: d.Dot}, filepath.Join(natsDir, confName(d.Dot, "regional"))); err != nil {
			return err
		}
		if err := renderTo(centralConfTmpl, tierConfData{Dot: d.Dot}, filepath.Join(natsDir, confName(d.Dot, "central"))); err != nil {
			return err
		}
		if err := renderTo(dmzConfTmpl, tierConfData{Dot: d.Dot}, filepath.Join(natsDir, confName(d.Dot, "dmz"))); err != nil {
			return err
		}
		for _, c := range d.Cabinets {
			data := cabinetConfData{Dot: d.Dot, Domain: jsDomain(d.Dot, d.District, c.ID)}
			path := filepath.Join(natsDir, confName(d.Dot, c.ID))
			if err := renderTo(cabinetConfTmpl, data, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cabinet sim configs — matches internal/sim.Config's YAML tags exactly.
// ---------------------------------------------------------------------------

var simConfTmpl = template.Must(template.New("sim.yaml").Parse(
	`dot: {{.Dot}}
district: {{.District}}
cabinet: {{.ID}}
vendor: {{.Vendor}}
seed: {{.Seed}}
base_vph: {{.BaseVPH}}
nats_url: nats://{{.NATSHost}}:4222
http_addr: ":8080"
reversible: {{.Reversible}}
`))

type simConfData struct {
	Dot        string
	District   string
	ID         string
	Vendor     string
	Seed       int64
	BaseVPH    float64
	NATSHost   string
	Reversible bool
}

func writeSimConfigs(fleet Fleet, simsDir string) error {
	for _, d := range fleet.Dots {
		for _, c := range d.Cabinets {
			data := simConfData{
				Dot:        d.Dot,
				District:   d.District,
				ID:         c.ID,
				Vendor:     c.Vendor,
				Seed:       c.Seed,
				BaseVPH:    c.BaseVPH,
				NATSHost:   leafService(d.Dot, c.ID),
				Reversible: c.Reversible,
			}
			path := filepath.Join(simsDir, simConfName(d.Dot, c.ID))
			if err := renderTo(simConfTmpl, data, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Topology inventories — encoding/json rather than text/template: this is
// structured data with strings that could need escaping, and json.Marshal
// is the safer tool for that than hand-templated JSON.
// ---------------------------------------------------------------------------

type inventoryCabinet struct {
	ID        string `json:"id"`
	Partition string `json:"partition"`
	Filter    string `json:"filter"`
}

type inventory struct {
	Dot      string             `json:"dot"`
	Cabinets []inventoryCabinet `json:"cabinets"`
}

func writeInventories(fleet Fleet, invDir string) error {
	for _, d := range fleet.Dots {
		inv := inventory{Dot: d.Dot}
		for _, c := range d.Cabinets {
			inv.Cabinets = append(inv.Cabinets, inventoryCabinet{
				ID:        jsDomain(d.Dot, d.District, c.ID),
				Partition: d.District + "/0",
				Filter:    subjectFilter(d.Dot, d.District, c.ID),
			})
		}
		// A plain json.Marshal/MarshalIndent HTML-escapes '>' to ">",
		// which is valid JSON but unreadable given every filter subject ends
		// in ">". Use an Encoder with SetEscapeHTML(false) so filters render
		// with the literal glob character, matching the hand-written
		// convention this file supersedes.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(inv); err != nil {
			return fmt.Errorf("marshal %s inventory: %w", d.Dot, err)
		}
		path := filepath.Join(invDir, d.Dot+"-cabinets.json")
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ClickHouse dimension seed SQL — cabinets + devices, per-DOT DB and
// vikasa_federation. Built with fmt.Fprintf loops rather than nested
// text/template range blocks: the last-row-gets-a-semicolon-not-a-comma
// bookkeeping is far less error-prone as a Go loop than as template
// whitespace/action control.
// ---------------------------------------------------------------------------

type deviceKind struct {
	ID   string
	Kind string
}

// deviceKinds is the fixed 6-device-per-cabinet roster (Task 9/plan
// "Devices per cabinet"): asc-1, cam-1, cam-2, lidar-1, dms-1, gw.
var deviceKinds = []deviceKind{
	{"asc-1", "asc"},
	{"cam-1", "camera"},
	{"cam-2", "camera"},
	{"lidar-1", "lidar"},
	{"dms-1", "dms"},
	{"gw", "gateway"},
}

func writeSeedSQL(fleet Fleet, path string) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "-- GENERATED by tools/gen-compose from deploy/topology/fleet.yaml.\n")
	fmt.Fprintf(&buf, "-- Do not edit by hand; regenerate with `make gen-compose`.\n\n")

	fmt.Fprintf(&buf, "-- ============================================================\n")
	fmt.Fprintf(&buf, "-- cabinets\n")
	fmt.Fprintf(&buf, "-- ============================================================\n\n")

	for _, d := range fleet.Dots {
		writeCabinetInsert(&buf, "vikasa_"+d.Dot, fleet.Region, []DOT{d})
	}
	writeCabinetInsert(&buf, "vikasa_federation", fleet.Region, fleet.Dots)

	fmt.Fprintf(&buf, "-- ============================================================\n")
	fmt.Fprintf(&buf, "-- devices — 6 per cabinet: asc-1 (asc), cam-1/cam-2 (camera),\n")
	fmt.Fprintf(&buf, "-- lidar-1 (lidar), dms-1 (dms), gw (gateway); vendor matches the\n")
	fmt.Fprintf(&buf, "-- cabinet.\n")
	fmt.Fprintf(&buf, "-- ============================================================\n\n")

	for _, d := range fleet.Dots {
		writeDeviceInsert(&buf, "vikasa_"+d.Dot, []DOT{d})
	}
	writeDeviceInsert(&buf, "vikasa_federation", fleet.Dots)

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeCabinetInsert(buf *bytes.Buffer, db, region string, dots []DOT) {
	var rows []string
	for _, d := range dots {
		for _, c := range d.Cabinets {
			rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', '%s', '%s', '%s', '%s', %g, %g)",
				d.Dot, d.District, c.ID, c.Corridor, c.Route, region, c.Vendor, c.Lat, c.Lon))
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(buf, "INSERT INTO %s.cabinets (dot, district, cabinet_id, corridor, route, region, vendor, lat, lon) VALUES\n", db)
	writeRows(buf, rows)
}

func writeDeviceInsert(buf *bytes.Buffer, db string, dots []DOT) {
	var rows []string
	for _, d := range dots {
		for _, c := range d.Cabinets {
			for _, dev := range deviceKinds {
				rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', '%s', '%s', '%s')",
					d.Dot, d.District, c.ID, dev.ID, dev.Kind, c.Vendor))
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(buf, "INSERT INTO %s.devices (dot, district, cabinet_id, device_id, device_kind, vendor) VALUES\n", db)
	writeRows(buf, rows)
}

func writeRows(buf *bytes.Buffer, rows []string) {
	for i, row := range rows {
		sep := ","
		if i == len(rows)-1 {
			sep = ";"
		}
		fmt.Fprintf(buf, "%s%s\n", row, sep)
	}
	fmt.Fprintln(buf)
}

// ---------------------------------------------------------------------------
// docker-compose.generated.yml — every NATS/sim/sink/stream-init/exporter
// service for every DOT. Merged at runtime with the static
// docker-compose.yml (shared clickhouse/grafana/prometheus/migrate) via
// `docker compose -f docker-compose.yml -f docker-compose.generated.yml`.
// Every tier server (regional/central/dmz) gets its own
// natsio/prometheus-nats-exporter sidecar here too, one per DOT per tier (9
// total across the 3 DOTs) — see writePrometheusTargets for the matching
// Prometheus file-SD targets. Every cabinet LEAF also gets its own exporter
// sidecar ("<leafService>-exporter", e.g. "gdot-cab-i85-001-nats-exporter"):
// the WAN-cut/buffering demo's whole point is that a cut cabinet's local
// JetStream buffer grows, and that buffer lives on the leaf, not the tier
// servers, so the leaf's storage stat has to be scrapable too — including
// DURING a cut, which is exactly when the dashboard needs it most. The
// cabinet exporter is on BOTH "vikasa" and the cabinet's own private net
// (unlike the tier exporters, which are "vikasa"-only): `democtl cut` (see
// cmd/democtl) disconnects the LEAF from "vikasa" only, leaving it attached
// to its private net so the sim can keep buffering at the edge — confirmed
// live that this means an "vikasa"-only exporter loses the leaf entirely
// mid-cut (scrape target unreachable, metric goes stale right when it should
// be climbing). Staying on the cabinet's private net too keeps the scrape
// path alive through the cut (Prometheus itself only needs "vikasa" to
// reach the exporter; the exporter needs the private net to still reach the
// leaf once "vikasa" is severed).
// ---------------------------------------------------------------------------

var composeTmpl = template.Must(template.New("compose").Parse(
	`# GENERATED by tools/gen-compose from deploy/topology/fleet.yaml. Do not
# edit by hand; regenerate with ` + "`make gen-compose`" + `. Merge with the static
# docker-compose.yml (clickhouse/grafana/prometheus/migrate) via
# -f docker-compose.yml -f docker-compose.generated.yml — see Makefile's
# COMPOSE_ALL. "vikasa" (the shared WAN network) is declared in the static
# file; only the per-cabinet private networks are declared here. NATS
# exporter sidecars (one per tier server) live here too, alongside the
# servers they scrape.
networks:
{{- range .Networks}}
  {{.}}: {}
{{- end}}

services:
{{range .Dots}}
  {{.Dot}}-regional:
    image: nats:2.12-alpine
    command: ["-c", "/etc/nats/nats.conf"]
    networks: [vikasa]
    volumes: [ "./nats/{{.Dot}}-regional.conf:/etc/nats/nats.conf:ro", "{{.Dot}}-regional-data:/data" ]
  {{.Dot}}-central:
    image: nats:2.12-alpine
    command: ["-c", "/etc/nats/nats.conf"]
    networks: [vikasa]
    volumes: [ "./nats/{{.Dot}}-central.conf:/etc/nats/nats.conf:ro", "{{.Dot}}-central-data:/data" ]
  {{.Dot}}-dmz:
    image: nats:2.12-alpine
    command: ["-c", "/etc/nats/nats.conf"]
    networks: [vikasa]
    volumes: [ "./nats/{{.Dot}}-dmz.conf:/etc/nats/nats.conf:ro", "{{.Dot}}-dmz-data:/data" ]
  {{.Dot}}-regional-exporter:
    image: natsio/prometheus-nats-exporter:0.17.2
    command: ["-varz", "http://{{.Dot}}-regional:8222"]
    networks: [vikasa]
    depends_on: [{{.Dot}}-regional]
    restart: on-failure
  {{.Dot}}-central-exporter:
    image: natsio/prometheus-nats-exporter:0.17.2
    command: ["-varz", "http://{{.Dot}}-central:8222"]
    networks: [vikasa]
    depends_on: [{{.Dot}}-central]
    restart: on-failure
  {{.Dot}}-dmz-exporter:
    image: natsio/prometheus-nats-exporter:0.17.2
    command: ["-varz", "http://{{.Dot}}-dmz:8222"]
    networks: [vikasa]
    depends_on: [{{.Dot}}-dmz]
    restart: on-failure
{{range .Cabinets}}
  {{.LeafService}}:
    image: nats:2.12-alpine
    command: ["-c", "/etc/nats/nats.conf"]
    networks: [{{if .Hero}}vikasa, {{.NetName}}{{else}}vikasa{{end}}]
    volumes: [ "./nats/{{.ConfName}}:/etc/nats/nats.conf:ro"{{if .Hero}}, "{{.DataVolume}}:/data"{{end}} ]
{{- if .Hero}}
  {{.LeafService}}-exporter:
    image: natsio/prometheus-nats-exporter:0.17.2
    command: ["-varz", "http://{{.LeafService}}:8222"]
    networks: [vikasa, {{.NetName}}]
    depends_on: [{{.LeafService}}]
    restart: on-failure
{{- end}}
{{end}}
  stream-init-{{.Dot}}:
    image: natsio/nats-box:0.17.0
    networks: [vikasa]
    depends_on: [{{.Dot}}-regional, {{.Dot}}-central, {{.Dot}}-dmz]
    entrypoint: ["/bin/sh", "/stream-init.sh", "{{.Dot}}"]
    volumes:
      - ./stream-init.sh:/stream-init.sh:ro
      - ../topology/rendered:/rendered:ro
    restart: "no"
{{range .Cabinets}}
  {{.SimService}}:
    image: vikasa-demo:dev
    command: ["cabinet-sim", "-config", "/etc/sim.yaml"]
    networks: [{{if .Hero}}{{.NetName}}{{else}}vikasa{{end}}]
    depends_on: [{{.LeafService}}]
    volumes: [ "./sims/{{.SimConfName}}:/etc/sim.yaml:ro" ]
{{- if .PortMapped}}
    ports: ["{{.SimPort}}:8080"]
{{- end}}
{{end}}
  {{.Dot}}-sink:
    image: vikasa-demo:dev
    command: ["central-sink"]
    networks: [vikasa]
    depends_on:
      {{.Dot}}-central:
        condition: service_started
      clickhouse:
        condition: service_healthy
    environment:
      SINK_NATS_URL: nats://{{.Dot}}-central:4222
      SINK_STREAM: {{.SinkStream}}
      SINK_DURABLE: central-sink
      SINK_FILTER: {{.SinkFilter}}
      SINK_CH_DSN: clickhouse://clickhouse:9000
      SINK_CH_DATABASE: {{.SinkDatabase}}
    ports: ["{{.SinkPort}}:9091"]
    restart: on-failure
{{end}}
  federation-sink:
    image: vikasa-demo:dev
    command: ["federation-sink"]
    networks: [vikasa]
    depends_on:
{{range .DotNames}}      {{.}}-dmz:
        condition: service_started
{{end}}      clickhouse:
        condition: service_healthy
    environment:
      FEDERATION_CH_DSN: clickhouse://clickhouse:9000
    ports: ["19094:9091"]
    restart: on-failure
volumes:
{{- range .Volumes}}
  {{.}}: {}
{{- end}}
`))

type composeData struct {
	Networks []string
	Volumes  []string
	Dots     []composeDot
	// DotNames is Dots' Dot field only, for the single federation-sink
	// service's depends_on block (which needs every DOT's dmz service name
	// but isn't itself rendered per-DOT like the rest of the template).
	DotNames []string
}

type composeDot struct {
	Dot          string
	Cabinets     []composeCabinet
	SinkStream   string
	SinkFilter   string
	SinkDatabase string
	SinkPort     int
}

type composeCabinet struct {
	// Hero marks a cabinet with a non-empty fleet.yaml "corridor" field — the
	// one I-85 cabinet per DOT that carries the WAN-cut / edge-buffering demo.
	// Only hero cabinets get a private compose network, a NATS exporter
	// sidecar, and a persistent JetStream data volume; their sim sits on the
	// private net ONLY (so a WAN cut can sever it from the shared "vikasa"
	// network while it keeps buffering to its leaf). Non-hero cabinets exist
	// to give the fleet distributed-network SCALE: they are just a leaf + sim
	// on "vikasa", no private net (avoids exhausting Docker's address pool at
	// ~100 cabinets), no exporter (avoids ~100 Prometheus scrape targets), no
	// volume (ephemeral edge buffer is fine — they never get cut).
	Hero bool
	// PortMapped is true when this cabinet's sim gets a host-mapped HTTP port
	// (all hero cabinets + Expose-flagged non-hero cabinets). Independent of
	// Hero: a non-hero exposed cabinet has a host port but no private net.
	PortMapped  bool
	LeafService string
	ConfName    string
	DataVolume  string
	NetName     string
	SimService  string
	SimConfName string
	SimPort     int
}

func writeCompose(fleet Fleet, path string) error {
	data := composeData{}
	simPort := 18081
	for dotIdx, d := range fleet.Dots {
		cd := composeDot{
			Dot:          d.Dot,
			SinkStream:   centralStreamName(d.Dot, d.District),
			SinkFilter:   sinkFilter(d.Dot),
			SinkDatabase: chDatabase(d.Dot),
			SinkPort:     19091 + dotIdx,
		}
		data.Volumes = append(data.Volumes, d.Dot+"-regional-data", d.Dot+"-central-data", d.Dot+"-dmz-data")

		for _, c := range d.Cabinets {
			cc := composeCabinet{
				Hero:        c.Corridor != "",
				PortMapped:  portMapped(c),
				LeafService: leafService(d.Dot, c.ID),
				ConfName:    confName(d.Dot, c.ID),
				SimService:  simService(c.ID),
				SimConfName: simConfName(d.Dot, c.ID),
			}
			// Only hero cabinets declare a private network + data volume (see
			// composeCabinet.Hero). Non-hero cabinets are leaf+sim on
			// "vikasa" only — this is what keeps the fleet scalable to ~100
			// cabinets without exhausting Docker's network address pool.
			if cc.Hero {
				net := cabinetNet(c.ID)
				dataVol := fmt.Sprintf("%s-%s-data", d.Dot, c.ID)
				data.Networks = append(data.Networks, net)
				data.Volumes = append(data.Volumes, dataVol)
				cc.NetName = net
				cc.DataVolume = dataVol
			}
			// Host-mapped sim port for hero + Expose-flagged cabinets, in
			// fleet order — LoadFleetInfo mirrors this exact walk.
			if cc.PortMapped {
				cc.SimPort = simPort
				simPort++
			}
			cd.Cabinets = append(cd.Cabinets, cc)
		}
		data.Dots = append(data.Dots, cd)
		data.DotNames = append(data.DotNames, d.Dot)
	}

	var buf bytes.Buffer
	if err := composeTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render docker-compose.generated.yml: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// deploy/prometheus/targets.generated.json — Prometheus file_sd_configs
// target file for every generated exporter/sink, since prometheus.yml itself
// stays static and can't hardcode a per-DOT target list. Three target
// groups: "nats" (all 9 <dot>-<tier>-exporter:7777, one per DOT per tier),
// "nats-cabinet" (all 9 <dot>-<cabinetId>-nats-exporter:7777, one per
// cabinet leaf — a separate job from "nats" so the existing tier-only
// up{job="nats"} liveness panel in demo-tour.json keeps meaning exactly what
// it always meant), and "sinks" (the 3 per-DOT sinks + federation-sink, all
// on container port 9091 regardless of their host port mapping in the
// generated compose). Each group's own "labels.job" wins over
// prometheus.yml's scrape-config-level job_name (Prometheus only defaults
// job from job_name when the discovered target doesn't already carry a
// "job" label), which is what lets a single scrape_config + single file
// serve all three jobs without double-scraping.
// ---------------------------------------------------------------------------

type promTargetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

func writePrometheusTargets(fleet Fleet, path string) error {
	var natsTargets []string
	for _, d := range fleet.Dots {
		for _, tier := range []string{"regional", "central", "dmz"} {
			natsTargets = append(natsTargets, fmt.Sprintf("%s-%s-exporter:7777", d.Dot, tier))
		}
	}

	// Only hero cabinets (corridor != "") run a leaf NATS exporter — they are
	// the ones whose edge JetStream buffer the WAN-cut demo watches. Non-hero
	// scale cabinets have no exporter, so scraping them would just be dead
	// targets. See composeCabinet.Hero.
	var natsCabinetTargets []string
	for _, d := range fleet.Dots {
		for _, c := range d.Cabinets {
			if c.Corridor == "" {
				continue
			}
			natsCabinetTargets = append(natsCabinetTargets, fmt.Sprintf("%s-exporter:7777", leafService(d.Dot, c.ID)))
		}
	}

	var sinkTargets []string
	for _, d := range fleet.Dots {
		sinkTargets = append(sinkTargets, fmt.Sprintf("%s-sink:9091", d.Dot))
	}
	sinkTargets = append(sinkTargets, "federation-sink:9091")

	groups := []promTargetGroup{
		{Targets: natsTargets, Labels: map[string]string{"job": "nats"}},
		{Targets: natsCabinetTargets, Labels: map[string]string{"job": "nats-cabinet"}},
		{Targets: sinkTargets, Labels: map[string]string{"job": "sinks"}},
	}

	data, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal prometheus targets: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func renderTo(tmpl *template.Template, data any, path string) error {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render %s: %w", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
