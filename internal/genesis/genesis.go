// Package genesis defines the fixed AetherNet token supply and genesis allocation buckets.
//
// All amounts are in micro-AET (1 AET = 1,000,000 micro-AET). The total supply
// is 1,000,000,000 AET expressed as 1,000,000,000,000,000 micro-AET. Allocations are
// split across six protocol-controlled accounts seeded at chain launch.
package genesis

// Token supply in micro-AET. All constants use the same base unit.
const (
	// TotalSupply is the fixed token supply: 1 quadrillion micro-AET (= 1 billion AET).
	TotalSupply uint64 = 1_000_000_000_000_000

	// FoundersAllocation is 15% of TotalSupply.
	FoundersAllocation uint64 = 150_000_000_000_000

	// InvestorsAllocation is 15% of TotalSupply.
	InvestorsAllocation uint64 = 150_000_000_000_000

	// EcosystemAllocation is 30% of TotalSupply; funds grants, onboarding, and growth.
	EcosystemAllocation uint64 = 300_000_000_000_000

	// NetworkRewards is 20% of TotalSupply; distributed to validators and verification agents.
	NetworkRewards uint64 = 200_000_000_000_000

	// TreasuryAllocation is 10% of TotalSupply; controlled by protocol governance.
	TreasuryAllocation uint64 = 100_000_000_000_000

	// PublicAllocation is 10% of TotalSupply; available via public sale or airdrops.
	PublicAllocation uint64 = 100_000_000_000_000
)

// Genesis account names. These are used as AgentIDs in the TransferLedger to
// represent protocol-controlled allocation pools.
const (
	BucketFounders  = "genesis:founders"
	BucketInvestors = "genesis:investors"
	BucketEcosystem = "genesis:ecosystem"
	BucketRewards   = "genesis:rewards"
	BucketTreasury  = "genesis:treasury"
	BucketPublic    = "genesis:public"
)

// Onboarding constants.
const (
	// OnboardingPoolTotal is the maximum total micro-AET distributed to new agents
	// across all four onboarding tiers: 1k×50B + 9k×10B + 90k×1B + 700k×100M = 300T.
	// Funded from the EcosystemAllocation bucket; must not exceed EcosystemAllocation.
	OnboardingPoolTotal uint64 = 300_000_000_000_000

	// OnboardingMaxAgents is the hard cap on the number of agents eligible for grants.
	// Tier 4 covers agents 100 000 – 799 999, so total pool = 300T = EcosystemAllocation.
	OnboardingMaxAgents uint64 = 800_000
)
