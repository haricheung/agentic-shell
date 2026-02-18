package bus

import (
	"log"
	"sync"

	"github.com/haricheung/agentic-shell/internal/types"
)

const (
	subscriberBufSize = 64
	tapBufSize        = 256
)

// Bus is the observable message bus. All inter-role communication passes through it.
// The Auditor receives a read-only tap channel for every message published.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[types.MessageType][]chan types.Message
	tapCh       chan types.Message
}

// New creates a new Bus.
func New() *Bus {
	return &Bus{
		subscribers: make(map[types.MessageType][]chan types.Message),
		tapCh:       make(chan types.Message, tapBufSize),
	}
}

// Publish fans out msg to all subscribers of msg.Type and to the tap channel.
// Non-blocking: if a subscriber's channel is full, the message is dropped with a warning.
func (b *Bus) Publish(msg types.Message) {
	b.mu.RLock()
	subs := b.subscribers[msg.Type]
	b.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			log.Printf("[BUS] WARNING: subscriber channel full for type=%s from=%s — message dropped", msg.Type, msg.From)
		}
	}

	// Send to tap (auditor). Non-blocking to avoid auditor backpressure stalling the bus.
	select {
	case b.tapCh <- msg:
	default:
		log.Printf("[BUS] WARNING: tap channel full — audit message dropped type=%s", msg.Type)
	}
}

// Subscribe returns a receive-only channel that delivers messages of type t.
// Each call creates a new independent subscriber channel.
func (b *Bus) Subscribe(t types.MessageType) <-chan types.Message {
	ch := make(chan types.Message, subscriberBufSize)
	b.mu.Lock()
	b.subscribers[t] = append(b.subscribers[t], ch)
	b.mu.Unlock()
	return ch
}

// Tap returns the read-only tap channel for the Auditor.
// Only one consumer should call this; calling it multiple times returns the same channel.
func (b *Bus) Tap() <-chan types.Message {
	return b.tapCh
}
