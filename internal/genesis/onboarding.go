package genesis

// OnboardingAllocation returns the AET grant (in micro-AET) for the agent that
// would be onboarded when agentCount agents have already been registered.
//
// The curve declines in four tiers, rewarding early network participants more
// generously and closing automatically once OnboardingMaxAgents is reached:
//
//	agents 1 – 1 000       50 000 000 micro-AET  ( 50 AET each)
//	agents 1 001 – 10 000  10 000 000 micro-AET  ( 10 AET each)
//	agents 10 001 – 100 000 1 000 000 micro-AET  (  1 AET each)
//	agents 100 001 – 1 M      100 000 micro-AET  (0.1 AET each)
//	agents > 1 M                    0            (onboarding closed)
func OnboardingAllocation(agentCount uint64) uint64 {
	switch {
	case agentCount < 1_000:
		return 50_000_000
	case agentCount < 10_000:
		return 10_000_000
	case agentCount < 100_000:
		return 1_000_000
	case agentCount < OnboardingMaxAgents:
		return 100_000
	default:
		return 0
	}
}
