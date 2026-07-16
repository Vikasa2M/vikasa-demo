package sink

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/openits/openits-models/pkg/proto/openits/common/v1"
	perceptionv1 "github.com/openits/openits-models/pkg/proto/openits/perception/v1"
	reversiblelanev1 "github.com/openits/openits-models/pkg/proto/openits/reversible_lane/v1"
	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
	trafficsensorv1 "github.com/openits/openits-models/pkg/proto/openits/traffic_sensor/v1"
	"github.com/Vikasa2M/vikasa-demo/internal/events"
)

// envelopeColumns are the columns every typed table carries first, in this
// exact order (see deploy/clickhouse/migrations/002_event_tables.sql).
var envelopeColumns = []string{"ce_id", "ce_time", "occurred_at", "dot", "district", "cabinet_id", "device_id"}

// cols builds a table's full column list: the envelope prefix followed by
// the table-specific payload columns.
func cols(payload ...string) []string {
	c := make([]string, 0, len(envelopeColumns)+len(payload))
	c = append(c, envelopeColumns...)
	c = append(c, payload...)
	return c
}

// prefix builds the envelope-derived values common to every typed row:
// ce_id, ce_time, occurred_at, dot, district, cabinet_id, device_id.
func prefix(env *events.Envelope, p events.SubjectParts) []any {
	return []any{env.ID, env.Time, env.Time, p.Dot, p.District, p.Cabinet, p.Controller}
}

// b2u converts a bool payload field to the UInt8 (0/1) ClickHouse expects.
func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// parseF32 parses a proto string-encoded decimal field into a float32,
// defaulting to 0 on parse failure (per task-10 mapping rules).
func parseF32(s string) float32 {
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return 0
	}
	return float32(v)
}

func unmarshalErr(ceType string, err error) error {
	return fmt.Errorf("sink: unmarshal %s payload: %w", ceType, err)
}

func init() {
	Registry["vikasa.signal-control.phase-state-change.v1"] = Handler{
		Table:   "phase_state_change",
		Columns: cols("phase_number", "to_state", "termination_reason", "hold_active", "call_registered"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.PhaseStateChange
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p),
				uint8(m.GetPhaseNumber()), m.GetToState().String(), m.GetTerminationReason().String(),
				b2u(m.GetHoldActive()), b2u(m.GetCallRegistered()))
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.detector-transition.v1"] = Handler{
		Table:   "detector_transition",
		Columns: cols("channel", "transition", "lane", "approach", "phase_served"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.DetectorTransition
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p),
				uint8(m.GetChannel()), m.GetTransition().String(), m.GetLane(), m.GetApproach(),
				uint8(m.GetPhaseServed()))
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.pedestrian-event.v1"] = Handler{
		Table:   "pedestrian_event",
		Columns: cols("phase_number", "event_kind"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.PedestrianEvent
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), uint8(m.GetPhaseNumber()), m.GetEvent().String())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.coordination-change.v1"] = Handler{
		Table:   "coordination_change",
		Columns: cols("plan_id", "state"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.CoordinationChange
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			// NOTE: proto has no plan_id field; NewValue is the closest analogue.
			// Documented mismatch, see task-10-report.md.
			row := append(prefix(env, p), strconv.FormatInt(int64(m.GetNewValue()), 10), m.GetChangeKind().String())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.preemption-event.v1"] = Handler{
		Table:   "preemption_event",
		Columns: cols("preempt_number", "state"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.PreemptionEvent
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), uint8(m.GetPreemptNumber()), m.GetStage().String())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.controller-fault-event.v1"] = Handler{
		Table:   "controller_fault_event",
		Columns: cols("fault_id", "raised", "severity"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.ControllerFaultEvent
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), m.GetFaultId(), b2u(m.GetRaised()), m.GetSeverity().String())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.signal-control.operational-status-report.v1"] = Handler{
		Table:   "operational_status",
		Columns: cols("mode", "active_plan_id"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m signalcontrolv1.OperationalStatusReport
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			// NOTE: proto has no active plan id field; left empty.
			// Documented mismatch, see task-10-report.md.
			row := append(prefix(env, p), m.GetMode(), "")
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.traffic-sensor.traffic-interval-report.v1"] = Handler{
		Table:   "traffic_sensor_lane_interval",
		Columns: cols("lane_id", "interval_s", "volume", "speed_kmh", "occupancy", "density"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m trafficsensorv1.TrafficIntervalReport
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			lanes := m.GetLane()
			rows := make([][]any, 0, len(lanes))
			for _, ln := range lanes {
				row := append(prefix(env, p),
					strconv.FormatUint(uint64(ln.GetLaneId()), 10),
					uint16(ln.GetIntervalDurationS()),
					uint16(ln.GetVolume()),
					parseF32(ln.GetSpeedAverageKmh()),
					parseF32(ln.GetOccupancy()),
					parseF32(ln.GetDensity()))
				rows = append(rows, row)
			}
			return rows, nil
		},
	}

	Registry["vikasa.traffic-sensor.traffic-sensor-status-report.v1"] = Handler{
		Table:   "traffic_sensor_status",
		Columns: cols("status"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m trafficsensorv1.TrafficSensorStatusReport
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), m.GetOperationalStatus().String())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.traffic-sensor.queue-state-changed.v1"] = Handler{
		Table:   "queue_state",
		Columns: cols("zone_id", "queued", "queue_length_m"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m trafficsensorv1.QueueStateChanged
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			// NOTE: proto carries QueueDurationS, not a queue length; set to 0.
			// Documented mismatch, see task-10-report.md.
			row := append(prefix(env, p), m.GetZoneId(), b2u(m.GetQueueing()), float32(0))
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.perception.zone-interval-report.v1"] = Handler{
		Table:   "perception_zone_interval",
		Columns: cols("zone_id", "vehicle_class", "count"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m perceptionv1.ZoneIntervalReport
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			var rows [][]any
			for _, z := range m.GetZone() {
				for _, cc := range z.GetClassCount() {
					row := append(prefix(env, p), z.GetZoneId(), cc.GetClass(), uint16(cc.GetCount()))
					rows = append(rows, row)
				}
			}
			return rows, nil
		},
	}

	Registry["vikasa.perception.zone-incident-detected.v1"] = Handler{
		Table:   "perception_incident",
		Columns: cols("incident_id", "zone_id", "incident_type", "state"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m perceptionv1.ZoneIncidentDetected
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), m.GetIncidentId(), m.GetZoneId(), m.GetType(), "detected")
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.perception.zone-incident-cleared.v1"] = Handler{
		Table:   "perception_incident",
		Columns: cols("incident_id", "zone_id", "incident_type", "state"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m perceptionv1.ZoneIncidentCleared
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), m.GetIncidentId(), m.GetZoneId(), "", "cleared")
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.dms.mode-changed.v1"] = Handler{
		Table:   "dms_event",
		Columns: cols("event_kind", "mode", "fault_id"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m commonv1.ModeChanged
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), "mode-changed", m.GetCurrent(), "")
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.dms.fault-raised.v1"] = Handler{
		Table:   "dms_event",
		Columns: cols("event_kind", "mode", "fault_id"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m commonv1.FaultRaised
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), "fault-raised", "", m.GetFaultId())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.dms.fault-cleared.v1"] = Handler{
		Table:   "dms_event",
		Columns: cols("event_kind", "mode", "fault_id"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m commonv1.FaultCleared
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p), "fault-cleared", "", m.GetFaultId())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.reversible-lane.lane-state-changed.v1"] = Handler{
		Table:   "reversible_lane_state",
		Columns: cols("flow_state", "open_direction", "previous_direction", "initiated_by"),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			var m reversiblelanev1.LaneStateChanged
			if err := proto.Unmarshal(env.Data, &m); err != nil {
				return nil, unmarshalErr(env.Type, err)
			}
			row := append(prefix(env, p),
				m.GetNewState().String(), m.GetNewDirection().String(),
				m.GetPreviousDirection().String(), m.GetInitiatedBy())
			return [][]any{row}, nil
		},
	}

	Registry["vikasa.gateway.heartbeat.v1"] = Handler{
		Table:   "heartbeats",
		Columns: cols(),
		Extract: func(env *events.Envelope, p events.SubjectParts) ([][]any, error) {
			return [][]any{prefix(env, p)}, nil
		},
	}
}
