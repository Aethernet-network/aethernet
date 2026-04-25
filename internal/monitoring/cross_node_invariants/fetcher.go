package cross_node_invariants

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SnapshotEndpointPath is the conventional HTTP path the production
// SnapshotFetcher queries on each peer to retrieve a LedgerSnapshot.
//
// Endpoint shipped in F5 5B Phase 1.3 (criterion 12 prep) at
// internal/api/admin_handlers.go's handleAdminLedgerSnapshot. Available
// when the node is started with --enable-admin-api. Production-deploy
// surface should also gate via network ACL since the snapshot exposes
// per-agent balances + per-task escrow residuals.
const SnapshotEndpointPath = "/v1/admin/ledger-snapshot"

// HTTPFetcher is the production SnapshotFetcher. It issues a GET to
// SnapshotEndpointPath on each peer and decodes the JSON response into
// a LedgerSnapshot.
//
// PeerScheme defaults to "http://" if empty; production deployments
// behind TLS-terminating ALBs may pass "https://". A custom Client
// allows test wiring or per-deployment timeout tuning.
type HTTPFetcher struct {
	Client     *http.Client
	PeerScheme string
}

// NewHTTPFetcher constructs an HTTPFetcher with sensible defaults: 5s
// total timeout per request, http:// scheme.
func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{
		Client:     &http.Client{Timeout: 5 * time.Second},
		PeerScheme: "http://",
	}
}

// Fetch GETs the snapshot endpoint on peerAddr. peerAddr may be either
// "host:port" (PeerScheme is prepended) or a full URL. ctx cancellation
// is honored.
func (h *HTTPFetcher) Fetch(ctx context.Context, peerAddr string) (*LedgerSnapshot, error) {
	url := peerAddr
	if !strings.Contains(peerAddr, "://") {
		scheme := h.PeerScheme
		if scheme == "" {
			scheme = "http://"
		}
		url = scheme + peerAddr
	}
	url += SnapshotEndpointPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cross_node_invariants: build request for %s: %w", peerAddr, err)
	}

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cross_node_invariants: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Truncate body for log friendliness.
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("cross_node_invariants: %s returned status %d: %s",
			url, resp.StatusCode, preview)
	}

	var snap LedgerSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("cross_node_invariants: decode snapshot from %s: %w", url, err)
	}
	if snap.NodeID == "" {
		// Use the peer address as a fallback node identifier so reports
		// remain attributable even if the endpoint omits the field.
		snap.NodeID = peerAddr
	}
	return &snap, nil
}

// StaticPeerSource is a trivial PeerSource backed by a fixed slice.
// Useful for the CLI (which receives peer addresses on the command
// line or from environment) and for tests. Production wiring uses an
// adapter over internal/network's discovery state.
type StaticPeerSource struct {
	Addrs []string
}

// Peers returns a copy of the configured addresses to honor the
// PeerSource contract (callers should treat the returned slice as
// read-only, and we copy defensively to avoid surprise mutation).
func (s *StaticPeerSource) Peers() []string {
	out := make([]string, len(s.Addrs))
	copy(out, s.Addrs)
	return out
}
