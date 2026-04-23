package main

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// Corpus is the input file structure. Version is echoed into the output so
// a diff surfaces if the corpus itself changes between runs.
type Corpus struct {
	Version string  `json:"corpus_version"`
	Entries []Entry `json:"entries"`
}

// Entry is one canonical input. Context selects which code path exercises
// the inputs; see runEntry for the dispatch.
type Entry struct {
	ID      string `json:"id"`
	Context string `json:"context"`
	Pool    uint64 `json:"pool"`

	// Recipients is used by protocolmath_direct and validator_distribution.
	Recipients []CorpusRecipient `json:"recipients,omitempty"`

	// AcceptedTaskEventID, DagTopology, and QualityFnMapping are used by
	// generation_ledger entries.
	AcceptedTaskEventID string           `json:"accepted_task_event_id,omitempty"`
	DagTopology         []DagNode        `json:"dag_topology,omitempty"`
	QualityFnMapping    map[string]int64 `json:"quality_fn_mapping,omitempty"`
}

// CorpusRecipient is one (AgentID, Q) pair. QBP may be negative — those
// are explicit test cases for the pre-clamp path.
type CorpusRecipient struct {
	AgentID string `json:"agent_id"`
	QBP     int64  `json:"q_bp"`
}

// DagNode describes one event in the stub DAG used by generation-ledger
// entries.
type DagNode struct {
	EventID    string   `json:"event_id"`
	AgentID    string   `json:"agent_id"`
	CausalRefs []string `json:"causal_refs"`
}

// Output is what the binary writes to stdout.
type Output struct {
	CorpusVersion string   `json:"corpus_version"`
	Results       []Result `json:"results"`
}

// Result is one entry's outcome. Success populates Amounts + TotalAllocated;
// failure populates Error. Never both.
type Result struct {
	EntryID        string            `json:"entry_id"`
	Context        string            `json:"context"`
	Pool           uint64            `json:"pool"`
	Amounts        map[string]uint64 `json:"amounts,omitempty"`
	TotalAllocated uint64            `json:"total_allocated,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// Run processes the entire corpus in declared order, returning the Output
// structure. Exported (lowercase package but uppercase symbol) so the
// unit test can exercise it in-process without shelling to the binary.
func Run(c *Corpus) *Output {
	out := &Output{CorpusVersion: c.Version}
	for _, e := range c.Entries {
		out.Results = append(out.Results, runEntry(e))
	}
	return out
}

// runEntry dispatches on context and wraps the per-entry logic with a
// recover so one panic cannot corrupt the whole output.
func runEntry(e Entry) (res Result) {
	res.EntryID = e.ID
	res.Context = e.Context
	res.Pool = e.Pool
	defer func() {
		if r := recover(); r != nil {
			res.Amounts = nil
			res.TotalAllocated = 0
			res.Error = fmt.Sprintf("panic: %v", r)
		}
	}()
	switch e.Context {
	case "protocolmath_direct":
		return runProtocolmathDirect(e, res)
	case "validator_distribution":
		return runValidatorDistribution(e, res)
	case "generation_ledger":
		return runGenerationLedger(e, res)
	default:
		res.Error = "unknown context: " + e.Context
		return res
	}
}

// runProtocolmathDirect calls protocolmath.Allocate with no clamping or
// pre-processing — the bare primitive.
func runProtocolmathDirect(e Entry, res Result) Result {
	pm := make([]protocolmath.Recipient, 0, len(e.Recipients))
	for _, r := range e.Recipients {
		pm = append(pm, protocolmath.Recipient{
			CanonicalKey: []byte(r.AgentID),
			Weight:       protocolmath.BasisPoints(r.QBP),
		})
	}
	result, err := protocolmath.Allocate(pm, protocolmath.MicroAET(e.Pool))
	if err != nil {
		res.Error = "allocate: " + err.Error()
		return res
	}
	res.Amounts = make(map[string]uint64, len(result))
	var total uint64
	for k, v := range result {
		amt := uint64(v)
		res.Amounts[k] = amt
		total += amt
	}
	res.TotalAllocated = total
	return res
}

// runValidatorDistribution invokes the real settler integer path via the
// exported test helper settlement.ComputeValidatorPayoutsIntegerForTest.
// This is the artifact Part F will cite when asked how confident we are
// that amd64 and arm64 agree on validator fee splits: the corpus binary
// and the production settler run the same function.
func runValidatorDistribution(e Entry, res Result) Result {
	mapping := make(map[crypto.AgentID]protocolmath.BasisPoints, len(e.Recipients))
	recipients := make([]crypto.AgentID, 0, len(e.Recipients))
	for _, r := range e.Recipients {
		id := crypto.AgentID(r.AgentID)
		mapping[id] = protocolmath.BasisPoints(r.QBP)
		recipients = append(recipients, id)
	}
	qFn := settlement.ValidatorQScoreFn(
		func(v crypto.AgentID, _ string, _ string) protocolmath.BasisPoints {
			return mapping[v]
		})
	result := settlement.ComputeValidatorPayoutsIntegerForTest(
		qFn, recipients, e.Pool, "research")
	res.Amounts = make(map[string]uint64, len(result))
	var total uint64
	for k, v := range result {
		res.Amounts[string(k)] = v
		total += v
	}
	res.TotalAllocated = total
	return res
}

// stubDAG satisfies settlement.DAGAncestorReader from a hardcoded map.
// The corpus entry's DagTopology declares the topology; stubDAG.Get
// returns the same *event.Event each time for a given EventID.
type stubDAG struct {
	events map[event.EventID]*event.Event
}

func (d *stubDAG) Get(id event.EventID) (*event.Event, error) {
	if e, ok := d.events[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("event not found: %s", id)
}

// runGenerationLedger constructs a stub DAG from the corpus entry and
// calls the real settlement.GenerationLedgerCalculator with
// shadowMode=false — which makes the integer path canonical. That means
// Calculate() returns the integer distribution; no duplication.
func runGenerationLedger(e Entry, res Result) Result {
	events := make(map[event.EventID]*event.Event, len(e.DagTopology))
	for _, n := range e.DagTopology {
		refs := make([]event.EventID, 0, len(n.CausalRefs))
		for _, r := range n.CausalRefs {
			refs = append(refs, event.EventID(r))
		}
		events[event.EventID(n.EventID)] = &event.Event{
			ID:         event.EventID(n.EventID),
			AgentID:    n.AgentID,
			CausalRefs: refs,
		}
	}
	dag := &stubDAG{events: events}
	qualities := e.QualityFnMapping
	qFn := func(id event.EventID) protocolmath.BasisPoints {
		if q, ok := qualities[string(id)]; ok {
			return protocolmath.BasisPoints(q)
		}
		return protocolmath.NeutralBP
	}
	calc := settlement.NewGenerationLedgerCalculator(dag, qFn, false) // integer path canonical
	dist := calc.Calculate(event.EventID(e.AcceptedTaskEventID), e.Pool)
	res.Amounts = make(map[string]uint64, len(dist.Recipients)+1)
	var total uint64
	for _, r := range dist.Recipients {
		res.Amounts[string(r.EventID)] = r.Amount
		total += r.Amount
	}
	if dist.Treasury > 0 {
		res.Amounts["treasury"] = dist.Treasury
		total += dist.Treasury
	}
	res.TotalAllocated = total
	return res
}
