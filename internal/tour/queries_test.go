package tour

import "testing"

func TestQueryBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			"CabinetHeartbeatFreshnessQuery",
			CabinetHeartbeatFreshnessQuery("vikasa_gdot", "cab-i85-001"),
			"SELECT dateDiff('second', max(ingested_at), now64(3)) FROM vikasa_gdot.heartbeats WHERE cabinet_id = 'cab-i85-001' FORMAT TSV",
		},
		{
			"ActiveFaultCountQuery",
			ActiveFaultCountQuery("vikasa_gdot", "cab-002"),
			"SELECT count() FROM (SELECT fault_id, argMax(raised, ce_time) AS active FROM vikasa_gdot.controller_fault_event " +
				"WHERE cabinet_id = 'cab-002' AND ce_time > now() - INTERVAL 1 HOUR GROUP BY fault_id HAVING active = 1) FORMAT TSV",
		},
		{
			"LatestOperationalModeQuery",
			LatestOperationalModeQuery("vikasa_gdot", "cab-002"),
			"SELECT argMax(mode, ce_time) FROM vikasa_gdot.operational_status WHERE cabinet_id = 'cab-002' FORMAT TSV",
		},
		{
			"PhaseStateChangeCountQuery",
			PhaseStateChangeCountQuery("vikasa_gdot", "cab-i85-001", 15),
			"SELECT count() FROM vikasa_gdot.phase_state_change WHERE cabinet_id = 'cab-i85-001' AND ce_time > now() - INTERVAL 15 MINUTE FORMAT TSV",
		},
		{
			"FederationDetectedIncidentCountQuery",
			FederationDetectedIncidentCountQuery("gdot", "cab-i85-001"),
			"SELECT count() FROM (SELECT cabinet_id, incident_id, argMax(state, ce_time) AS state FROM vikasa_federation.perception_incident " +
				"WHERE dot = 'gdot' AND cabinet_id = 'cab-i85-001' AND ce_time > now() - INTERVAL 1 HOUR " +
				"GROUP BY cabinet_id, incident_id HAVING state = 'detected') FORMAT TSV",
		},
		{
			"FederationAdvisoryCountQuery",
			FederationAdvisoryCountQuery("gdot", "cab-i85-001"),
			"SELECT count() FROM (SELECT cabinet_id, device_id, argMax(mode, ce_time) AS mode FROM vikasa_federation.dms_event " +
				"WHERE dot = 'gdot' AND cabinet_id = 'cab-i85-001' AND event_kind = 'mode-changed' AND ce_time > now() - INTERVAL 1 HOUR " +
				"GROUP BY cabinet_id, device_id HAVING mode = 'advisory') FORMAT TSV",
		},
		{
			"FederationNonCorridorLeakQuery",
			FederationNonCorridorLeakQuery("gdot", []string{"cab-i85-001", "cab-i85-101", "cab-i85-201"}),
			"SELECT count() FROM vikasa_federation.perception_incident WHERE dot = 'gdot' AND cabinet_id NOT IN ('cab-i85-001', 'cab-i85-101', 'cab-i85-201') " +
				"AND ce_time > now() - INTERVAL 1 HOUR FORMAT TSV",
		},
		{
			"FederationNonCorridorLeakQuery empty list",
			FederationNonCorridorLeakQuery("gdot", nil),
			"SELECT count() FROM vikasa_federation.perception_incident WHERE dot = 'gdot' AND cabinet_id NOT IN ('') " +
				"AND ce_time > now() - INTERVAL 1 HOUR FORMAT TSV",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got  %q\nwant %q", tc.got, tc.want)
			}
		})
	}
}
