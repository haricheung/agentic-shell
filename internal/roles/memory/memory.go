package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/types"
)

// Store is a file-backed JSON memory store. Only R4b (Meta-Validator) may write.
type Store struct {
	path string
	mu   sync.RWMutex
	data []types.MemoryEntry
	b    *bus.Bus
}

// New creates a Store backed by the JSON file at path.
func New(b *bus.Bus, path string) *Store {
	s := &Store{path: path, b: b}
	_ = s.load()
	return s
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.data)
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Write stores a MemoryEntry. Only R4b should call this (enforced by message routing).
func (s *Store) Write(entry types.MemoryEntry) error {
	if entry.EntryID == "" {
		entry.EntryID = uuid.New().String()
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, entry)
	return s.save()
}

// Query returns entries matching taskID, tags, and/or a natural-language keyword query.
// All provided filters are ANDed; an empty filter is a wildcard.
// The Query field is matched as keywords against entry tags, task_id, and serialised content.
func (s *Store) Query(taskID, tags, query string) ([]types.MemoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Pre-tokenise natural-language query into lowercase words
	var keywords []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if len(w) >= 3 { // skip short noise words
			keywords = append(keywords, w)
		}
	}

	var results []types.MemoryEntry
	for _, e := range s.data {
		if taskID != "" && e.TaskID != taskID {
			continue
		}
		if tags != "" {
			matched := false
			for _, tag := range e.Tags {
				if strings.Contains(strings.ToLower(tag), strings.ToLower(tags)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(keywords) > 0 {
			// Serialise the entry into a single string for keyword scanning
			raw, _ := json.Marshal(e)
			haystack := strings.ToLower(string(raw))
			anyMatch := false
			for _, kw := range keywords {
				if strings.Contains(haystack, kw) {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				continue
			}
		}
		results = append(results, e)
	}
	return results, nil
}

// Run starts the memory store's bus listener goroutine.
func (s *Store) Run(ctx context.Context) {
	writeCh := s.b.Subscribe(types.MsgMemoryWrite)
	readCh := s.b.Subscribe(types.MsgMemoryRead)

	for {
		select {
		case <-ctx.Done():
			// Drain any pending writes before exiting so one-shot mode doesn't lose data
			for {
				select {
				case msg := <-writeCh:
					entry, err := toMemoryEntry(msg.Payload)
					if err == nil {
						if err := s.Write(entry); err != nil {
							log.Printf("[R5] ERROR: shutdown write failed: %v", err)
						} else {
							log.Printf("[R5] stored entry %s for task %s (shutdown flush)", entry.EntryID, entry.TaskID)
						}
					}
				default:
					return
				}
			}

		case msg, ok := <-writeCh:
			if !ok {
				return
			}
			entry, err := toMemoryEntry(msg.Payload)
			if err != nil {
				log.Printf("[R5] ERROR: bad MemoryEntry payload: %v", err)
				continue
			}
			if err := s.Write(entry); err != nil {
				log.Printf("[R5] ERROR: write failed: %v", err)
			} else {
				log.Printf("[R5] stored entry %s for task %s", entry.EntryID, entry.TaskID)
			}

		case msg, ok := <-readCh:
			if !ok {
				return
			}
			query, err := toMemoryQuery(msg.Payload)
			if err != nil {
				log.Printf("[R5] ERROR: bad MemoryQuery payload: %v", err)
				continue
			}
			entries, err := s.Query(query.TaskID, query.Tags, query.Query)
			if err != nil {
				log.Printf("[R5] ERROR: query failed: %v", err)
				entries = nil
			}
			resp := types.MemoryResponse{
				TaskID:  query.TaskID,
				Entries: entries,
			}
			s.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleMemory,
				To:        types.RolePlanner,
				Type:      types.MsgMemoryResponse,
				Payload:   resp,
			})
		}
	}
}

func toMemoryEntry(payload any) (types.MemoryEntry, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.MemoryEntry{}, fmt.Errorf("marshal: %w", err)
	}
	var e types.MemoryEntry
	return e, json.Unmarshal(b, &e)
}

func toMemoryQuery(payload any) (types.MemoryQuery, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.MemoryQuery{}, fmt.Errorf("marshal: %w", err)
	}
	var q types.MemoryQuery
	return q, json.Unmarshal(b, &q)
}
