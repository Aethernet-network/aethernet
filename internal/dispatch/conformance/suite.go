// Package conformance provides shared test templates that every
// canonical-event consumer must pass. Per C-8: behavioral conformance
// runs in CI; startup performs structural validation only.
//
// Consumer packages import this suite and run it against their actual
// implementation:
//
//	func TestMyConsumer_Conformance(t *testing.T) {
//	    conformance.RunTypeAConformance(t, func() (dispatch.Consumer, func()) {
//	        c := newMyConsumer(...)
//	        return c, func() { c.Close() }
//	    })
//	}
package conformance

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ConsumerFactory creates a fresh consumer and a cleanup function.
// Called once per sub-test to ensure isolation.
type ConsumerFactory func() (dispatch.Consumer, func())

func makeEvent(t *testing.T, agent string, amount uint64) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: agent,
			ToAgent:   "bob",
			Amount:    amount,
			Currency:  "AET",
		},
		agent,
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct event: %v", err)
	}
	return e
}
