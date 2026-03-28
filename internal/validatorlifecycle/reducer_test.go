package validatorlifecycle

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// joinEvent creates a genesis join (exempt from MinBondedStake) for state
// machine tests. Use nonGenesisJoinEvent to test stake thresholds.
func joinEvent(eventID string, key crypto.AgentID, stake uint64, causalTS uint64) LifecycleEvent {
	return LifecycleEvent{
		Kind:        EventJoin,
		EventID:     eventID,
		CausalTS:    causalTS,
		SeatID:      DeriveValidatorID(eventID),
		OperatorKey: key,
		StakeAmount: stake,
		IsGenesis:   true,
	}
}

func genesisJoinEvent(eventID string, key crypto.AgentID, stake uint64) LifecycleEvent {
	return joinEvent(eventID, key, stake, 0)
}

func nonGenesisJoinEvent(eventID string, key crypto.AgentID, stake uint64, causalTS uint64) LifecycleEvent {
	return LifecycleEvent{
		Kind:        EventJoin,
		EventID:     eventID,
		CausalTS:    causalTS,
		SeatID:      DeriveValidatorID(eventID),
		OperatorKey: key,
		StakeAmount: stake,
		IsGenesis:   false,
	}
}

func seatEvent(kind LifecycleEventKind, seatID ValidatorID, eventID string, causalTS uint64) LifecycleEvent {
	return LifecycleEvent{
		Kind:     kind,
		EventID:  eventID,
		CausalTS: causalTS,
		SeatID:   seatID,
	}
}

// ---------------------------------------------------------------------------
// Initial state
// ---------------------------------------------------------------------------

func TestReducer_Empty(t *testing.T) {
	r := NewReducer()
	if r.SeatCount() != 0 {
		t.Fatalf("new reducer should have 0 seats, got %d", r.SeatCount())
	}
	if r.Version() != 0 {
		t.Fatalf("new reducer should have version 0, got %d", r.Version())
	}
}

// ---------------------------------------------------------------------------
// Join
// ---------------------------------------------------------------------------

func TestReducer_Join(t *testing.T) {
	r := NewReducer()
	ev := joinEvent("join-1", "key-aaa", 50_000, 10)
	if err := r.Apply(ev); err != nil {
		t.Fatalf("join: %v", err)
	}
	if r.SeatCount() != 1 {
		t.Fatalf("expected 1 seat, got %d", r.SeatCount())
	}
	if r.Version() != 1 {
		t.Fatalf("expected version 1, got %d", r.Version())
	}

	seat, err := r.Seat(ev.SeatID)
	if err != nil {
		t.Fatalf("Seat: %v", err)
	}
	if seat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", seat.Status)
	}
	if seat.OperatorKey != "key-aaa" {
		t.Fatalf("expected key-aaa, got %s", seat.OperatorKey)
	}
	if seat.StakeAmount != 50_000 {
		t.Fatalf("expected stake 50000, got %d", seat.StakeAmount)
	}
	if len(seat.KeyHistory) != 1 || seat.KeyHistory[0].Epoch != 1 {
		t.Fatalf("expected 1 key epoch at epoch 1, got %v", seat.KeyHistory)
	}
}

func TestReducer_Join_MissingKey(t *testing.T) {
	r := NewReducer()
	ev := LifecycleEvent{Kind: EventJoin, EventID: "j1", CausalTS: 1}
	if err := r.Apply(ev); !errors.Is(err, ErrMissingOperatorKey) {
		t.Fatalf("expected ErrMissingOperatorKey, got %v", err)
	}
}

func TestReducer_Join_Duplicate(t *testing.T) {
	r := NewReducer()
	ev := joinEvent("j1", "key-1", 1000, 1)
	_ = r.Apply(ev)
	if err := r.Apply(ev); !errors.Is(err, ErrSeatAlreadyExists) {
		t.Fatalf("expected ErrSeatAlreadyExists, got %v", err)
	}
}

func TestReducer_Join_Genesis(t *testing.T) {
	r := NewReducer()
	ev := genesisJoinEvent("genesis-1", "key-g", 100_000)
	if err := r.Apply(ev); err != nil {
		t.Fatalf("genesis join: %v", err)
	}
	seat, _ := r.Seat(ev.SeatID)
	if !seat.IsGenesis {
		t.Fatal("expected IsGenesis=true")
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle: Join → Activate → CoolDown → Exit
// ---------------------------------------------------------------------------

func TestReducer_FullLifecycle(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-lifecycle", "key-lc", 100_000, 1)
	seatID := joinEv.SeatID

	// Join
	if err := r.Apply(joinEv); err != nil {
		t.Fatalf("join: %v", err)
	}

	// PendingJoin → Probationary
	if err := r.Apply(seatEvent(EventActivate, seatID, "act-1", 5)); err == nil || !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("PendingJoin → Active should fail, got: %v", err)
	}
	// PendingJoin → Active is disallowed. The lifecycle for PendingJoin
	// requires a future "enter probation" event kind. For the rest of the
	// lifecycle test, we start from Probationary directly.

	// For the full lifecycle test, start from Probationary directly.
	// Manually set status to Probationary to test the rest of the chain.
	r2 := NewReducer()
	joinEv2 := joinEvent("join-lc2", "key-lc2", 100_000, 1)
	seatID2 := joinEv2.SeatID
	_ = r2.Apply(joinEv2)
	// Transition PendingJoin → Probationary by directly mutating (test helper).
	r2.seats[seatID2].Status = SeatProbationary
	r2.seats[seatID2].Weight = 50_000 // probationary weight

	// Probationary → Active
	if err := r2.Apply(seatEvent(EventActivate, seatID2, "act-2", 10)); err != nil {
		t.Fatalf("Probationary → Active: %v", err)
	}
	seat, _ := r2.Seat(seatID2)
	if seat.Status != SeatActive {
		t.Fatalf("expected Active, got %s", seat.Status)
	}
	if seat.Weight != 100_000 {
		t.Fatalf("expected full weight 100000, got %d", seat.Weight)
	}
	if seat.ActivatedCausalTS != 10 {
		t.Fatalf("expected ActivatedCausalTS=10, got %d", seat.ActivatedCausalTS)
	}

	// Active → CoolingDown
	cdEv := LifecycleEvent{
		Kind:             EventBeginCooldown,
		EventID:          "cd-1",
		CausalTS:         20,
		SeatID:           seatID2,
		CooldownDuration: 50,
	}
	if err := r2.Apply(cdEv); err != nil {
		t.Fatalf("Active → CoolingDown: %v", err)
	}
	seat, _ = r2.Seat(seatID2)
	if seat.Status != SeatCoolingDown {
		t.Fatalf("expected CoolingDown, got %s", seat.Status)
	}
	if seat.Weight != 0 {
		t.Fatalf("CoolingDown weight should be 0, got %d", seat.Weight)
	}

	// Exit too early
	earlyExit := seatEvent(EventExit, seatID2, "exit-early", 30) // only 10 elapsed, need 50
	if err := r2.Apply(earlyExit); !errors.Is(err, ErrCooldownNotElapsed) {
		t.Fatalf("expected ErrCooldownNotElapsed, got %v", err)
	}

	// CoolingDown → Exited (after cooldown)
	exitEv := seatEvent(EventExit, seatID2, "exit-1", 75) // 75-20=55 >= 50
	if err := r2.Apply(exitEv); err != nil {
		t.Fatalf("CoolingDown → Exited: %v", err)
	}
	seat, _ = r2.Seat(seatID2)
	if seat.Status != SeatExited {
		t.Fatalf("expected Exited, got %s", seat.Status)
	}
}

// ---------------------------------------------------------------------------
// Re-entry
// ---------------------------------------------------------------------------

func TestReducer_ReEntry(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-re", "key-old", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)

	// Fast-track to Exited for test purposes.
	r.seats[seatID].Status = SeatExited
	r.seats[seatID].EffectiveFromVersion = r.version

	// Re-entry with sufficient stake.
	reJoin := LifecycleEvent{
		Kind:        EventJoin,
		EventID:     "rejoin-1",
		CausalTS:    100,
		SeatID:      seatID,
		OperatorKey: "key-new",
		StakeAmount: MinBondedStake,
	}
	if err := r.Apply(reJoin); err != nil {
		t.Fatalf("re-entry: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin after re-entry, got %s", seat.Status)
	}
	if seat.OperatorKey != "key-new" {
		t.Fatalf("expected key-new, got %s", seat.OperatorKey)
	}
	if seat.StakeAmount != MinBondedStake {
		t.Fatalf("expected stake %d, got %d", MinBondedStake, seat.StakeAmount)
	}
	// Key history should have 2 entries.
	if len(seat.KeyHistory) != 2 {
		t.Fatalf("expected 2 key epochs after re-entry, got %d", len(seat.KeyHistory))
	}
}

// ---------------------------------------------------------------------------
// Suspension and reinstatement
// ---------------------------------------------------------------------------

func TestReducer_SuspendAndReinstate(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-sr", "key-sr", 100_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].Weight = 100_000
	r.seats[seatID].EffectiveFromVersion = r.version

	// Suspend
	suspEv := LifecycleEvent{
		Kind:     EventSuspend,
		EventID:  "susp-1",
		CausalTS: 10,
		SeatID:   seatID,
		Reason:   "stake deficit",
	}
	if err := r.Apply(suspEv); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.Status != SeatSuspended {
		t.Fatalf("expected Suspended, got %s", seat.Status)
	}
	if seat.SuspensionReason != "stake deficit" {
		t.Fatalf("expected reason 'stake deficit', got %q", seat.SuspensionReason)
	}
	if seat.Weight != 0 {
		t.Fatalf("suspended weight should be 0, got %d", seat.Weight)
	}

	// Reinstate — returns to Probationary, not Active.
	if err := r.Apply(seatEvent(EventReinstate, seatID, "rein-1", 20)); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	seat, _ = r.Seat(seatID)
	if seat.Status != SeatProbationary {
		t.Fatalf("expected Probationary after reinstate (must re-earn Active), got %s", seat.Status)
	}
	if seat.SuspensionReason != "" {
		t.Fatalf("suspension reason should be cleared, got %q", seat.SuspensionReason)
	}
	if seat.Weight != 50_000 { // probationary: half weight
		t.Fatalf("expected probationary weight 50000, got %d", seat.Weight)
	}
}

// ---------------------------------------------------------------------------
// Exclusion (slashing)
// ---------------------------------------------------------------------------

func TestReducer_Exclude(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-ex", "key-ex", 100_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version
	r.seats[seatID].Weight = 100_000

	exEv := LifecycleEvent{
		Kind:     EventExclude,
		EventID:  "slash-1",
		CausalTS: 15,
		SeatID:   seatID,
		Reason:   "fraudulent_approval",
	}
	if err := r.Apply(exEv); err != nil {
		t.Fatalf("exclude: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.Status != SeatExcluded {
		t.Fatalf("expected Excluded, got %s", seat.Status)
	}
	if seat.SlashCount != 1 {
		t.Fatalf("expected SlashCount=1, got %d", seat.SlashCount)
	}
	if seat.LastSlashEventID != "slash-1" {
		t.Fatalf("expected LastSlashEventID=slash-1, got %s", seat.LastSlashEventID)
	}

	// No further transitions from Excluded.
	if err := r.Apply(seatEvent(EventActivate, seatID, "nope", 20)); err == nil {
		t.Fatal("should not allow transitions from Excluded")
	}
}

func TestReducer_Exclude_Idempotent(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-idem", "key-id", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version

	exEv := LifecycleEvent{Kind: EventExclude, EventID: "sl-1", CausalTS: 5, SeatID: seatID, Reason: "collusion"}
	_ = r.Apply(exEv)
	// Apply again — should be idempotent.
	if err := r.Apply(exEv); err != nil {
		t.Fatalf("idempotent exclude should not fail: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Key rotation
// ---------------------------------------------------------------------------

func TestReducer_RotateKey(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-kr", "key-old", 100_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version

	rotEv := LifecycleEvent{
		Kind:        EventRotateKey,
		EventID:     "rot-1",
		CausalTS:    20,
		SeatID:      seatID,
		OperatorKey: "key-new",
	}
	if err := r.Apply(rotEv); err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.OperatorKey != "key-new" {
		t.Fatalf("expected key-new, got %s", seat.OperatorKey)
	}
	if len(seat.KeyHistory) != 2 {
		t.Fatalf("expected 2 key epochs, got %d", len(seat.KeyHistory))
	}
	if seat.KeyHistory[1].Epoch != 2 {
		t.Fatalf("expected epoch 2, got %d", seat.KeyHistory[1].Epoch)
	}
}

func TestReducer_RotateKey_SameKey(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-kr2", "key-same", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version

	rotEv := LifecycleEvent{
		Kind:        EventRotateKey,
		EventID:     "rot-dup",
		CausalTS:    10,
		SeatID:      seatID,
		OperatorKey: "key-same",
	}
	if err := r.Apply(rotEv); !errors.Is(err, ErrKeyEpochRegression) {
		t.Fatalf("expected ErrKeyEpochRegression, got %v", err)
	}
}

func TestReducer_RotateKey_Terminal(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-kr3", "key-x", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatExcluded
	r.seats[seatID].EffectiveFromVersion = r.version

	rotEv := LifecycleEvent{
		Kind:        EventRotateKey,
		EventID:     "rot-term",
		CausalTS:    10,
		SeatID:      seatID,
		OperatorKey: "key-y",
	}
	if err := r.Apply(rotEv); !errors.Is(err, ErrTerminalSeat) {
		t.Fatalf("expected ErrTerminalSeat, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stake update
// ---------------------------------------------------------------------------

func TestReducer_StakeUpdate(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-su", "key-su", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version
	r.seats[seatID].Weight = 50_000

	suEv := LifecycleEvent{
		Kind:        EventStakeUpdate,
		EventID:     "su-1",
		CausalTS:    10,
		SeatID:      seatID,
		StakeAmount: 200_000,
	}
	if err := r.Apply(suEv); err != nil {
		t.Fatalf("stake update: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.StakeAmount != 200_000 {
		t.Fatalf("expected stake 200000, got %d", seat.StakeAmount)
	}
	if seat.Weight != 200_000 {
		t.Fatalf("expected weight 200000, got %d", seat.Weight)
	}
}

func TestReducer_StakeUpdate_NonParticipating(t *testing.T) {
	r := NewReducer()
	joinEv := joinEvent("join-su2", "key-su2", 50_000, 1)
	seatID := joinEv.SeatID
	_ = r.Apply(joinEv)
	r.seats[seatID].Status = SeatSuspended
	r.seats[seatID].EffectiveFromVersion = r.version

	suEv := LifecycleEvent{
		Kind:        EventStakeUpdate,
		EventID:     "su-2",
		CausalTS:    10,
		SeatID:      seatID,
		StakeAmount: 100_000,
	}
	if err := r.Apply(suEv); err != nil {
		t.Fatalf("stake update on suspended seat: %v", err)
	}
	seat, _ := r.Seat(seatID)
	if seat.StakeAmount != 100_000 {
		t.Fatalf("expected stake 100000, got %d", seat.StakeAmount)
	}
	if seat.Weight != 0 {
		t.Fatalf("suspended seat should have weight 0 after stake update, got %d", seat.Weight)
	}
}

// ---------------------------------------------------------------------------
// Unsupported event
// ---------------------------------------------------------------------------

func TestReducer_UnsupportedEvent(t *testing.T) {
	r := NewReducer()
	ev := LifecycleEvent{Kind: "nonexistent_event", EventID: "x", SeatID: "s"}
	if err := r.Apply(ev); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("expected ErrUnsupportedEvent, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SeatByOperatorKey
// ---------------------------------------------------------------------------

func TestReducer_SeatByOperatorKey(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "key-1", 10000, 1))
	_ = r.Apply(joinEvent("j2", "key-2", 20000, 2))

	seat, err := r.SeatByOperatorKey("key-2")
	if err != nil {
		t.Fatalf("SeatByOperatorKey: %v", err)
	}
	if seat.OperatorKey != "key-2" {
		t.Fatalf("expected key-2, got %s", seat.OperatorKey)
	}

	_, err = r.SeatByOperatorKey("key-nonexistent")
	if !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("expected ErrSeatNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Version increment
// ---------------------------------------------------------------------------

func TestReducer_VersionIncrements(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 1000, 1))
	if r.Version() != 1 {
		t.Fatalf("expected version 1 after join, got %d", r.Version())
	}
	_ = r.Apply(joinEvent("j2", "k2", 2000, 2))
	if r.Version() != 2 {
		t.Fatalf("expected version 2 after second join, got %d", r.Version())
	}

	seatID := DeriveValidatorID("j1")
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].EffectiveFromVersion = r.version
	_ = r.Apply(seatEvent(EventSuspend, seatID, "s1", 5))
	if r.Version() != 3 {
		t.Fatalf("expected version 3 after suspend, got %d", r.Version())
	}
}

// ---------------------------------------------------------------------------
// Future-snapshot effect rules
// ---------------------------------------------------------------------------

func TestReducer_Join_EffectiveNextSnapshot(t *testing.T) {
	// A joining seat should NOT be participating in a snapshot taken at the
	// version before the join.
	// Seed a genesis seat so we have a base snapshot.
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "genesis-1", OperatorAgentID: "g-key", ConsensusPublicKey: "g-key",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r2, _ := SeedReducerFromManifest(m)
	preJoinVersion := r2.Version()
	preJoinSnap := r2.Snapshot()

	// Now a new validator joins.
	_ = r2.Apply(joinEvent("new-joiner", "new-key", 50_000, 100))
	newSeatID := DeriveValidatorID("new-joiner")
	postJoinSnap := r2.Snapshot()

	// The new seat should NOT be in the pre-join snapshot's participating set.
	for _, seat := range preJoinSnap.ParticipatingSeats() {
		if seat.ID == newSeatID {
			t.Fatal("joining seat should NOT be in pre-join snapshot")
		}
	}

	// The new seat should appear in the post-join snapshot (as PendingJoin,
	// not participating yet — PendingJoin is not a participating status).
	postSeat, ok := postJoinSnap.Seats[newSeatID]
	if !ok {
		t.Fatal("new seat should be in post-join snapshot")
	}
	if postSeat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", postSeat.Status)
	}
	if postSeat.EffectiveFromVersion <= preJoinVersion {
		t.Fatalf("EffectiveFromVersion should be > pre-join version %d, got %d",
			preJoinVersion, postSeat.EffectiveFromVersion)
	}
}

func TestReducer_Activate_EffectiveNextSnapshot(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-act", OperatorAgentID: "k-act", ConsensusPublicKey: "k-act",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatProbationary},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	preActivateVersion := r.Version()
	preActivateSnap := r.Snapshot()

	// Activate the probationary seat.
	_ = r.Apply(seatEvent(EventActivate, "v-act", "act-1", 50))
	postActivateSnap := r.Snapshot()

	// Pre-activate snapshot: seat is probationary (participating at half weight).
	preSeat := preActivateSnap.Seats["v-act"]
	if preSeat.Status != SeatProbationary {
		t.Fatalf("expected Probationary in pre-activate snapshot, got %s", preSeat.Status)
	}

	// Post-activate snapshot: seat is active with full weight.
	postSeat := postActivateSnap.Seats["v-act"]
	if postSeat.Status != SeatActive {
		t.Fatalf("expected Active in post-activate snapshot, got %s", postSeat.Status)
	}
	if postSeat.EffectiveFromVersion <= preActivateVersion {
		t.Fatalf("activation EffectiveFromVersion should be > %d, got %d",
			preActivateVersion, postSeat.EffectiveFromVersion)
	}
}

func TestReducer_Exit_DoesNotCorruptOldRounds(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-exit", OperatorAgentID: "k-exit", ConsensusPublicKey: "k-exit",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	// Take a snapshot while the seat is active — this represents an open round.
	activeSnap := r.Snapshot()

	// Begin cooldown and exit.
	_ = r.Apply(LifecycleEvent{
		Kind: EventBeginCooldown, EventID: "cd-1", CausalTS: 50,
		SeatID: "v-exit", CooldownDuration: 10,
	})
	_ = r.Apply(seatEvent(EventExit, "v-exit", "exit-1", 100))

	// The active snapshot (taken before exit) should still show the seat
	// as eligible with its weight intact. Exiting does not retroactively
	// invalidate already-open rounds.
	w, eligible := activeSnap.VoteWeightByKey("k-exit")
	if !eligible {
		t.Fatal("exited seat should still be eligible in pre-exit snapshot")
	}
	if w != 100_000 {
		t.Fatalf("expected weight 100000 in pre-exit snapshot, got %d", w)
	}

	// The post-exit snapshot should NOT include the seat as participating.
	exitSnap := r.Snapshot()
	_, eligible = exitSnap.VoteWeightByKey("k-exit")
	if eligible {
		t.Fatal("exited seat should NOT be eligible in post-exit snapshot")
	}
}

func TestReducer_Suspend_AffectsFutureSnapshots(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-susp", OperatorAgentID: "k-susp", ConsensusPublicKey: "k-susp",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	activeSnap := r.Snapshot()

	// Suspend.
	_ = r.Apply(LifecycleEvent{
		Kind: EventSuspend, EventID: "susp-future", CausalTS: 30,
		SeatID: "v-susp", Reason: "stake deficit",
	})
	suspSnap := r.Snapshot()

	// Active snapshot: seat eligible.
	_, eligible := activeSnap.VoteWeightByKey("k-susp")
	if !eligible {
		t.Fatal("seat should be eligible in pre-suspend snapshot")
	}

	// Suspended snapshot: seat NOT eligible.
	_, eligible = suspSnap.VoteWeightByKey("k-susp")
	if eligible {
		t.Fatal("suspended seat should NOT be eligible in post-suspend snapshot")
	}
}

func TestReducer_Resume_ReturnsToProbationary(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-resume", OperatorAgentID: "k-resume", ConsensusPublicKey: "k-resume",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Suspend then reinstate.
	_ = r.Apply(LifecycleEvent{
		Kind: EventSuspend, EventID: "s1", CausalTS: 10,
		SeatID: "v-resume", Reason: "test",
	})
	_ = r.Apply(seatEvent(EventReinstate, "v-resume", "r1", 20))

	seat, _ := r.Seat("v-resume")
	if seat.Status != SeatProbationary {
		t.Fatalf("resume should return to Probationary, got %s", seat.Status)
	}
	if seat.Weight != 50_000 { // half of 100_000
		t.Fatalf("probationary weight should be half stake, got %d", seat.Weight)
	}
}

func TestReducer_Join_InsufficientStake(t *testing.T) {
	r := NewReducer()
	ev := nonGenesisJoinEvent("low-stake", "key-low", MinBondedStake-1, 1)
	err := r.Apply(ev)
	if !errors.Is(err, ErrInsufficientStake) {
		t.Fatalf("expected ErrInsufficientStake, got %v", err)
	}
}

func TestReducer_Join_ExactMinStake(t *testing.T) {
	r := NewReducer()
	ev := nonGenesisJoinEvent("exact-stake", "key-exact", MinBondedStake, 1)
	if err := r.Apply(ev); err != nil {
		t.Fatalf("exact MinBondedStake should be accepted: %v", err)
	}
}

func TestReducer_Join_GenesisExemptFromMinStake(t *testing.T) {
	r := NewReducer()
	ev := joinEvent("genesis-low", "key-gl", 1, 0) // 1 µAET — far below minimum
	if err := r.Apply(ev); err != nil {
		t.Fatalf("genesis joins should be exempt from MinBondedStake: %v", err)
	}
}

func TestReducer_EffectiveFromVersion_TrackedOnAllTransitions(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-efv", OperatorAgentID: "k-efv", ConsensusPublicKey: "k-efv",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	transitions := []struct {
		kind LifecycleEventKind
		ev   LifecycleEvent
	}{
		{EventSuspend, LifecycleEvent{Kind: EventSuspend, EventID: "t1", CausalTS: 10, SeatID: "v-efv", Reason: "test"}},
		{EventReinstate, seatEvent(EventReinstate, "v-efv", "t2", 20)},
		{EventActivate, seatEvent(EventActivate, "v-efv", "t3", 30)},
		{EventBeginCooldown, LifecycleEvent{Kind: EventBeginCooldown, EventID: "t4", CausalTS: 40, SeatID: "v-efv", CooldownDuration: 100}},
	}

	for _, tt := range transitions {
		prevVersion := r.Version()
		if err := r.Apply(tt.ev); err != nil {
			t.Fatalf("apply %s: %v", tt.kind, err)
		}
		seat, _ := r.Seat("v-efv")
		if seat.EffectiveFromVersion <= prevVersion {
			t.Fatalf("after %s: EffectiveFromVersion %d should be > %d",
				tt.kind, seat.EffectiveFromVersion, prevVersion)
		}
	}
}

// ---------------------------------------------------------------------------
// Key rotation with old-key validation
// ---------------------------------------------------------------------------

func TestReducer_RotateKey_OldKeyValidation(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-rot", OperatorAgentID: "key-original", ConsensusPublicKey: "key-original",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Rotate with correct old key.
	if err := r.Apply(LifecycleEvent{
		Kind:           EventRotateKey,
		EventID:        "rot-ok",
		CausalTS:       10,
		SeatID:         "v-rot",
		OperatorKey:    "key-new",
		OldOperatorKey: "key-original",
	}); err != nil {
		t.Fatalf("rotation with correct old key should succeed: %v", err)
	}

	seat, _ := r.Seat("v-rot")
	if seat.OperatorKey != "key-new" {
		t.Fatalf("expected key-new, got %s", seat.OperatorKey)
	}
	if len(seat.KeyHistory) != 2 {
		t.Fatalf("expected 2 key epochs, got %d", len(seat.KeyHistory))
	}
}

func TestReducer_RotateKey_OldKeyMismatch(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-rot-bad", OperatorAgentID: "key-orig", ConsensusPublicKey: "key-orig",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	err := r.Apply(LifecycleEvent{
		Kind:           EventRotateKey,
		EventID:        "rot-bad",
		CausalTS:       10,
		SeatID:         "v-rot-bad",
		OperatorKey:    "key-attacker",
		OldOperatorKey: "key-wrong", // doesn't match current
	})
	if !errors.Is(err, ErrOldKeyMismatch) {
		t.Fatalf("expected ErrOldKeyMismatch, got %v", err)
	}
}

func TestReducer_RotateKey_SeatContinuity(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-cont", OperatorAgentID: "key-v1", ConsensusPublicKey: "key-v1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Rotate twice.
	_ = r.Apply(LifecycleEvent{
		Kind: EventRotateKey, EventID: "rot-1", CausalTS: 10,
		SeatID: "v-cont", OperatorKey: "key-v2", OldOperatorKey: "key-v1",
	})
	_ = r.Apply(LifecycleEvent{
		Kind: EventRotateKey, EventID: "rot-2", CausalTS: 20,
		SeatID: "v-cont", OperatorKey: "key-v3", OldOperatorKey: "key-v2",
	})

	seat, _ := r.Seat("v-cont")
	// Seat ID is preserved across rotations.
	if seat.ID != "v-cont" {
		t.Fatalf("seat ID should be preserved: got %s", seat.ID)
	}
	if seat.OperatorKey != "key-v3" {
		t.Fatalf("expected key-v3, got %s", seat.OperatorKey)
	}
	if len(seat.KeyHistory) != 3 {
		t.Fatalf("expected 3 key epochs, got %d", len(seat.KeyHistory))
	}
	if seat.Status != SeatActive {
		t.Fatalf("status should remain Active across rotations, got %s", seat.Status)
	}
}

func TestReducer_RotateKey_OldKeyValidForOldSnapshot(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-epoch", OperatorAgentID: "key-old", ConsensusPublicKey: "key-old",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	preRotateSnap := r.Snapshot()

	// Rotate key.
	_ = r.Apply(LifecycleEvent{
		Kind: EventRotateKey, EventID: "rot-ep", CausalTS: 50,
		SeatID: "v-epoch", OperatorKey: "key-new", OldOperatorKey: "key-old",
	})
	postRotateSnap := r.Snapshot()

	// Old key valid for old snapshot.
	_, eligible := preRotateSnap.VoteWeightByKey("key-old")
	if !eligible {
		t.Fatal("old key should be eligible in pre-rotate snapshot")
	}
	_, eligible = preRotateSnap.VoteWeightByKey("key-new")
	if eligible {
		t.Fatal("new key should NOT be eligible in pre-rotate snapshot")
	}

	// New key valid for new snapshot.
	_, eligible = postRotateSnap.VoteWeightByKey("key-new")
	if !eligible {
		t.Fatal("new key should be eligible in post-rotate snapshot")
	}
	_, eligible = postRotateSnap.VoteWeightByKey("key-old")
	if eligible {
		t.Fatal("old key should NOT be eligible in post-rotate snapshot")
	}
}

// ---------------------------------------------------------------------------
// Slash integration
// ---------------------------------------------------------------------------

func TestReducer_Slash_ReducesStake(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-sl", OperatorAgentID: "k-sl", ConsensusPublicKey: "k-sl",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	_ = r.Apply(LifecycleEvent{
		Kind: EventSlash, EventID: "slash-1", CausalTS: 10,
		SeatID: "v-sl", Reason: "fraud", SlashAmount: 30_000,
	})

	seat, _ := r.Seat("v-sl")
	if seat.StakeAmount != 70_000 {
		t.Fatalf("expected stake 70000 after slash, got %d", seat.StakeAmount)
	}
	if seat.SlashCount != 1 {
		t.Fatalf("expected SlashCount=1, got %d", seat.SlashCount)
	}
	if seat.Status != SeatSuspended {
		t.Fatalf("non-permanent slash should suspend, got %s", seat.Status)
	}
}

func TestReducer_Slash_Permanent_Excludes(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-sl-perm", OperatorAgentID: "k-sp", ConsensusPublicKey: "k-sp",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	_ = r.Apply(LifecycleEvent{
		Kind: EventSlash, EventID: "slash-perm", CausalTS: 10,
		SeatID: "v-sl-perm", Reason: "severe fraud",
		SlashAmount: 100_000, PermanentExclusion: true,
	})

	seat, _ := r.Seat("v-sl-perm")
	if seat.Status != SeatExcluded {
		t.Fatalf("permanent slash should exclude, got %s", seat.Status)
	}
	if seat.StakeAmount != 0 {
		t.Fatalf("expected 0 stake after full slash, got %d", seat.StakeAmount)
	}
}

func TestReducer_Slash_ExceedsStake(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-sl-over", OperatorAgentID: "k-so", ConsensusPublicKey: "k-so",
				KeyEpoch: 1, BondedStake: 50_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	err := r.Apply(LifecycleEvent{
		Kind: EventSlash, EventID: "slash-over", CausalTS: 10,
		SeatID: "v-sl-over", Reason: "test", SlashAmount: 60_000,
	})
	if !errors.Is(err, ErrSlashExceedsStake) {
		t.Fatalf("expected ErrSlashExceedsStake, got %v", err)
	}
}

func TestReducer_Slash_WithCooldown_BlocksReinstate(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-sl-cd", OperatorAgentID: "k-cd", ConsensusPublicKey: "k-cd",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Slash with cooldown of 100 causal units.
	_ = r.Apply(LifecycleEvent{
		Kind: EventSlash, EventID: "slash-cd", CausalTS: 10,
		SeatID: "v-sl-cd", Reason: "minor offense",
		SlashAmount: 20_000, CooldownDuration: 100,
	})

	// Try to reinstate before cooldown — should fail.
	err := r.Apply(seatEvent(EventReinstate, "v-sl-cd", "rein-early", 50)) // 50-10=40 < 100
	if !errors.Is(err, ErrCooldownNotElapsed) {
		t.Fatalf("expected ErrCooldownNotElapsed, got %v", err)
	}

	// Reinstate after cooldown — should succeed.
	err = r.Apply(seatEvent(EventReinstate, "v-sl-cd", "rein-ok", 120)) // 120-10=110 >= 100
	if err != nil {
		t.Fatalf("reinstate after cooldown should succeed: %v", err)
	}

	seat, _ := r.Seat("v-sl-cd")
	if seat.Status != SeatProbationary {
		t.Fatalf("expected Probationary after reinstate, got %s", seat.Status)
	}
	if seat.CooldownDurationEvents != 0 {
		t.Fatalf("cooldown should be cleared after reinstate, got %d", seat.CooldownDurationEvents)
	}
}

func TestReducer_Slash_IneligibleInSnapshot(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-sl-snap", OperatorAgentID: "k-ss", ConsensusPublicKey: "k-ss",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	preSlashSnap := r.Snapshot()

	_ = r.Apply(LifecycleEvent{
		Kind: EventSlash, EventID: "slash-snap", CausalTS: 10,
		SeatID: "v-sl-snap", Reason: "test", SlashAmount: 30_000,
	})
	postSlashSnap := r.Snapshot()

	// Pre-slash: eligible.
	_, eligible := preSlashSnap.VoteWeightByKey("k-ss")
	if !eligible {
		t.Fatal("should be eligible in pre-slash snapshot")
	}

	// Post-slash: not eligible (suspended).
	_, eligible = postSlashSnap.VoteWeightByKey("k-ss")
	if eligible {
		t.Fatal("should NOT be eligible in post-slash snapshot")
	}
}

// ---------------------------------------------------------------------------
// Key epoch validation via eligibility
// ---------------------------------------------------------------------------

func TestValidateVoteKeyEpoch_CurrentKey(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-vke", OperatorAgentID: "key-current", ConsensusPublicKey: "key-current",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	snap := r.Snapshot()

	seatID, err := ValidateVoteKeyEpoch(snap, "key-current")
	if err != nil {
		t.Fatalf("current key should be valid: %v", err)
	}
	if seatID != "v-vke" {
		t.Fatalf("expected seat v-vke, got %s", seatID)
	}
}

func TestValidateVoteKeyEpoch_SupersededKey(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-vke2", OperatorAgentID: "key-old", ConsensusPublicKey: "key-old",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	_ = r.Apply(LifecycleEvent{
		Kind: EventRotateKey, EventID: "rot-vke", CausalTS: 50,
		SeatID: "v-vke2", OperatorKey: "key-new", OldOperatorKey: "key-old",
	})
	// Take snapshot AFTER rotation.
	postSnap := r.Snapshot()

	// New key should be valid.
	_, err := ValidateVoteKeyEpoch(postSnap, "key-new")
	if err != nil {
		t.Fatalf("new key should be valid in post-rotate snapshot: %v", err)
	}

	// Old key should be INVALID (superseded in this snapshot).
	_, err = ValidateVoteKeyEpoch(postSnap, "key-old")
	if !errors.Is(err, ErrKeyEpochMismatch) {
		t.Fatalf("expected ErrKeyEpochMismatch for superseded key, got %v", err)
	}
}

func TestValidateVoteKeyEpoch_OldKeyValidInOldSnapshot(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-vke3", OperatorAgentID: "key-orig", ConsensusPublicKey: "key-orig",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	preRotateSnap := r.Snapshot()

	_ = r.Apply(LifecycleEvent{
		Kind: EventRotateKey, EventID: "rot-vke3", CausalTS: 50,
		SeatID: "v-vke3", OperatorKey: "key-rotated", OldOperatorKey: "key-orig",
	})

	// Old key should still be valid in the pre-rotation snapshot.
	seatID, err := ValidateVoteKeyEpoch(preRotateSnap, "key-orig")
	if err != nil {
		t.Fatalf("old key should be valid in pre-rotate snapshot: %v", err)
	}
	if seatID != "v-vke3" {
		t.Fatalf("expected seat v-vke3, got %s", seatID)
	}
}
