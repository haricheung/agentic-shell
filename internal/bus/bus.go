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
// Multiple consumers (Auditor, UI) can each register their own tap channel via NewTap.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[types.MessageType][]chan types.Message
	taps        []chan types.Message
}

// New creates a new Bus.
func New() *Bus {
	return &Bus{
		subscribers: make(map[types.MessageType][]chan types.Message),
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

	// Fan out to all tap channels (auditor, UI, etc.). Non-blocking.
	b.mu.RLock()
	taps := b.taps
	b.mu.RUnlock()
	for _, tap := range taps {
		select {
		case tap <- msg:
		default:
			log.Printf("[BUS] WARNING: tap channel full — message dropped type=%s", msg.Type)
		}
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

// NewTap registers and returns a new read-only tap channel.
// Each caller gets an independent channel that receives every published message.
func (b *Bus) NewTap() <-chan types.Message {
	ch := make(chan types.Message, tapBufSize)
	b.mu.Lock()
	b.taps = append(b.taps, ch)
	b.mu.Unlock()
	return ch
}

// Tap is an alias for NewTap, kept for backward compatibility.
func (b *Bus) Tap() <-chan types.Message {
	return b.NewTap()
}
