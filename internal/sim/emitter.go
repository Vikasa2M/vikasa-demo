// internal/sim/emitter.go
package sim

import (
	"time"

	"google.golang.org/protobuf/proto"
)

// Emitter is implemented by the cabinet publisher (Task 6) and by test fakes.
type Emitter interface {
	Emit(controller, ceType string, occurredAt time.Time, msg proto.Message)
}

// Device is the common shape for all simulated devices.
type Device interface {
	Tick(now time.Time, em Emitter) // advance to `now`, emit any due events
}
