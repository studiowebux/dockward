package hub

import (
	"testing"
)

func TestHub_Subscribe_ReceivesBroadcast(t *testing.T) {
	h := NewHub()
	ch, err := h.Subscribe("127.0.0.1")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer h.Unsubscribe(ch)

	h.Broadcast([]byte("hello"))

	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Errorf("want %q, got %q", "hello", string(msg))
		}
	default:
		t.Error("expected message in channel after Broadcast")
	}
}

func TestHub_Broadcast_ReachesAllSubscribers(t *testing.T) {
	h := NewHub()
	ch1, err := h.Subscribe("127.0.0.1")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	ch2, err := h.Subscribe("127.0.0.2")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer h.Unsubscribe(ch1)
	defer h.Unsubscribe(ch2)

	h.Broadcast([]byte("ping"))

	for i, ch := range []chan []byte{ch1, ch2} {
		select {
		case msg := <-ch:
			if string(msg) != "ping" {
				t.Errorf("subscriber %d: want ping, got %q", i+1, string(msg))
			}
		default:
			t.Errorf("subscriber %d did not receive broadcast", i+1)
		}
	}
}

func TestHub_Unsubscribe_StopsReceiving(t *testing.T) {
	h := NewHub()
	ch, err := h.Subscribe("127.0.0.1")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	h.Unsubscribe(ch)

	// Channel is closed; reading from it should return immediately with zero value.
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after Unsubscribe")
	}
}

func TestHub_Broadcast_DropsForSlowClient(t *testing.T) {
	h := NewHub()
	// Use a zero-buffer channel to simulate a slow client.
	// The hub creates buffered channels (size 64) via Subscribe, so we
	// manually insert an unbuffered channel to test the drop path.
	slowCh := make(chan []byte) // unbuffered — always "full"
	h.mu.Lock()
	h.clients[slowCh] = &ClientInfo{IP: "127.0.0.1", Channel: slowCh}
	h.mu.Unlock()

	fastCh, err := h.Subscribe("127.0.0.2")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer h.Unsubscribe(fastCh)

	// Should not block even though slowCh is unbuffered.
	h.Broadcast([]byte("data"))

	// Fast client receives; slow client drops.
	select {
	case msg := <-fastCh:
		if string(msg) != "data" {
			t.Errorf("want data, got %q", string(msg))
		}
	default:
		t.Error("fast client should have received the broadcast")
	}

	select {
	case <-slowCh:
		t.Error("slow client should have had the message dropped")
	default:
		// Correct: message was dropped, channel is empty.
	}

	// Clean up the manually inserted channel.
	h.mu.Lock()
	delete(h.clients, slowCh)
	h.mu.Unlock()
}
