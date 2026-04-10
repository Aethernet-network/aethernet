package blobsync

import (
	"testing"
	"time"
)

func TestHolderCache_AddAndGet(t *testing.T) {
	c := NewHolderHintCache(100)
	var hash [32]byte
	hash[0] = 1
	c.Add(HolderHint{
		BlobHash:       hash,
		HolderPeerID:   "peer-1",
		ValidUntilUnix: time.Now().Add(time.Minute).Unix(),
		Signature:      []byte("sig"),
	})
	hints := c.Get(hash)
	if len(hints) != 1 {
		t.Fatalf("got %d hints; want 1", len(hints))
	}
	if hints[0].HolderPeerID != "peer-1" {
		t.Errorf("peer = %s; want peer-1", hints[0].HolderPeerID)
	}
}

func TestHolderCache_ExpiredNotReturned(t *testing.T) {
	c := NewHolderHintCache(100)
	var hash [32]byte
	hash[0] = 2
	c.Add(HolderHint{
		BlobHash:       hash,
		HolderPeerID:   "peer-1",
		ValidUntilUnix: time.Now().Add(-time.Minute).Unix(), // expired
	})
	hints := c.Get(hash)
	if len(hints) != 0 {
		t.Errorf("expired hint should not be returned; got %d", len(hints))
	}
}

func TestHolderCache_LRUEviction(t *testing.T) {
	c := NewHolderHintCache(2) // max 2
	future := time.Now().Add(time.Minute).Unix()
	for i := 0; i < 3; i++ {
		var hash [32]byte
		hash[0] = byte(i)
		c.Add(HolderHint{BlobHash: hash, HolderPeerID: "p", ValidUntilUnix: future})
	}
	if c.Size() > 2 {
		t.Errorf("cache size = %d; want <= 2", c.Size())
	}
}

func TestHolderCache_Blacklist(t *testing.T) {
	c := NewHolderHintCache(100)
	var hash [32]byte
	hash[0] = 3
	c.Add(HolderHint{
		BlobHash:       hash,
		HolderPeerID:   "bad-peer",
		ValidUntilUnix: time.Now().Add(time.Minute).Unix(),
	})
	c.Blacklist("bad-peer", time.Minute)
	hints := c.Get(hash)
	if len(hints) != 0 {
		t.Errorf("blacklisted peer's hints should not be returned; got %d", len(hints))
	}
}

func TestHolderCache_Remove(t *testing.T) {
	c := NewHolderHintCache(100)
	var hash [32]byte
	hash[0] = 4
	c.Add(HolderHint{
		BlobHash:       hash,
		HolderPeerID:   "peer-x",
		ValidUntilUnix: time.Now().Add(time.Minute).Unix(),
	})
	c.Remove(hash, "peer-x")
	hints := c.Get(hash)
	if len(hints) != 0 {
		t.Errorf("removed hint should not be returned; got %d", len(hints))
	}
}
