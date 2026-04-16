package dispatch

// Deferral threshold constants per §5.4 of the locked F3-B design.
// Units are canonical epochs from the round counter primitive (D-7).
const (
	DeferralComplaintThreshold uint64 = 30
	DeferralFailoverThreshold  uint64 = 100
)
