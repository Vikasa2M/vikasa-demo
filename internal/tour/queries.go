package tour

import "fmt"

// federationDB is the fixed ClickHouse database federation-sink writes into
// (see cmd/federation-sink) — unlike the per-DOT databases, there is only
// ever one of these.
const federationDB = "vikasa_federation"

// --- Query builders (pure, unit-tested) ---
//
// These extend internal/verify's fixed baseline/dedup checks with the
// cabinet-scoped and federation queries the tour's phase assertions need.
// They're executed via verify.Query (the same ClickHouse HTTP POST helper
// internal/verify's own checks use) and parsed with verify.ParseScalarInt.

// CabinetHeartbeatFreshnessQuery returns the age, in seconds, of the
// freshest heartbeat ingested for one cabinet — unlike
// verify.HeartbeatFreshnessQuery, which reports the fleet-wide max across
// every cabinet in db and so can't detect a single cut cabinet going stale
// while its siblings keep reporting.
func CabinetHeartbeatFreshnessQuery(db, cabinet string) string {
	return fmt.Sprintf(
		"SELECT dateDiff('second', max(ingested_at), now64(3)) FROM %s.heartbeats WHERE cabinet_id = '%s' FORMAT TSV",
		db, cabinet)
}

// ActiveFaultCountQuery counts fault_ids currently raised (argMax(raised,
// ce_time) = 1, i.e. the latest raised/cleared transition for that fault_id
// was a raise) for one cabinet within the last hour. Mirrors the fleet-health
// dashboard's "Active faults" panel SQL, scoped to a single per-DOT database
// and cabinet rather than the DMZ-filtered vikasa_federation view (a
// non-corridor cabinet's faults never cross the DMZ, so asserting against
// federation would never see them).
func ActiveFaultCountQuery(db, cabinet string) string {
	return fmt.Sprintf(
		"SELECT count() FROM (SELECT fault_id, argMax(raised, ce_time) AS active FROM %s.controller_fault_event "+
			"WHERE cabinet_id = '%s' AND ce_time > now() - INTERVAL 1 HOUR GROUP BY fault_id HAVING active = 1) FORMAT TSV",
		db, cabinet)
}

// ReversibleDirectionsQuery counts the distinct OPEN directions the reversible
// express-lane segment has shown in the last 5 minutes. A healthy scheduled
// segment flips NORTHBOUND <-> SOUTHBOUND, so this returns 2; a stuck (or
// never-started) segment returns 0 or 1.
func ReversibleDirectionsQuery(db string) string {
	return fmt.Sprintf(
		"SELECT count(DISTINCT open_direction) FROM %s.reversible_lane_state "+
			"WHERE flow_state = 'LANE_FLOW_STATE_OPEN' AND ce_time > now() - INTERVAL 5 MINUTE FORMAT TSV",
		db)
}

// LatestOperationalModeQuery returns the most recent operational_status mode
// ("coordinated" or "flash") reported by one cabinet. Empty string if the
// cabinet has never reported.
func LatestOperationalModeQuery(db, cabinet string) string {
	return fmt.Sprintf(
		"SELECT argMax(mode, ce_time) FROM %s.operational_status WHERE cabinet_id = '%s' FORMAT TSV",
		db, cabinet)
}

// PhaseStateChangeCountQuery counts phase_state_change rows for one cabinet
// in the last sinceMinutes minutes — used post-restore to prove the
// cabinet's buffered backlog (accumulated while its WAN uplink was cut)
// actually landed once reconnected, not just that new events resumed.
func PhaseStateChangeCountQuery(db, cabinet string, sinceMinutes int) string {
	return fmt.Sprintf(
		"SELECT count() FROM %s.phase_state_change WHERE cabinet_id = '%s' AND ce_time > now() - INTERVAL %d MINUTE FORMAT TSV",
		db, cabinet, sinceMinutes)
}

// FederationDetectedIncidentCountQuery counts perception_incident rows for
// one dot/cabinet in vikasa_federation whose latest state
// (argMax(state, ce_time)) is "detected" within the last hour — proof a
// perception incident crossed the DMZ into the shared federation view.
func FederationDetectedIncidentCountQuery(dot, cabinet string) string {
	return fmt.Sprintf(
		"SELECT count() FROM (SELECT cabinet_id, incident_id, argMax(state, ce_time) AS state FROM %s.perception_incident "+
			"WHERE dot = '%s' AND cabinet_id = '%s' AND ce_time > now() - INTERVAL 1 HOUR "+
			"GROUP BY cabinet_id, incident_id HAVING state = 'detected') FORMAT TSV",
		federationDB, dot, cabinet)
}

// FederationAdvisoryCountQuery counts dms_event rows for one dot/cabinet in
// vikasa_federation whose latest mode-changed transition
// (argMax(mode, ce_time)) is "advisory" within the last hour — proof the
// DMS advisory posted alongside a corridor incident also crossed the DMZ.
func FederationAdvisoryCountQuery(dot, cabinet string) string {
	return fmt.Sprintf(
		"SELECT count() FROM (SELECT cabinet_id, device_id, argMax(mode, ce_time) AS mode FROM %s.dms_event "+
			"WHERE dot = '%s' AND cabinet_id = '%s' AND event_kind = 'mode-changed' AND ce_time > now() - INTERVAL 1 HOUR "+
			"GROUP BY cabinet_id, device_id HAVING mode = 'advisory') FORMAT TSV",
		federationDB, dot, cabinet)
}

// FederationNonCorridorLeakQuery counts vikasa_federation.perception_incident
// rows for dot whose cabinet_id is NOT one of corridorCabinets within the
// last hour — must be 0. This is the DMZ-boundary proof: only corridor
// cabinets share their events across DOTs, so if a non-corridor cabinet's
// incident ever showed up here, the DMZ boundary would have failed.
func FederationNonCorridorLeakQuery(dot string, corridorCabinets []string) string {
	inList := "''" // deliberately unmatchable if the list is empty
	if len(corridorCabinets) > 0 {
		inList = "'" + corridorCabinets[0] + "'"
		for _, c := range corridorCabinets[1:] {
			inList += ", '" + c + "'"
		}
	}
	return fmt.Sprintf(
		"SELECT count() FROM %s.perception_incident WHERE dot = '%s' AND cabinet_id NOT IN (%s) "+
			"AND ce_time > now() - INTERVAL 1 HOUR FORMAT TSV",
		federationDB, dot, inList)
}
