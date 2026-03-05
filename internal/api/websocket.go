package api

import (
	"net/http"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/eventbus"
	"golang.org/x/net/websocket"
)

// wsHandler returns an http.Handler that upgrades HTTP connections to WebSocket
// and streams node events from the event bus.
//
// An optional ?filter=type1,type2 query parameter restricts which event types
// are delivered (comma-separated, no spaces). An absent or empty filter delivers
// all event types.
//
// Example: GET /v1/ws?filter=transfer,generation
func (s *Server) wsHandler() http.Handler {
	return websocket.Handler(s.handleWebSocket)
}

// handleWebSocket is the WebSocket connection handler. It subscribes to the
// event bus and streams matching events as JSON until the client disconnects
// or the server shuts down.
func (s *Server) handleWebSocket(conn *websocket.Conn) {
	defer conn.Close()

	if s.eventBus == nil {
		return
	}

	// Parse optional comma-separated event type filter from the request URL.
	var filter []eventbus.EventType
	if fp := conn.Request().URL.Query().Get("filter"); fp != "" {
		for _, f := range strings.Split(fp, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				filter = append(filter, eventbus.EventType(f))
			}
		}
	}

	sub := s.eventBus.Subscribe(filter, 64)
	defer s.eventBus.Unsubscribe(sub)

	// Goroutine reads (and discards) any incoming client frames.
	// When the client disconnects, the read returns an error and connClosed is closed.
	connClosed := make(chan struct{})
	go func() {
		defer close(connClosed)
		var discard any
		for {
			if err := websocket.JSON.Receive(conn, &discard); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-connClosed:
			// Client disconnected.
			return
		case evt, ok := <-sub.Ch:
			if !ok {
				// Bus closed the subscription (Unsubscribe was called).
				return
			}
			if err := websocket.JSON.Send(conn, evt); err != nil {
				// Write failed — client likely disconnected.
				return
			}
		}
	}
}
