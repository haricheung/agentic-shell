// Package memory implements R5 — the MKCT (Megram/Knowledge/Common-Sense/Thinking)
// memory engine backed by LevelDB. GGS is the sole writer; Planner queries structured data.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/types"
)

// LevelDB key prefix scheme — uses "|" as separator so colons in space/entity are safe.
//
//	m|<id>               → Megram JSON             (primary record)
//	x|<space>|<entity>|<id> → nil                  (inverted index for tag scan)
//	l|<level>|<id>       → nil                     (level scan for Dreamer)
//	r|<id>               → RFC3339                 (last_recalled_at; only mutable key)
const (
	prefixMegram = "m|"
	prefixIdx    = "x|"
	prefixLevel  = "l|"
	prefixRecall = "r|"
)

// GGS quantization matrix: maps macro-state to (f, σ, k).
// Decay constants: k=0.05 ≈ 14-day half-life; k=0.2 ≈ 3.5-day; k=0.5 ≈ 1.4-day.
var quantizationMatrix = map[string]struct{ f, sigma, k float64 }{
	"abandon":         {f: 0.95, sigma: -1.0, k: 0.05},
	"accept":          {f: 0.90, sigma: +1.0, k: 0.05},
	"change_approach": {f: 0.85, sigma: -1.0, k: 0.05},
	"success":         {f: 0.80, sigma: +1.0, k: 0.05},
	"break_symmetry":  {f: 0.75, sigma: +1.0, k: 0.05},
	"change_path":     {f: 0.30, sigma: 0.0, k: 0.20},
	"refine":          {f: 0.10, sigma: +0.5, k: 0.50},
}

// Dreamer consolidation thresholds (upward flow).
const (
	lambdaAtt = 5.0 // M_attention threshold for C-level promotion
	lambdaDec = 3.0 // |M_decision| threshold for C-level promotion
)

// Store is the LevelDB-backed MKCT memory engine.
// Write() is async (fire-and-forget channel); QueryC/QueryMK are synchronous.
type Store struct {
	b       *bus.Bus
	db      *leveldb.DB
	llm     *llm.Client       // used by Dreamer Phase 3 distillation; nil disables upward consolidation
	writeCh chan types.Megram // async write queue; buffered to avoid blocking GGS hot path
}

// New opens (or creates) a LevelDB database at dbPath and returns a Store.
// dbPath should be a directory path (LevelDB creates it if absent).
// llmClient is used by the Dreamer's upward consolidation phase to distil C-level SOPs;
// pass nil to disable consolidation and run only GC + Trust Bankruptcy (v0.8 behaviour).
func New(b *bus.Bus, dbPath string, llmClient *llm.Client) *Store {
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		// Write to stderr directly — main.go redirects log to debug.log before calling New(),
		// so log.Fatalf would be invisible to the user. fmt.Fprintf(Stderr) bypasses that.
		fmt.Fprintf(os.Stderr, "\033[31m[R5] failed to open LevelDB at %s: %v\033[0m\n", dbPath, err)
		fmt.Fprintf(os.Stderr, "\033[2mAnother artoo process may be running (LevelDB is single-writer). Kill it and retry.\033[0m\n")
		os.Exit(1)
	}
	return &Store{
		b:       b,
		llm:     llmClient,
		writeCh: make(chan types.Megram, 1024),
		db:      db,
	}
}

// Write enqueues a Megram for async non-blocking persistence.
// Drops the Megram with a warning if the write queue is full (back-pressure).
//
// Expectations:
//   - Non-blocking: never blocks the caller goroutine
//   - Assigns ID and CreatedAt if missing
//   - Drops Megram with log warning when queue is at capacity
//   - Does not guarantee persistence before returning
func (s *Store) Write(m types.Megram) {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if m.Level == "" {
		m.Level = "M"
	}
	select {
	case s.writeCh <- m:
	default:
		slog.Warn("[R5] write queue full — dropping Megram", "id", m.ID, "state", m.State)
	}
}

// QueryC returns all C-level SOPs for the (space, entity) tag pair.
// Updates last_recalled_at for each returned entry, resetting time decay.
//
// Expectations:
//   - Returns only C-level Megrams matching the (space, entity) pair
//   - Returns empty slice (not error) when no C-level entries exist
//   - Updates last_recalled_at for every returned entry
//   - Returns error only on LevelDB iteration failure
func (s *Store) QueryC(ctx context.Context, space, entity string) ([]types.SOPRecord, error) {
	prefix := idxPrefix(space, entity)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
	defer iter.Release()

	var results []types.SOPRecord
	for iter.Next() {
		id := megIDFromIdxKey(string(iter.Key()), prefix)
		if id == "" {
			continue
		}
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}
		if m.Level != "C" {
			continue
		}
		// Update last_recalled_at to reset time decay for this entry.
		_ = s.db.Put([]byte(prefixRecall+id), []byte(time.Now().UTC().Format(time.RFC3339)), nil)
		results = append(results, types.SOPRecord{
			ID:      m.ID,
			Space:   m.Space,
			Entity:  m.Entity,
			Content: m.Content,
			Sigma:   m.Sigma,
		})
	}
	return results, iter.Error()
}

// QueryMK computes the live dual-channel convolution potentials for a (space, entity) pair.
// Time decay uses last_recalled_at when available (recall resets the decay clock).
//
// Expectations:
//   - Returns Potentials with all zero values and Action="Ignore" when no megrams match
//   - M_attention = Σ|fᵢ|·exp(−kᵢ·Δt_days)
//   - M_decision = Σσᵢ·fᵢ·exp(−kᵢ·Δt_days)
//   - Uses last_recalled_at as decay origin when it is later than created_at
//   - Action is derived via the action decision plane thresholds
//   - Returns error only on LevelDB iteration failure
func (s *Store) QueryMK(ctx context.Context, space, entity string) (types.Potentials, error) {
	prefix := idxPrefix(space, entity)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
	defer iter.Release()

	now := time.Now().UTC()
	var attention, decision float64

	for iter.Next() {
		id := megIDFromIdxKey(string(iter.Key()), prefix)
		if id == "" {
			continue
		}
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}

		createdAt, err := time.Parse(time.RFC3339, m.CreatedAt)
		if err != nil {
			continue
		}
		// Use last_recalled_at as decay origin when available (recall resets clock).
		decayOrigin := createdAt
		if recallBytes, err := s.db.Get([]byte(prefixRecall+id), nil); err == nil {
			if recalled, err := time.Parse(time.RFC3339, string(recallBytes)); err == nil {
				if recalled.After(decayOrigin) {
					decayOrigin = recalled
				}
			}
		}

		deltaDays := now.Sub(decayOrigin).Hours() / 24.0
		decay := math.Exp(-m.K * deltaDays)
		attention += math.Abs(m.F) * decay
		decision += m.Sigma * m.F * decay
	}

	if err := iter.Error(); err != nil {
		return types.Potentials{}, err
	}
	return types.Potentials{
		Attention: attention,
		Decision:  decision,
		Action:    deriveAction(attention, decision),
	}, nil
}

// QueryRecent returns up to n most recent M/K-level Megrams for the given (space, entity)
// pair, sorted newest-first by CreatedAt. Skips consolidated entries.
// Used by Planner to inject concrete past experience directly into R2's prompt without
// waiting for Dreamer C-level promotion — mirrors the behaviour of the old file-backed
// memory system which injected raw content directly.
//
// Expectations:
//   - Returns at most n entries
//   - Only returns M and K level entries (not C or T)
//   - Skips entries with State="consolidated"
//   - Sorted newest-first by CreatedAt
//   - Returns empty slice (not error) when no entries match
func (s *Store) QueryRecent(_ context.Context, space, entity string, n int) ([]types.Megram, error) {
	prefix := idxPrefix(space, entity)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
	defer iter.Release()

	var results []types.Megram
	for iter.Next() {
		id := megIDFromIdxKey(string(iter.Key()), prefix)
		if id == "" {
			continue
		}
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}
		if m.Level != "M" && m.Level != "K" {
			continue
		}
		if m.State == "consolidated" {
			continue
		}
		results = append(results, m)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt > results[j].CreatedAt
	})
	if len(results) > n {
		results = results[:n]
	}
	return results, nil
}

// RecordNegativeFeedback appends a negative-σ Megram that mathematically cancels
// a stale positive potential. This implements the "Soft Overwrite" from Module 4.
//
// Expectations:
//   - No-ops when ruleID does not exist in the database
//   - Copies space/entity/level/k from the original Megram
//   - Sets sigma = -1.0 on the new Megram regardless of original sigma
//   - Writes via the async queue (fire-and-forget)
func (s *Store) RecordNegativeFeedback(_ context.Context, ruleID, content string) {
	orig, err := s.fetchMegram(ruleID)
	if err != nil {
		return
	}
	s.Write(types.Megram{
		ID:        uuid.New().String(),
		Level:     orig.Level,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     orig.Space,
		Entity:    orig.Entity,
		Content:   content,
		State:     "negative_feedback",
		F:         orig.F,
		Sigma:     -1.0,
		K:         orig.K,
	})
}

// Close is a no-op; Run() handles draining and DB close on context cancellation.
// Satisfies the types.MemoryService interface.
func (s *Store) Close() {}

// Run processes the async write queue and runs the Dreamer in the background.
// Drains all pending writes and closes the DB when ctx is cancelled.
func (s *Store) Run(ctx context.Context) {
	go s.dreamer(ctx)

	for {
		select {
		case <-ctx.Done():
			s.drainWriteQueue()
			if err := s.db.Close(); err != nil {
				slog.Warn("[R5] DB close error", "error", err)
			}
			return
		case m := <-s.writeCh:
			s.persistMegram(m)
		}
	}
}

// ---------------------------------------------------------------------------
// Internal — write path
// ---------------------------------------------------------------------------

func (s *Store) persistMegram(m types.Megram) {
	data, err := json.Marshal(m)
	if err != nil {
		slog.Error("[R5] marshal megram failed", "id", m.ID, "error", err)
		return
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(prefixMegram+m.ID), data)
	batch.Put([]byte(idxKey(m.Space, m.Entity, m.ID)), nil)
	batch.Put([]byte(levelKey(m.Level, m.ID)), nil)

	if err := s.db.Write(batch, nil); err != nil {
		slog.Error("[R5] persist megram failed", "id", m.ID, "error", err)
		return
	}
	slog.Info("[R5] persisted Megram", "id", m.ID, "level", m.Level, "state", m.State, "space", m.Space, "entity", m.Entity)
}

func (s *Store) drainWriteQueue() {
	for {
		select {
		case m := <-s.writeCh:
			s.persistMegram(m)
		default:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Internal — Dreamer (offline consolidation engine)
// ---------------------------------------------------------------------------

// dreamer runs the Dreamer consolidation/GC engine.
// Triggers: (a) 5-minute periodic timer, (b) 50 ms after each FinalResult
// (debounced) so Megrams from GGS flush before GC/trust-bankruptcy runs,
// (c) one final cycle on context cancellation (handles one-shot mode exit).
func (s *Store) dreamer(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	finalResultCh := s.b.Subscribe(types.MsgFinalResult)
	var settleC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			s.runDreamer("shutdown")
			return
		case <-ticker.C:
			s.runDreamer("timer")
		case _, ok := <-finalResultCh:
			if !ok {
				return
			}
			// Short settle so GGS Megram write (async) lands before consolidation.
			// Debounced: re-arming on rapid successive FinalResults is intentional.
			settleC = time.After(50 * time.Millisecond)
		case <-settleC:
			settleC = nil
			s.runDreamer("post-task")
		}
	}
}

func (s *Store) runDreamer(trigger string) {
	start := time.Now()
	slog.Info("[R5/Dreamer] consolidation cycle starting", "trigger", trigger)
	gcScanned, gcDeleted := s.gcPass()
	tbScanned, tbDemoted := s.trustBankruptcyPass()
	upPromoted := 0
	if s.llm != nil {
		upPromoted = s.consolidationPass(context.Background())
	}
	elapsed := time.Since(start)
	slog.Info("[R5/Dreamer] consolidation cycle complete",
		"elapsed_ms", elapsed.Milliseconds(),
		"gc_scanned", gcScanned, "gc_deleted", gcDeleted,
		"trust_scanned", tbScanned, "trust_demoted", tbDemoted,
		"up_promoted", upPromoted)
}

// gcPass scans M and K-level Megrams and hard-deletes those with M_attention < 0.1.
// Returns (scanned, deleted) counts for Dreamer cycle logging.
//
// Expectations:
//   - Deletes M/K megrams whose decayed attention potential falls below Λ_gc=0.1
//   - Does not delete C or T level megrams
//   - Removes all four index entries (primary, inverted, level, recall) on delete
func (s *Store) gcPass() (scanned, deleted int) {
	now := time.Now().UTC()
	for _, lvl := range []string{"M", "K"} {
		prefix := prefixLevel + lvl + "|"
		iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
		var toDelete []string
		for iter.Next() {
			scanned++
			id := string(iter.Key())[len(prefix):]
			m, err := s.fetchMegram(id)
			if err != nil {
				continue
			}
			createdAt, err := time.Parse(time.RFC3339, m.CreatedAt)
			if err != nil {
				continue
			}
			deltaDays := now.Sub(createdAt).Hours() / 24.0
			decay := math.Exp(-m.K * deltaDays)
			if math.Abs(m.F)*decay < 0.1 {
				toDelete = append(toDelete, id)
			}
		}
		iter.Release()
		for _, id := range toDelete {
			s.deleteMegram(id, lvl)
			deleted++
			slog.Info("[R5/Dreamer] GC deleted Megram", "id", id, "level", lvl, "reason", "M_att < Λ_gc=0.1")
		}
	}
	slog.Debug("[R5/Dreamer] GC pass complete", "scanned", scanned, "deleted", deleted, "threshold_lambda_gc", 0.1)
	return
}

// trustBankruptcyPass scans C-level Megrams and demotes those whose live
// M_decision < 0.0 to K-level with k reverted to 0.05 (stripping time immunity).
// Returns (scanned, demoted) counts for Dreamer cycle logging.
//
// Expectations:
//   - Only processes C-level Megrams
//   - Demotes to K (level="K", k=0.05) when live M_decision for the tag pair is < 0.0
//   - Updates the primary megram record, level index (removes C, adds K), leaves idx intact
//   - Does not delete demoted megrams (they remain queryable)
func (s *Store) trustBankruptcyPass() (scanned, demoted int) {
	prefix := prefixLevel + "C|"
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
	var toUpdate []types.Megram
	for iter.Next() {
		scanned++
		id := string(iter.Key())[len(prefix):]
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}
		pots, err := s.QueryMK(context.Background(), m.Space, m.Entity)
		if err != nil {
			continue
		}
		if pots.Decision < 0.0 {
			m.Level = "K"
			m.K = 0.05
			toUpdate = append(toUpdate, m)
		}
	}
	iter.Release()
	slog.Debug("[R5/Dreamer] Trust Bankruptcy pass", "c_level_scanned", scanned, "to_demote", len(toUpdate))
	for _, m := range toUpdate {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		batch := new(leveldb.Batch)
		batch.Put([]byte(prefixMegram+m.ID), data)
		batch.Delete([]byte(levelKey("C", m.ID)))
		batch.Put([]byte(levelKey("K", m.ID)), nil)
		if err := s.db.Write(batch, nil); err != nil {
			slog.Error("[R5/Dreamer] trust bankruptcy update failed", "id", m.ID, "error", err)
		} else {
			demoted++
			slog.Info("[R5/Dreamer] Trust Bankruptcy: demoted C→K", "id", m.ID, "reason", "M_dec < 0.0")
		}
	}
	return
}

// consolidationPass scans M/K-level Megrams grouped by (space, entity) and promotes
// qualifying clusters to C-level SOPs via LLM distillation.
// Returns the count of newly promoted C-level Megrams.
//
// Promotion thresholds (Λ_promote from dreamer.md):
//   - M_attention ≥ λAtt=5.0 AND M_decision ≥ λDec=3.0  → Best Practice (σ=+1.0)
//   - M_attention ≥ λAtt=5.0 AND M_decision ≤ −λDec=−3.0 → Constraint   (σ=−1.0)
//
// Expectations:
//   - No-ops when s.llm is nil
//   - Only scans M and K level Megrams
//   - Skips Megrams with State="consolidated" (already promoted in a prior cycle)
//   - Groups by (space, entity); computes live dual-channel potentials for each group
//   - Promotes at most one new C-level Megram per (space, entity) group per cycle
//   - Marks all source Megrams in a promoted group as State="consolidated"
//   - Returns count of groups promoted this cycle
func (s *Store) consolidationPass(ctx context.Context) int {
	if s.llm == nil {
		return 0
	}

	type groupKey struct{ space, entity string }
	type groupEntry struct {
		meg      types.Megram
		decayAtt float64
		decayDec float64
	}
	groups := make(map[groupKey][]groupEntry)

	now := time.Now().UTC()
	for _, lvl := range []string{"M", "K"} {
		prefix := prefixLevel + lvl + "|"
		iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
		for iter.Next() {
			id := string(iter.Key())[len(prefix):]
			m, err := s.fetchMegram(id)
			if err != nil {
				continue
			}
			if m.State == "consolidated" {
				continue
			}
			createdAt, err := time.Parse(time.RFC3339, m.CreatedAt)
			if err != nil {
				continue
			}
			decayOrigin := createdAt
			if recallBytes, err := s.db.Get([]byte(prefixRecall+id), nil); err == nil {
				if recalled, err := time.Parse(time.RFC3339, string(recallBytes)); err == nil {
					if recalled.After(decayOrigin) {
						decayOrigin = recalled
					}
				}
			}
			deltaDays := now.Sub(decayOrigin).Hours() / 24.0
			decay := math.Exp(-m.K * deltaDays)
			att := math.Abs(m.F) * decay
			dec := m.Sigma * m.F * decay
			k := groupKey{m.Space, m.Entity}
			groups[k] = append(groups[k], groupEntry{meg: m, decayAtt: att, decayDec: dec})
		}
		iter.Release()
	}

	promoted := 0
	for k, entries := range groups {
		var totalAtt, totalDec float64
		for _, e := range entries {
			totalAtt += e.decayAtt
			totalDec += e.decayDec
		}
		if totalAtt < lambdaAtt {
			continue
		}
		var signal string
		var sigma float64
		if totalDec >= lambdaDec {
			signal = "Best Practice"
			sigma = +1.0
		} else if totalDec <= -lambdaDec {
			signal = "Constraint"
			sigma = -1.0
		} else {
			continue
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].meg.CreatedAt > entries[j].meg.CreatedAt
		})
		contents := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.meg.Content != "" {
				contents = append(contents, e.meg.Content)
			}
		}

		rule, err := s.distilSOP(ctx, k.space, k.entity, signal, contents)
		if err != nil {
			slog.Warn("[R5/Dreamer] distilSOP failed", "space", k.space, "entity", k.entity, "error", err)
			continue
		}

		sopMeg := types.Megram{
			ID:        uuid.New().String(),
			Level:     "C",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Space:     k.space,
			Entity:    k.entity,
			Content:   rule,
			State:     signal,
			F:         math.Abs(sigma),
			Sigma:     sigma,
			K:         0.0,
		}
		s.persistMegram(sopMeg)
		slog.Info("[R5/Dreamer] promoted C-level SOP",
			"space", k.space, "entity", k.entity, "signal", signal,
			"att", totalAtt, "dec", totalDec, "rule", rule)

		for _, e := range entries {
			updated := e.meg
			updated.State = "consolidated"
			if data, err := json.Marshal(updated); err == nil {
				_ = s.db.Put([]byte(prefixMegram+updated.ID), data, nil)
			}
		}
		promoted++
	}
	return promoted
}

// distilSOP invokes the LLM to synthesise a single C-level rule from a cluster of Megram
// content strings. Returns the rule text or an error.
//
// Expectations:
//   - Returns non-empty rule string on success
//   - Returns error when LLM call fails
//   - Caps source experiences at 10 (newest first)
//   - Prompt instructs LLM to output ONLY the rule text (no preamble)
func (s *Store) distilSOP(ctx context.Context, space, entity, signal string, contents []string) (string, error) {
	if len(contents) > 10 {
		contents = contents[:10]
	}
	var sb strings.Builder
	sb.WriteString("You are synthesising accumulated task experience into a reusable rule.\n\n")
	sb.WriteString("Space: " + space + "\n")
	sb.WriteString("Entity: " + entity + "\n")
	sb.WriteString("Signal: " + signal + "\n\n")
	sb.WriteString("Source experiences (newest first):\n")
	for i, c := range contents {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, c))
	}
	sb.WriteString("\nWrite ONE concise rule (≤2 sentences) that captures the generalised lesson from these experiences.\n")
	sb.WriteString("The rule must be actionable: a Planner reading it should know exactly what to do or avoid.\n")
	sb.WriteString("Output ONLY the rule text, no preamble.")

	rule, _, err := s.llm.Chat(ctx, "", sb.String())
	if err != nil {
		return "", err
	}
	rule = strings.TrimSpace(rule)
	return rule, nil
}

// deleteMegram removes all keys associated with a Megram from LevelDB.
func (s *Store) deleteMegram(id, level string) {
	m, err := s.fetchMegram(id)
	if err != nil {
		return
	}
	batch := new(leveldb.Batch)
	batch.Delete([]byte(prefixMegram + id))
	batch.Delete([]byte(idxKey(m.Space, m.Entity, id)))
	batch.Delete([]byte(levelKey(level, id)))
	batch.Delete([]byte(prefixRecall + id))
	_ = s.db.Write(batch, nil)
}

// DeleteByPrefix scans all Megrams and deletes those whose ID starts with the
// given prefix. Returns the number of Megrams deleted. Used by /forget command.
//
// Expectations:
//   - Returns 0 when no Megram ID starts with prefix
//   - Deletes all index keys (m|, x|, l|, r|) for each matched Megram
//   - Returns the count of deleted Megrams
func (s *Store) DeleteByPrefix(prefix string) int {
	deleted := 0
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefixMegram)), nil)
	for iter.Next() {
		id := string(iter.Key())[len(prefixMegram):]
		if strings.HasPrefix(id, prefix) {
			var m types.Megram
			if err := json.Unmarshal(iter.Value(), &m); err != nil {
				continue
			}
			s.deleteMegram(id, m.Level)
			deleted++
		}
	}
	iter.Release()
	return deleted
}

// DeleteAll removes every Megram and all associated index keys from LevelDB.
// Returns the count of deleted Megrams. Used by /forget all.
//
// Expectations:
//   - Deletes all m|, x|, l|, r| keys for every Megram
//   - Returns the total number of Megrams deleted
func (s *Store) DeleteAll() int {
	deleted := 0
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefixMegram)), nil)
	var ids []string
	for iter.Next() {
		ids = append(ids, string(iter.Key())[len(prefixMegram):])
	}
	iter.Release()
	for _, id := range ids {
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}
		s.deleteMegram(id, m.Level)
		deleted++
	}
	return deleted
}

// fetchMegram retrieves a Megram by ID from LevelDB.
func (s *Store) fetchMegram(id string) (types.Megram, error) {
	data, err := s.db.Get([]byte(prefixMegram+id), nil)
	if err != nil {
		return types.Megram{}, err
	}
	var m types.Megram
	return m, json.Unmarshal(data, &m)
}

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

// idxPrefix returns the LevelDB prefix for an inverted index scan.
func idxPrefix(space, entity string) string {
	return prefixIdx + safeKeyPart(space) + "|" + safeKeyPart(entity) + "|"
}

// idxKey returns the full inverted index key for a (space, entity, id) triple.
func idxKey(space, entity, id string) string {
	return idxPrefix(space, entity) + id
}

// levelKey returns the level-scan index key.
func levelKey(level, id string) string {
	return prefixLevel + level + "|" + id
}

// megIDFromIdxKey extracts the megram ID from a full index key, given the known prefix.
func megIDFromIdxKey(fullKey, prefix string) string {
	if !strings.HasPrefix(fullKey, prefix) {
		return ""
	}
	return fullKey[len(prefix):]
}

// safeKeyPart replaces "|" with "_" so LevelDB keys parse unambiguously.
func safeKeyPart(s string) string {
	return strings.ReplaceAll(s, "|", "_")
}

// ---------------------------------------------------------------------------
// Exported helpers (used by GGS and Planner)
// ---------------------------------------------------------------------------

// IntentSlug derives the MKCT space tag from a task intent string.
// Format: "intent:<first_three_words_lowercase_underscored_alphanumeric_only>".
//
// Expectations:
//   - Always starts with "intent:"
//   - Uses at most 3 words from the intent
//   - Lowercases all characters
//   - Joins words with underscore
//   - Strips all characters except a-z, 0-9, and underscore
//   - Returns "intent:" for an empty or whitespace-only intent
func IntentSlug(intent string) string {
	words := strings.Fields(strings.ToLower(intent))
	max := 3
	if len(words) < max {
		max = len(words)
	}
	var parts []string
	for _, w := range words[:max] {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			parts = append(parts, b.String())
		}
	}
	return "intent:" + strings.Join(parts, "_")
}

// ParseToolCall extracts the tool name and primary target value from a tool-call
// string in the format produced by R3 Executor:
//
//	"toolname: {json_input} → output_snippet"
//
// Extracts the "query", "command", or "path" field from the JSON input.
//
// Expectations:
//   - Returns ("", "") when string lacks ": "
//   - Extracts tool name as the part before the first ": "
//   - Strips the " → output_snippet" suffix before JSON parsing
//   - Returns "query" field when present in JSON
//   - Returns "command" field when "query" absent
//   - Returns "path" field when both "query" and "command" absent
//   - Returns ("toolname", "") when JSON has none of the recognized fields
//   - Returns ("toolname", "") when JSON is malformed
func ParseToolCall(tc string) (toolName, target string) {
	colonIdx := strings.Index(tc, ": ")
	if colonIdx < 0 {
		return "", ""
	}
	toolName = strings.TrimSpace(tc[:colonIdx])

	rest := tc[colonIdx+2:]
	if arrowIdx := strings.Index(rest, " → "); arrowIdx >= 0 {
		rest = rest[:arrowIdx]
	}

	var m map[string]string
	if err := json.Unmarshal([]byte(rest), &m); err != nil {
		return toolName, ""
	}
	for _, key := range []string{"query", "command", "path"} {
		if val := strings.TrimSpace(m[key]); val != "" {
			return toolName, val
		}
	}
	return toolName, ""
}

// deriveAction maps dual-channel potential values to an action using the
// decision plane thresholds from the MKCT spec.
//
// Expectations:
//   - Returns "Ignore" when attention < 0.5
//   - Returns "Exploit" when attention >= 0.5 and decision > 0.2
//   - Returns "Avoid" when attention >= 0.5 and decision < -0.2
//   - Returns "Caution" when attention >= 0.5 and -0.2 <= decision <= 0.2
func deriveAction(attention, decision float64) string {
	if attention < 0.5 {
		return "Ignore"
	}
	if decision > 0.2 {
		return "Exploit"
	}
	if decision < -0.2 {
		return "Avoid"
	}
	return "Caution"
}

// Summary scans the LevelDB store and returns per-level Megram counts and all C-level entries.
// Used by the /memory REPL command. Reads are synchronous; not called from hot paths.
//
// Expectations:
//   - LevelCounts contains an entry for each of "M", "K", "C", "T" (zero when empty)
//   - CLevel contains one SOPRecord per C-level Megram currently in the store
func (s *Store) Summary() types.MemorySummary {
	counts := map[string]int{"M": 0, "K": 0, "C": 0, "T": 0}
	for _, lvl := range []string{"M", "K", "C", "T"} {
		prefix := prefixLevel + lvl + "|"
		iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
		for iter.Next() {
			counts[lvl]++
		}
		iter.Release()
	}

	var cLevel []types.SOPRecord
	cPrefix := prefixLevel + "C|"
	iter := s.db.NewIterator(util.BytesPrefix([]byte(cPrefix)), nil)
	for iter.Next() {
		id := string(iter.Key())[len(cPrefix):]
		m, err := s.fetchMegram(id)
		if err != nil {
			continue
		}
		cLevel = append(cLevel, types.SOPRecord{
			ID:      m.ID,
			Space:   m.Space,
			Entity:  m.Entity,
			Content: m.Content,
			Sigma:   m.Sigma,
		})
	}
	iter.Release()

	return types.MemorySummary{LevelCounts: counts, CLevel: cLevel}
}

// SummaryVerbose returns the same counts as Summary plus a Groups slice that contains
// every Megram in the store, grouped by (level, space, entity) with per-entry and
// aggregate dual-channel potentials.
//
// Expectations:
//   - Groups contains one entry per distinct (level, space, entity) combination
//   - Groups are sorted: M→K→C→T, then by space, then by entity
//   - Each MegRamRecord carries its individual attention/decision contribution
//   - Aggregate Attention/Decision equal the sum of their entries' contributions
//   - Action is derived from the aggregate potentials using the decision plane
func (s *Store) SummaryVerbose() types.MemorySummary {
	base := s.Summary()
	now := time.Now().UTC()

	groupMap := make(map[string]*types.MegRamGroup) // key: "level|space|entity"

	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefixMegram)), nil)
	for iter.Next() {
		var m types.Megram
		if err := json.Unmarshal(iter.Value(), &m); err != nil {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, m.CreatedAt)
		if err != nil {
			continue
		}
		decayOrigin := createdAt
		if recallBytes, err := s.db.Get([]byte(prefixRecall+m.ID), nil); err == nil {
			if recalled, err := time.Parse(time.RFC3339, string(recallBytes)); err == nil {
				if recalled.After(decayOrigin) {
					decayOrigin = recalled
				}
			}
		}
		deltaDays := now.Sub(decayOrigin).Hours() / 24.0
		decay := math.Exp(-m.K * deltaDays)
		att := math.Abs(m.F) * decay
		dec := m.Sigma * m.F * decay

		key := m.Level + "|" + m.Space + "|" + m.Entity
		if _, ok := groupMap[key]; !ok {
			groupMap[key] = &types.MegRamGroup{
				Level:  m.Level,
				Space:  m.Space,
				Entity: m.Entity,
			}
		}
		g := groupMap[key]
		g.Megrams = append(g.Megrams, types.MegRamRecord{
			ID:        m.ID,
			State:     m.State,
			Content:   m.Content,
			Sigma:     m.Sigma,
			F:         m.F,
			K:         m.K,
			CreatedAt: m.CreatedAt,
			Attention: att,
			Decision:  dec,
		})
		g.Attention += att
		g.Decision += dec
	}
	iter.Release()

	levelOrder := map[string]int{"M": 0, "K": 1, "C": 2, "T": 3}
	groups := make([]types.MegRamGroup, 0, len(groupMap))
	for _, g := range groupMap {
		g.Action = deriveAction(g.Attention, g.Decision)
		// Sort Megrams within each group newest-first.
		sort.Slice(g.Megrams, func(i, j int) bool {
			return g.Megrams[i].CreatedAt > g.Megrams[j].CreatedAt
		})
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		li, lj := levelOrder[groups[i].Level], levelOrder[groups[j].Level]
		if li != lj {
			return li < lj
		}
		if groups[i].Space != groups[j].Space {
			return groups[i].Space < groups[j].Space
		}
		return groups[i].Entity < groups[j].Entity
	})

	base.Groups = groups
	return base
}

// QuantizationMatrix exports the GGS state → (f, σ, k) table for use by GGS write path.
func QuantizationMatrix() map[string]struct{ F, Sigma, K float64 } {
	out := make(map[string]struct{ F, Sigma, K float64 }, len(quantizationMatrix))
	for state, q := range quantizationMatrix {
		out[state] = struct{ F, Sigma, K float64 }{F: q.f, Sigma: q.sigma, K: q.k}
	}
	return out
}
