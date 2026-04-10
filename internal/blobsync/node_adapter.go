package blobsync

// NodePeerSender wraps a network.Node to satisfy the PeerSender interface,
// avoiding a direct import of blobsync from network (which would create a
// circular dependency).
//
// Usage in cmd/node/main.go:
//
//	adapter := blobsync.NewNodePeerSender(node)
//	transport := blobsync.NewBlobTransport(adapter, reputation)
type NodePeerSender struct {
	sendToPeer   func(peerID string, msgType string, payload []byte) error
	broadcastMsg func(msgType string, payload []byte, fanout int) ([]string, error)
	localPeerID  string
}

// NewNodePeerSender creates a PeerSender backed by callback functions.
// The caller wires these to Node.SendToPeerByID and Node.BroadcastToN.
func NewNodePeerSender(
	localPeerID string,
	sendToPeer func(peerID string, msgType string, payload []byte) error,
	broadcastMsg func(msgType string, payload []byte, fanout int) ([]string, error),
) *NodePeerSender {
	return &NodePeerSender{
		localPeerID:  localPeerID,
		sendToPeer:   sendToPeer,
		broadcastMsg: broadcastMsg,
	}
}

func (a *NodePeerSender) SendToPeer(peerID string, msgType string, payload []byte) error {
	return a.sendToPeer(peerID, msgType, payload)
}

func (a *NodePeerSender) BroadcastMessage(msgType string, payload []byte, fanout int) ([]string, error) {
	return a.broadcastMsg(msgType, payload, fanout)
}

func (a *NodePeerSender) LocalPeerID() string {
	return a.localPeerID
}
