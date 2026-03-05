package metrics

// AetherNetMetrics holds all protocol-level metrics for an AetherNet node.
type AetherNetMetrics struct {
	// Transactions
	TransactionsTotal    *Counter // total transactions submitted
	TransactionsSettled  *Counter // total transactions settled
	TransactionsReversed *Counter // total transactions reversed
	TransactionVolume    *Counter // total AET volume transacted

	// Fees
	FeesCollected  *Counter // total fees collected
	FeesBurned     *Counter // total fees burned
	FeesToTreasury *Counter // total fees to treasury

	// Agents
	AgentsRegistered *Gauge // current registered agent count
	AgentsActive     *Gauge // agents active in last 24h

	// Staking
	TotalStaked *Gauge   // total AET staked across all agents
	SlashEvents *Counter // total slash events

	// DAG
	DAGSize   *Gauge // total events in DAG
	DAGTips   *Gauge // current tip count
	PeerCount *Gauge // connected peers

	// API
	APIRequests      *Counter   // total API requests
	APIErrors        *Counter   // total API errors (4xx + 5xx)
	APILatency       *Histogram // request latency in milliseconds
	RateLimitRejects *Counter   // total rate limit rejections

	// WebSocket
	WSConnections *Gauge // current WebSocket connections

	// Node
	UptimeSeconds *Gauge // node uptime in seconds
}

// NewAetherNetMetrics registers all protocol metrics with reg and returns the
// populated struct. reg must be non-nil.
func NewAetherNetMetrics(reg *Registry) *AetherNetMetrics {
	latencyBuckets := []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 5000}

	return &AetherNetMetrics{
		TransactionsTotal:    reg.Counter("aethernet_transactions_total", "Total transactions submitted"),
		TransactionsSettled:  reg.Counter("aethernet_transactions_settled_total", "Total transactions settled"),
		TransactionsReversed: reg.Counter("aethernet_transactions_reversed_total", "Total transactions reversed"),
		TransactionVolume:    reg.Counter("aethernet_transaction_volume_aet", "Total AET volume transacted"),
		FeesCollected:        reg.Counter("aethernet_fees_collected_aet", "Total fees collected in AET"),
		FeesBurned:           reg.Counter("aethernet_fees_burned_aet", "Total fees burned in AET"),
		FeesToTreasury:       reg.Counter("aethernet_fees_treasury_aet", "Total fees sent to treasury"),
		AgentsRegistered:     reg.Gauge("aethernet_agents_registered", "Current registered agent count"),
		AgentsActive:         reg.Gauge("aethernet_agents_active_24h", "Agents active in last 24 hours"),
		TotalStaked:          reg.Gauge("aethernet_total_staked_aet", "Total AET staked"),
		SlashEvents:          reg.Counter("aethernet_slash_events_total", "Total slash events"),
		DAGSize:              reg.Gauge("aethernet_dag_size", "Total events in DAG"),
		DAGTips:              reg.Gauge("aethernet_dag_tips", "Current DAG tip count"),
		PeerCount:            reg.Gauge("aethernet_peer_count", "Connected peer count"),
		APIRequests:          reg.Counter("aethernet_api_requests_total", "Total API requests"),
		APIErrors:            reg.Counter("aethernet_api_errors_total", "Total API errors"),
		APILatency:           reg.Histogram("aethernet_api_latency_ms", "API request latency in milliseconds", latencyBuckets),
		RateLimitRejects:     reg.Counter("aethernet_rate_limit_rejects_total", "Total rate limit rejections"),
		WSConnections:        reg.Gauge("aethernet_ws_connections", "Current WebSocket connections"),
		UptimeSeconds:        reg.Gauge("aethernet_uptime_seconds", "Node uptime in seconds"),
	}
}
