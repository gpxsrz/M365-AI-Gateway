package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	legacyTransportCheckpointSchema                = "wp6-transport-checkpoints/v1"
	transportCheckpointSchema                      = "wp6-transport-checkpoints/v2"
	transportCheckpointTTL                         = 24 * time.Hour
	transportCheckpointMaxFileBytes                = int64(32 << 20)
	transportCheckpointMaxRecords                  = 256
	transportCheckpointMaxMessages                 = 4096
	transportCheckpointMaxCursors                  = 64
	transportCheckpointMaxToolCalls                = 64
	transportCheckpointMaxToolEvidence             = 64
	transportCheckpointMaxCompletedToolCallDigests = transportCheckpointMaxMessages
	transportCheckpointHashDomain                  = "m365/wp6/transport-checkpoint/message/v1\x00"
	transportCheckpointChainDomain                 = "m365/wp6/transport-checkpoint/chain/v1\x00"
	transportCheckpointOwnerDomain                 = "m365/wp6/transport-checkpoint/owner/v1\x00"
	transportCheckpointKeyDomain                   = "m365/wp6/transport-checkpoint/key/v1\x00"
	transportCheckpointCursorDomain                = "m365/wp6/transport-checkpoint/cursor/v1\x00"
	transportCheckpointCallIDDomain                = "m365/wp6/transport-checkpoint/tool-call-id/v1\x00"
	transportCheckpointToolIdentityDomain          = "m365/wp6/transport-checkpoint/tool-identity/v1\x00"
	transportCheckpointMaxNamespace                = 128
	transportCheckpointMaxIdentity                 = 4096
	transportCheckpointMaxToolName                 = 512
)

var (
	ErrCheckpointIdentity          = errors.New("transport checkpoint identity is required")
	ErrCheckpointKeyRequired       = errors.New("transport checkpoint key is required")
	ErrCheckpointNotFound          = errors.New("transport checkpoint not found")
	ErrCheckpointBusy              = errors.New("transport checkpoint already has an in-flight turn")
	ErrCheckpointCapacity          = errors.New("transport checkpoint capacity reached")
	ErrCheckpointHistoryLimit      = errors.New("transport checkpoint history limit reached")
	ErrCheckpointUnknownCursor     = errors.New("transport checkpoint response cursor is unknown")
	ErrCheckpointAmbiguous         = errors.New("transport checkpoint match is ambiguous")
	ErrCheckpointTurnClosed        = errors.New("transport checkpoint turn is closed")
	ErrCheckpointAttemptStale      = errors.New("transport checkpoint turn is stale")
	ErrCheckpointInvalidBinding    = errors.New("transport checkpoint binding is invalid")
	ErrCheckpointConversationDrift = errors.New(
		"transport checkpoint conversation identity changed",
	)
	ErrCheckpointCanonicalization = errors.New("transport checkpoint message cannot be canonicalized")
	ErrCheckpointPersistence      = errors.New("transport checkpoint persistence failed")
)

type checkpointBinding struct {
	ConversationID string
	SessionID      string
}

type transportCheckpointView struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	SessionID      string    `json:"sessionId"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type transportCheckpointPersistenceSnapshot struct {
	RecordCount                      int    `json:"recordCount"`
	PersistedBytes                   int64  `json:"persistedBytes"`
	GenerationSwitchCount            uint64 `json:"generationSwitchCount"`
	LastGenerationRecordCount        int    `json:"lastGenerationRecordCount"`
	LastGenerationReusedRecordCount  int    `json:"lastGenerationReusedRecordCount"`
	LastGenerationWrittenRecordCount int    `json:"lastGenerationWrittenRecordCount"`
	LastGenerationDurationMS         int64  `json:"lastGenerationDurationMs"`
}

type checkpointTurn struct {
	Binding                   checkpointBinding
	Outbound                  []oaiMsg
	Rebound                   bool
	AllowedPriorToolCallIDs   []string
	KnownPriorToolCallDigests []string
	ToolLedger                agentLedger

	store               *transportCheckpointStore
	recordID            string
	revision            uint64
	baseDigests         []string
	baseHashChain       []string
	resolvedToolCallIDs []string
	untracked           bool
	closed              bool
}

func (t *checkpointTurn) RecordID() string {
	if t == nil {
		return ""
	}
	return t.recordID
}

func (t *checkpointTurn) Accept(binding checkpointBinding, produced []oaiMsg, responseID string) error {
	if t == nil || t.store == nil {
		return ErrCheckpointAttemptStale
	}
	return t.store.accept(t, binding, produced, responseID)
}

func (t *checkpointTurn) Abort() error {
	if t == nil || t.store == nil {
		return ErrCheckpointAttemptStale
	}
	return t.store.abort(t)
}

type transportCheckpointStore struct {
	mu                               sync.Mutex
	path                             string
	records                          map[string]*transportCheckpointRecord
	now                              func() time.Time
	generation                       string
	recordBytes                      map[string]int64
	persistedBytes                   int64
	nextPruneAt                      time.Time
	generationSwitchCount            uint64
	lastGenerationRecordCount        int
	lastGenerationReusedRecordCount  int
	lastGenerationWrittenRecordCount int
	lastGenerationDuration           time.Duration
}

type transportCheckpointFile struct {
	Schema  string                      `json:"schema"`
	Records []transportCheckpointRecord `json:"records"`
}

type transportCheckpointRecord struct {
	ID                           string                      `json:"id"`
	Namespace                    string                      `json:"namespace"`
	OwnerDigest                  string                      `json:"ownerDigest"`
	KeyDigest                    string                      `json:"keyDigest,omitempty"`
	ConversationID               string                      `json:"conversationId,omitempty"`
	CurrentSessionID             string                      `json:"currentSessionId,omitempty"`
	LastSessionID                string                      `json:"lastSessionId,omitempty"`
	AcceptedCount                int                         `json:"acceptedCount"`
	MessageDigests               []string                    `json:"messageDigests"`
	HashChain                    []string                    `json:"hashChain"`
	ResponseCursors              []checkpointResponseCursor  `json:"responseCursorDigests,omitempty"`
	PendingToolCalls             []checkpointPendingToolCall `json:"pendingToolCalls,omitempty"`
	CompletedToolEvidence        []toolEvidence              `json:"completedToolEvidence,omitempty"`
	CompletedToolCallDigests     []string                    `json:"completedToolCallDigests,omitempty"`
	CompletedToolIdentityDigests []string                    `json:"completedToolIdentityDigests,omitempty"`
	CreatedAt                    time.Time                   `json:"createdAt"`
	UpdatedAt                    time.Time                   `json:"updatedAt"`
	Revision                     uint64                      `json:"revision"`
	InFlight                     bool                        `json:"inFlight,omitempty"`
}

type checkpointPendingToolCall struct {
	CallID          string `json:"callId"`
	Name            string `json:"name"`
	ArgumentsDigest string `json:"argumentsDigest"`
}

type checkpointResponseCursor struct {
	Digest   string `json:"digest"`
	Revision uint64 `json:"revision"`
}

func openTransportCheckpointStore(path string) (*transportCheckpointStore, error) {
	return openTransportCheckpointStoreWithClock(path, func() time.Time { return time.Now().UTC() })
}

func openTransportCheckpointStoreWithClock(path string, now func() time.Time) (*transportCheckpointStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrCheckpointIdentity
	}
	if now == nil {
		return nil, ErrCheckpointIdentity
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: checkpoint file must not be a symbolic link", ErrCheckpointPersistence)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect path: %v", ErrCheckpointPersistence, err)
	}
	s := &transportCheckpointStore{
		path:        path,
		records:     make(map[string]*transportCheckpointRecord),
		recordBytes: make(map[string]int64),
		now:         now,
	}
	if err := s.loadPersistence(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *transportCheckpointStore) List() ([]transportCheckpointView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneExpiredLocked(); err != nil {
		return nil, err
	}
	views := make([]transportCheckpointView, 0, len(s.records))
	for _, record := range s.records {
		if record.ConversationID == "" {
			continue
		}
		views = append(views, transportCheckpointView{
			ID:             record.ID,
			ConversationID: record.ConversationID,
			SessionID:      record.CurrentSessionID,
			CreatedAt:      record.CreatedAt,
			UpdatedAt:      record.UpdatedAt,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].CreatedAt.Equal(views[j].CreatedAt) {
			return views[i].ID < views[j].ID
		}
		return views[i].CreatedAt.Before(views[j].CreatedAt)
	})
	return views, nil
}

func (s *transportCheckpointStore) persistenceSnapshot() transportCheckpointPersistenceSnapshot {
	if s == nil {
		return transportCheckpointPersistenceSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return transportCheckpointPersistenceSnapshot{
		RecordCount:                      len(s.records),
		PersistedBytes:                   s.persistedBytes,
		GenerationSwitchCount:            s.generationSwitchCount,
		LastGenerationRecordCount:        s.lastGenerationRecordCount,
		LastGenerationReusedRecordCount:  s.lastGenerationReusedRecordCount,
		LastGenerationWrittenRecordCount: s.lastGenerationWrittenRecordCount,
		LastGenerationDurationMS:         s.lastGenerationDuration.Milliseconds(),
	}
}

func (s *transportCheckpointStore) Delete(recordID string) (bool, error) {
	if recordID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneExpiredLocked(); err != nil {
		return false, err
	}
	if _, ok := s.records[recordID]; !ok {
		return false, nil
	}
	snapshot := cloneTransportCheckpointRecords(s.records)
	delete(s.records, recordID)
	if err := s.deleteRecordLocked(recordID); err != nil {
		s.records = snapshot
		return false, err
	}
	s.recomputeNextPruneAtLocked()
	return true, nil
}

func (s *transportCheckpointStore) Clear() error {
	return s.ClearThen(nil)
}

func (s *transportCheckpointStore) ClearThen(change func() error) error {
	if s == nil {
		if change != nil {
			return change()
		}
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := cloneTransportCheckpointRecords(s.records)
	previousNextPruneAt := s.nextPruneAt
	s.records = make(map[string]*transportCheckpointRecord)
	s.nextPruneAt = time.Time{}
	transition, err := s.switchCheckpointGenerationLocked(s.records)
	if err != nil {
		s.records = snapshot
		s.nextPruneAt = previousNextPruneAt
		return err
	}
	if change == nil {
		s.commitCheckpointGenerationLocked(transition)
		return nil
	}
	if err := change(); err != nil {
		s.records = snapshot
		s.nextPruneAt = previousNextPruneAt
		if restoreErr := s.rollbackCheckpointGenerationLocked(transition); restoreErr != nil {
			// The empty generation is already durable. Keep runtime state aligned
			// with it instead of reusing checkpoints that may vanish on restart.
			s.records = make(map[string]*transportCheckpointRecord)
			s.nextPruneAt = time.Time{}
			return fmt.Errorf("%w; restore transport checkpoints: %v", err, restoreErr)
		}
		return err
	}
	s.commitCheckpointGenerationLocked(transition)
	return nil
}

func (s *transportCheckpointStore) BeginFull(namespace, owner, key string, active []oaiMsg, forceNew bool) (*checkpointTurn, error) {
	return s.beginFull(namespace, owner, key, active, forceNew, nil)
}

func (s *transportCheckpointStore) BeginFullValidated(namespace, owner, key string, active []oaiMsg, forceNew bool, validateOutbound func([]oaiMsg) error) (*checkpointTurn, error) {
	return s.beginFull(namespace, owner, key, active, forceNew, validateOutbound)
}

func (s *transportCheckpointStore) beginFull(namespace, owner, key string, active []oaiMsg, forceNew bool, validateOutbound func([]oaiMsg) error) (*checkpointTurn, error) {
	if !validCheckpointText(namespace, transportCheckpointMaxNamespace) || !validCheckpointText(owner, transportCheckpointMaxIdentity) || (key != "" && !validCheckpointText(key, transportCheckpointMaxIdentity)) {
		return nil, ErrCheckpointIdentity
	}
	history, err := canonicalCheckpointMessages(active)
	if err != nil {
		return nil, err
	}
	legacyHistory, err := legacyCanonicalCheckpointMessages(active)
	if err != nil {
		return nil, err
	}
	ownerDigest := checkpointDigest(transportCheckpointOwnerDomain, owner)
	keyDigest := ""
	if key != "" {
		keyDigest = checkpointDigest(transportCheckpointKeyDomain, key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneExpiredLocked(); err != nil {
		return nil, err
	}
	snapshot := cloneTransportCheckpointRecords(s.records)

	if len(history.digests) > transportCheckpointMaxMessages {
		outbound := cloneOAIMessages(active)
		if validateOutbound != nil {
			if err := validateOutbound(outbound); err != nil {
				return nil, err
			}
		}
		rebound := forceNew
		if keyDigest != "" {
			for _, record := range s.findExplicitLocked(namespace, ownerDigest, keyDigest) {
				delete(s.records, record.ID)
				rebound = true
			}
			if rebound {
				if err := s.persistLocked(); err != nil {
					s.records = snapshot
					return nil, err
				}
			}
		}
		return &checkpointTurn{
			Outbound:  outbound,
			Rebound:   rebound,
			store:     s,
			untracked: true,
		}, nil
	}

	var selected *transportCheckpointRecord
	var discard []*transportCheckpointRecord
	rebound := forceNew
	if !forceNew {
		if keyDigest != "" {
			matches := s.findExplicitLocked(namespace, ownerDigest, keyDigest)
			if len(matches) == 1 && recordHasAnyExactPrefix(matches[0], history, legacyHistory) {
				selected = matches[0]
			} else if len(matches) > 0 {
				rebound = true
				discard = matches
			}
		} else {
			var ambiguous bool
			selected, ambiguous = s.uniqueLongestPrefixLocked(namespace, ownerDigest, history, legacyHistory)
			rebound = ambiguous || (selected == nil && s.hasOwnerNamespaceRecordLocked(namespace, ownerDigest))
		}
	} else if keyDigest != "" {
		discard = s.findExplicitLocked(namespace, ownerDigest, keyDigest)
	}

	if selected != nil {
		if selected.InFlight {
			return nil, ErrCheckpointBusy
		}
		outbound := checkpointDeltaOutbound(active, selected.AcceptedCount)
		if validateOutbound != nil {
			if err := validateOutbound(outbound); err != nil {
				return nil, err
			}
		}
		selected.InFlight = true
		selected.Revision++
		selected.UpdatedAt = s.now()
		if err := s.persistRecordLocked(selected); err != nil {
			s.records = snapshot
			return nil, err
		}
		return &checkpointTurn{
			Binding: checkpointBinding{
				ConversationID: selected.ConversationID,
				SessionID:      selected.CurrentSessionID,
			},
			Outbound:                  outbound,
			AllowedPriorToolCallIDs:   pendingToolCallIDs(selected.PendingToolCalls),
			KnownPriorToolCallDigests: append([]string(nil), selected.CompletedToolCallDigests...),
			ToolLedger:                buildAgentLedger(outbound, checkpointAgentLedger(selected)),
			store:                     s,
			recordID:                  selected.ID,
			revision:                  selected.Revision,
			baseDigests:               append([]string(nil), history.digests...),
			baseHashChain:             append([]string(nil), history.chains...),
			resolvedToolCallIDs:       checkpointToolResultIDsAfter(active, selected.AcceptedCount),
		}, nil
	}

	outbound := cloneOAIMessages(active)
	if validateOutbound != nil {
		if err := validateOutbound(outbound); err != nil {
			return nil, err
		}
	}
	structuralChange := len(discard) > 0
	for _, record := range discard {
		delete(s.records, record.ID)
	}
	evicted, err := s.makeRoomLocked(ownerDigest)
	if err != nil {
		s.records = snapshot
		return nil, err
	}
	structuralChange = structuralChange || evicted
	now := s.now()
	recordID, err := newCheckpointID()
	if err != nil {
		s.records = snapshot
		return nil, err
	}
	record := &transportCheckpointRecord{
		ID:             recordID,
		Namespace:      namespace,
		OwnerDigest:    ownerDigest,
		KeyDigest:      keyDigest,
		MessageDigests: []string{},
		HashChain:      []string{},
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision:       1,
		InFlight:       true,
	}
	s.records[record.ID] = record
	if structuralChange {
		err = s.persistLocked()
	} else {
		err = s.persistRecordLocked(record)
	}
	if err != nil {
		s.records = snapshot
		return nil, err
	}
	return &checkpointTurn{
		Outbound:            outbound,
		ToolLedger:          buildAgentLedger(outbound),
		Rebound:             rebound,
		store:               s,
		recordID:            record.ID,
		revision:            record.Revision,
		baseDigests:         append([]string(nil), history.digests...),
		baseHashChain:       append([]string(nil), history.chains...),
		resolvedToolCallIDs: checkpointToolResultIDs(active),
	}, nil
}

func (s *transportCheckpointStore) BeginDelta(namespace, owner, key string, delta []oaiMsg) (*checkpointTurn, error) {
	if !validCheckpointText(namespace, transportCheckpointMaxNamespace) || !validCheckpointText(owner, transportCheckpointMaxIdentity) {
		return nil, ErrCheckpointIdentity
	}
	if !validCheckpointText(key, transportCheckpointMaxIdentity) {
		return nil, ErrCheckpointKeyRequired
	}
	history, err := canonicalCheckpointMessages(delta)
	if err != nil {
		return nil, err
	}
	ownerDigest := checkpointDigest(transportCheckpointOwnerDomain, owner)
	keyDigest := checkpointDigest(transportCheckpointKeyDomain, key)

	s.mu.Lock()
	if err := s.pruneExpiredLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	matches := s.findExplicitLocked(namespace, ownerDigest, keyDigest)
	if len(matches) == 0 {
		s.mu.Unlock()
		return s.BeginFull(namespace, owner, key, delta, false)
	}
	if len(matches) != 1 {
		s.mu.Unlock()
		return nil, ErrCheckpointAmbiguous
	}
	turn, err := s.beginAppendLocked(matches[0], history, delta)
	s.mu.Unlock()
	return turn, err
}

func (s *transportCheckpointStore) BeginResponse(owner, parent string, delta []oaiMsg) (*checkpointTurn, error) {
	if !validCheckpointText(owner, transportCheckpointMaxIdentity) {
		return nil, ErrCheckpointIdentity
	}
	if !validCheckpointText(parent, transportCheckpointMaxIdentity) {
		return nil, ErrCheckpointUnknownCursor
	}
	history, err := canonicalCheckpointMessages(delta)
	if err != nil {
		return nil, err
	}
	ownerDigest := checkpointDigest(transportCheckpointOwnerDomain, owner)
	cursorDigest := checkpointDigest(transportCheckpointCursorDomain, parent)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneExpiredLocked(); err != nil {
		return nil, err
	}
	var matches []*transportCheckpointRecord
	for _, record := range s.records {
		if record.OwnerDigest != ownerDigest {
			continue
		}
		for _, cursor := range record.ResponseCursors {
			current := cursor.Revision == record.Revision ||
				(record.InFlight && cursor.Revision+1 == record.Revision)
			if cursor.Digest == cursorDigest && current {
				matches = append(matches, record)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil, ErrCheckpointUnknownCursor
	}
	if len(matches) != 1 {
		return nil, ErrCheckpointAmbiguous
	}
	return s.beginAppendLocked(matches[0], history, delta)
}

func (s *transportCheckpointStore) beginAppendLocked(record *transportCheckpointRecord, delta checkpointHistory, outbound []oaiMsg) (*checkpointTurn, error) {
	if record.InFlight {
		return nil, ErrCheckpointBusy
	}
	if len(record.MessageDigests)+len(delta.digests) > transportCheckpointMaxMessages {
		delete(s.records, record.ID)
		if err := s.deleteRecordLocked(record.ID); err != nil {
			s.records[record.ID] = record
			return nil, err
		}
		s.recomputeNextPruneAtLocked()
		return nil, ErrCheckpointHistoryLimit
	}
	snapshot := cloneTransportCheckpointRecords(s.records)
	digests := append(append([]string(nil), record.MessageDigests...), delta.digests...)
	chains, err := extendCheckpointHashChain(record.HashChain, delta.digests)
	if err != nil {
		return nil, err
	}
	record.InFlight = true
	record.Revision++
	record.UpdatedAt = s.now()
	if err := s.persistRecordLocked(record); err != nil {
		s.records = snapshot
		return nil, err
	}
	return &checkpointTurn{
		Binding: checkpointBinding{
			ConversationID: record.ConversationID,
			SessionID:      record.CurrentSessionID,
		},
		Outbound:                  cloneOAIMessages(outbound),
		AllowedPriorToolCallIDs:   pendingToolCallIDs(record.PendingToolCalls),
		KnownPriorToolCallDigests: append([]string(nil), record.CompletedToolCallDigests...),
		ToolLedger:                buildAgentLedger(outbound, checkpointAgentLedger(record)),
		store:                     s,
		recordID:                  record.ID,
		revision:                  record.Revision,
		baseDigests:               digests,
		baseHashChain:             chains,
		resolvedToolCallIDs:       checkpointToolResultIDs(outbound),
	}, nil
}

func (s *transportCheckpointStore) accept(turn *checkpointTurn, binding checkpointBinding, produced []oaiMsg, responseID string) error {
	producedHistory, err := canonicalCheckpointMessages(produced)
	if err != nil {
		if abortErr := s.abort(turn); abortErr != nil && !errors.Is(abortErr, ErrCheckpointTurnClosed) {
			return errors.Join(err, abortErr)
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn.closed {
		return ErrCheckpointTurnClosed
	}
	if turn.untracked {
		turn.closed = true
		return nil
	}
	record, ok := s.records[turn.recordID]
	if !ok || !record.InFlight || record.Revision != turn.revision {
		turn.closed = true
		return ErrCheckpointAttemptStale
	}
	if !validCheckpointText(binding.ConversationID, transportCheckpointMaxIdentity) || (binding.SessionID != "" && !validCheckpointText(binding.SessionID, transportCheckpointMaxIdentity)) || (responseID != "" && !validCheckpointText(responseID, transportCheckpointMaxIdentity)) {
		return s.invalidateTurnLocked(turn, ErrCheckpointInvalidBinding)
	}
	if record.ConversationID != "" && record.ConversationID != binding.ConversationID {
		return s.invalidateTurnLocked(turn, ErrCheckpointConversationDrift)
	}
	if len(turn.baseDigests)+len(producedHistory.digests) > transportCheckpointMaxMessages {
		return s.invalidateTurnLocked(turn, ErrCheckpointHistoryLimit)
	}

	allDigests := append(append([]string(nil), turn.baseDigests...), producedHistory.digests...)
	allChains, err := extendCheckpointHashChain(turn.baseHashChain, producedHistory.digests)
	if err != nil {
		return s.invalidateTurnLocked(turn, err)
	}
	previousSession := record.CurrentSessionID
	record.ConversationID = binding.ConversationID
	if binding.SessionID != "" && binding.SessionID != record.CurrentSessionID {
		record.LastSessionID = previousSession
		record.CurrentSessionID = binding.SessionID
	}
	record.MessageDigests = allDigests
	record.HashChain = allChains
	record.AcceptedCount = len(allDigests)
	pending, err := advancePendingToolCalls(record.PendingToolCalls, turn.resolvedToolCallIDs, produced, turn.ToolLedger.Completed)
	if err != nil {
		return s.invalidateTurnLocked(turn, err)
	}
	record.PendingToolCalls = pending
	record.CompletedToolCallDigests = mergeCompletedToolCallDigests(record.CompletedToolCallDigests, turn.ToolLedger.Completed)
	record.CompletedToolIdentityDigests = mergeCompletedToolIdentityDigests(record.CompletedToolIdentityDigests, turn.ToolLedger.Completed)
	if len(record.CompletedToolCallDigests) > transportCheckpointMaxCompletedToolCallDigests || len(record.CompletedToolIdentityDigests) > transportCheckpointMaxCompletedToolCallDigests {
		return s.invalidateTurnLocked(turn, ErrCheckpointHistoryLimit)
	}
	record.CompletedToolEvidence = append([]toolEvidence(nil), turn.ToolLedger.Completed...)
	if len(record.CompletedToolEvidence) > transportCheckpointMaxToolEvidence {
		record.CompletedToolEvidence = append([]toolEvidence(nil), record.CompletedToolEvidence[len(record.CompletedToolEvidence)-transportCheckpointMaxToolEvidence:]...)
	}
	if responseID != "" {
		cursor := checkpointResponseCursor{
			Digest:   checkpointDigest(transportCheckpointCursorDomain, responseID),
			Revision: record.Revision + 1,
		}
		if !checkpointContainsCursor(record.ResponseCursors, cursor) {
			record.ResponseCursors = append(record.ResponseCursors, cursor)
			if len(record.ResponseCursors) > transportCheckpointMaxCursors {
				record.ResponseCursors = append([]checkpointResponseCursor(nil), record.ResponseCursors[len(record.ResponseCursors)-transportCheckpointMaxCursors:]...)
			}
		}
	}
	record.InFlight = false
	record.Revision++
	record.UpdatedAt = s.now()
	if err := s.persistRecordLocked(record); err != nil {
		delete(s.records, record.ID)
		turn.closed = true
		_ = s.deleteRecordLocked(record.ID)
		s.recomputeNextPruneAtLocked()
		return err
	}
	turn.closed = true
	return nil
}

func (s *transportCheckpointStore) abort(turn *checkpointTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn.closed {
		return ErrCheckpointTurnClosed
	}
	turn.closed = true
	if turn.untracked {
		return nil
	}
	record, ok := s.records[turn.recordID]
	if !ok || !record.InFlight || record.Revision != turn.revision {
		return ErrCheckpointAttemptStale
	}
	delete(s.records, record.ID)
	if err := s.deleteRecordLocked(record.ID); err != nil {
		return err
	}
	s.recomputeNextPruneAtLocked()
	return nil
}

func (s *transportCheckpointStore) invalidateTurnLocked(turn *checkpointTurn, cause error) error {
	delete(s.records, turn.recordID)
	turn.closed = true
	if err := s.deleteRecordLocked(turn.recordID); err != nil {
		return errors.Join(cause, err)
	}
	s.recomputeNextPruneAtLocked()
	return cause
}

func (s *transportCheckpointStore) findExplicitLocked(namespace, ownerDigest, keyDigest string) []*transportCheckpointRecord {
	var matches []*transportCheckpointRecord
	for _, record := range s.records {
		if record.Namespace == namespace && record.OwnerDigest == ownerDigest && record.KeyDigest == keyDigest {
			matches = append(matches, record)
		}
	}
	return matches
}

func (s *transportCheckpointStore) hasOwnerNamespaceRecordLocked(namespace, ownerDigest string) bool {
	for _, record := range s.records {
		if record.Namespace == namespace && record.OwnerDigest == ownerDigest {
			return true
		}
	}
	return false
}

func (s *transportCheckpointStore) uniqueLongestPrefixLocked(namespace, ownerDigest string, histories ...checkpointHistory) (*transportCheckpointRecord, bool) {
	longest := -1
	var selected *transportCheckpointRecord
	ambiguous := false
	for _, record := range s.records {
		if record.Namespace != namespace || record.OwnerDigest != ownerDigest || record.AcceptedCount == 0 || !recordHasAnyExactPrefix(record, histories...) {
			continue
		}
		if record.AcceptedCount > longest {
			longest = record.AcceptedCount
			selected = record
			ambiguous = false
		} else if record.AcceptedCount == longest {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, true
	}
	return selected, false
}

func (s *transportCheckpointStore) makeRoomLocked(ownerDigest string) (bool, error) {
	if len(s.records) < transportCheckpointMaxRecords {
		return false, nil
	}
	var ownerOldest *transportCheckpointRecord
	var globalOldest *transportCheckpointRecord
	for _, record := range s.records {
		if record.InFlight {
			continue
		}
		if globalOldest == nil || record.UpdatedAt.Before(globalOldest.UpdatedAt) {
			globalOldest = record
		}
		if record.OwnerDigest == ownerDigest && (ownerOldest == nil || record.UpdatedAt.Before(ownerOldest.UpdatedAt)) {
			ownerOldest = record
		}
	}
	oldest := ownerOldest
	if oldest == nil {
		oldest = globalOldest
	}
	if oldest == nil {
		return false, ErrCheckpointCapacity
	}
	delete(s.records, oldest.ID)
	return true, nil
}

func (s *transportCheckpointStore) pruneExpiredLocked() error {
	now := s.now()
	if !s.nextPruneAt.IsZero() && now.Before(s.nextPruneAt) {
		return nil
	}
	snapshot := cloneTransportCheckpointRecords(s.records)
	changed := false
	for id, record := range s.records {
		if transportCheckpointExpired(record, now) {
			delete(s.records, id)
			changed = true
		}
	}
	if !changed {
		s.recomputeNextPruneAtLocked()
		return nil
	}
	if err := s.persistLocked(); err != nil {
		s.records = snapshot
		s.recomputeNextPruneAtLocked()
		return err
	}
	return nil
}

func transportCheckpointExpired(record *transportCheckpointRecord, now time.Time) bool {
	return record == nil || !now.Before(record.UpdatedAt.Add(transportCheckpointTTL))
}

func (s *transportCheckpointStore) persistLocked() error {
	return s.persistAllLocked()
}

func decodeTransportCheckpointFile(b []byte) (transportCheckpointFile, error) {
	var file transportCheckpointFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return file, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return file, err
	}
	return file, nil
}

func validTransportCheckpointRecord(record *transportCheckpointRecord) bool {
	if record == nil || !validCheckpointText(record.ID, 64) || !validCheckpointText(record.Namespace, transportCheckpointMaxNamespace) || record.OwnerDigest == "" || !validCheckpointText(record.ConversationID, transportCheckpointMaxIdentity) || (record.CurrentSessionID != "" && !validCheckpointText(record.CurrentSessionID, transportCheckpointMaxIdentity)) || (record.LastSessionID != "" && !validCheckpointText(record.LastSessionID, transportCheckpointMaxIdentity)) || record.AcceptedCount < 0 || record.Revision == 0 || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return false
	}
	if !validCheckpointDigest(record.OwnerDigest) || (record.KeyDigest != "" && !validCheckpointDigest(record.KeyDigest)) {
		return false
	}
	if record.AcceptedCount != len(record.MessageDigests) || len(record.MessageDigests) != len(record.HashChain) || len(record.MessageDigests) > transportCheckpointMaxMessages || len(record.ResponseCursors) > transportCheckpointMaxCursors || len(record.PendingToolCalls) > transportCheckpointMaxToolCalls || len(record.CompletedToolEvidence) > transportCheckpointMaxToolEvidence || len(record.CompletedToolCallDigests) > transportCheckpointMaxCompletedToolCallDigests || len(record.CompletedToolIdentityDigests) > transportCheckpointMaxCompletedToolCallDigests {
		return false
	}
	for _, digest := range record.MessageDigests {
		if !validCheckpointDigest(digest) {
			return false
		}
	}
	for _, cursor := range record.ResponseCursors {
		if !validCheckpointDigest(cursor.Digest) || cursor.Revision == 0 || cursor.Revision > record.Revision {
			return false
		}
	}
	pendingIDs := make(map[string]struct{}, len(record.PendingToolCalls))
	for _, pending := range record.PendingToolCalls {
		if !validCheckpointText(pending.CallID, transportCheckpointMaxIdentity) || !validCheckpointText(pending.Name, transportCheckpointMaxToolName) || !validCheckpointDigest(pending.ArgumentsDigest) {
			return false
		}
		if _, duplicate := pendingIDs[pending.CallID]; duplicate {
			return false
		}
		pendingIDs[pending.CallID] = struct{}{}
	}
	completedIDs := make(map[string]struct{}, len(record.CompletedToolEvidence))
	completedDigests := make(map[string]struct{}, len(record.CompletedToolCallDigests))
	for _, digest := range record.CompletedToolCallDigests {
		if !validCheckpointDigest(digest) {
			return false
		}
		if _, duplicate := completedDigests[digest]; duplicate {
			return false
		}
		completedDigests[digest] = struct{}{}
	}
	completedIdentityDigests := make(map[string]struct{}, len(record.CompletedToolIdentityDigests))
	for _, digest := range record.CompletedToolIdentityDigests {
		if !validCheckpointDigest(digest) {
			return false
		}
		if _, duplicate := completedIdentityDigests[digest]; duplicate {
			return false
		}
		completedIdentityDigests[digest] = struct{}{}
	}
	if len(completedDigests) > 0 && len(completedIdentityDigests) == 0 {
		return false
	}
	for _, pending := range record.PendingToolCalls {
		if _, completed := completedDigests[toolCallIDDigest(pending.CallID)]; completed {
			return false
		}
		if _, completed := completedIdentityDigests[toolCallIdentityDigest(pending.Name, pending.ArgumentsDigest)]; completed {
			return false
		}
	}
	for _, evidence := range record.CompletedToolEvidence {
		if !validCheckpointText(evidence.ID, transportCheckpointMaxIdentity) || !validCheckpointText(evidence.Name, transportCheckpointMaxToolName) || !validCheckpointDigest(evidence.ArgumentsSHA256) || !validCheckpointDigest(evidence.ResultSHA256) || evidence.ResultLength < 0 || len(evidence.Preview) > toolResultPreviewBytes || !utf8.ValidString(evidence.Preview) {
			return false
		}
		if _, duplicate := completedIDs[evidence.ID]; duplicate {
			return false
		}
		if _, stillPending := pendingIDs[evidence.ID]; stillPending {
			return false
		}
		if _, retained := completedDigests[toolCallIDDigest(evidence.ID)]; !retained {
			return false
		}
		if _, retained := completedIdentityDigests[toolCallIdentityDigest(evidence.Name, evidence.ArgumentsSHA256)]; !retained {
			return false
		}
		completedIDs[evidence.ID] = struct{}{}
	}
	chains, err := extendCheckpointHashChain(nil, record.MessageDigests)
	if err != nil || !equalStrings(chains, record.HashChain) {
		return false
	}
	return true
}

type checkpointHistory struct {
	digests []string
	chains  []string
}

func canonicalCheckpointMessages(messages []oaiMsg) (checkpointHistory, error) {
	return canonicalCheckpointMessagesWithLegacyToolNames(messages, false)
}

func legacyCanonicalCheckpointMessages(messages []oaiMsg) (checkpointHistory, error) {
	return canonicalCheckpointMessagesWithLegacyToolNames(messages, true)
}

func canonicalCheckpointMessagesWithLegacyToolNames(messages []oaiMsg, legacy bool) (checkpointHistory, error) {
	digests := make([]string, 0, len(messages))
	toolNames := make(map[string]string)
	for _, message := range messages {
		if message.SidecarGenerated {
			continue
		}
		toolName := ""
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if legacy && role == "tool" {
			toolName = message.Name
			if toolName == "" {
				toolName = toolNames[message.ToolCallID]
			}
		}
		canonical, err := canonicalCheckpointMessageWithToolName(message, toolName)
		if err != nil {
			return checkpointHistory{}, err
		}
		h := sha256.New()
		_, _ = io.WriteString(h, transportCheckpointHashDomain)
		_, _ = h.Write(canonical)
		digests = append(digests, hex.EncodeToString(h.Sum(nil)))
		for _, call := range message.ToolCalls {
			id, _ := call["id"].(string)
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			if id != "" && name != "" {
				toolNames[id] = name
			}
		}
	}
	chains, err := extendCheckpointHashChain(nil, digests)
	if err != nil {
		return checkpointHistory{}, err
	}
	return checkpointHistory{digests: digests, chains: chains}, nil
}

type canonicalCheckpointMessageEnvelope struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
}

type canonicalCheckpointToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func canonicalCheckpointMessage(message oaiMsg) ([]byte, error) {
	return canonicalCheckpointMessageWithToolName(message, "")
}

func canonicalCheckpointMessageWithToolName(message oaiMsg, toolName string) ([]byte, error) {
	content, err := canonicalCheckpointValue(message.Content)
	if err != nil {
		return nil, fmt.Errorf("%w: content: %v", ErrCheckpointCanonicalization, err)
	}
	toolCalls, err := canonicalCheckpointToolCalls(message.ToolCalls)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role == "" {
		role = "user"
	}
	name := message.Name
	if role == "tool" {
		// Clients may omit this redundant field when reloading tool results.
		name = toolName
	}
	return json.Marshal(canonicalCheckpointMessageEnvelope{
		Role:       role,
		Content:    content,
		Name:       name,
		ToolCallID: message.ToolCallID,
		ToolCalls:  toolCalls,
	})
}

func canonicalCheckpointToolCalls(toolCalls []map[string]any) (json.RawMessage, error) {
	if toolCalls == nil {
		return json.RawMessage("null"), nil
	}
	canonical := make([]canonicalCheckpointToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		id, ok := call["id"].(string)
		if !ok && call["id"] != nil {
			return nil, fmt.Errorf("%w: tool call id", ErrCheckpointCanonicalization)
		}
		typ, ok := call["type"].(string)
		if !ok && call["type"] != nil {
			return nil, fmt.Errorf("%w: tool call type", ErrCheckpointCanonicalization)
		}
		fn, ok := call["function"].(map[string]any)
		if !ok && call["function"] != nil {
			return nil, fmt.Errorf("%w: tool call function", ErrCheckpointCanonicalization)
		}
		name, ok := fn["name"].(string)
		if !ok && fn != nil && fn["name"] != nil {
			return nil, fmt.Errorf("%w: tool call name", ErrCheckpointCanonicalization)
		}
		arguments, err := canonicalCheckpointArguments(fn["arguments"])
		if err != nil {
			return nil, fmt.Errorf("%w: tool call arguments: %v", ErrCheckpointCanonicalization, err)
		}
		canonical = append(canonical, canonicalCheckpointToolCall{ID: id, Type: typ, Name: name, Arguments: arguments})
	}
	b, err := json.Marshal(canonical)
	return json.RawMessage(b), err
}

func canonicalCheckpointArguments(value any) (json.RawMessage, error) {
	if text, ok := value.(string); ok {
		decoded, err := decodeCheckpointJSON([]byte(text))
		if err == nil {
			b, marshalErr := json.Marshal(decoded)
			return json.RawMessage(b), marshalErr
		}
	}
	return canonicalCheckpointValue(value)
}

func canonicalCheckpointValue(value any) (json.RawMessage, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeCheckpointJSON(b)
	if err != nil {
		return nil, err
	}
	b, err = json.Marshal(decoded)
	return json.RawMessage(b), err
}

func decodeCheckpointJSON(b []byte) (any, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	return value, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func extendCheckpointHashChain(existing, digests []string) ([]string, error) {
	chains := append([]string(nil), existing...)
	previous := sha256.Sum256([]byte(transportCheckpointChainDomain))
	if len(chains) > 0 {
		decoded, err := hex.DecodeString(chains[len(chains)-1])
		if err != nil || len(decoded) != sha256.Size {
			return nil, ErrCheckpointCanonicalization
		}
		copy(previous[:], decoded)
	}
	for _, digest := range digests {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return nil, ErrCheckpointCanonicalization
		}
		h := sha256.New()
		_, _ = h.Write(previous[:])
		_, _ = h.Write(decoded)
		sum := h.Sum(nil)
		chains = append(chains, hex.EncodeToString(sum))
		copy(previous[:], sum)
	}
	return chains, nil
}

func recordHasExactPrefix(record *transportCheckpointRecord, digests, chains []string) bool {
	if record == nil || record.AcceptedCount > len(digests) || record.AcceptedCount > len(chains) {
		return false
	}
	return equalStrings(record.MessageDigests, digests[:record.AcceptedCount]) &&
		equalStrings(record.HashChain, chains[:record.AcceptedCount])
}

func recordHasAnyExactPrefix(record *transportCheckpointRecord, histories ...checkpointHistory) bool {
	for _, history := range histories {
		if recordHasExactPrefix(record, history.digests, history.chains) {
			return true
		}
	}
	return false
}

func checkpointDeltaOutbound(active []oaiMsg, acceptedCount int) []oaiMsg {
	out := make([]oaiMsg, 0, len(active))
	ordinal := 0
	for _, message := range active {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "user"
		}
		include := message.SidecarGenerated || role == "system" || role == "developer" || ordinal >= acceptedCount
		if include {
			out = append(out, message)
		}
		if !message.SidecarGenerated {
			ordinal++
		}
	}
	return out
}

func checkpointDigest(domain, value string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, domain)
	_, _ = io.WriteString(h, value)
	return hex.EncodeToString(h.Sum(nil))
}

func validCheckpointDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size
}

func newCheckpointID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create transport checkpoint ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func cloneOAIMessages(messages []oaiMsg) []oaiMsg {
	return append([]oaiMsg(nil), messages...)
}

func cloneTransportCheckpointRecord(record *transportCheckpointRecord) *transportCheckpointRecord {
	if record == nil {
		return nil
	}
	clone := *record
	clone.MessageDigests = append([]string(nil), record.MessageDigests...)
	clone.HashChain = append([]string(nil), record.HashChain...)
	clone.ResponseCursors = append([]checkpointResponseCursor(nil), record.ResponseCursors...)
	clone.PendingToolCalls = append([]checkpointPendingToolCall(nil), record.PendingToolCalls...)
	clone.CompletedToolEvidence = append([]toolEvidence(nil), record.CompletedToolEvidence...)
	clone.CompletedToolCallDigests = append([]string(nil), record.CompletedToolCallDigests...)
	clone.CompletedToolIdentityDigests = append([]string(nil), record.CompletedToolIdentityDigests...)
	return &clone
}

func cloneTransportCheckpointRecords(records map[string]*transportCheckpointRecord) map[string]*transportCheckpointRecord {
	clone := make(map[string]*transportCheckpointRecord, len(records))
	for id, record := range records {
		clone[id] = cloneTransportCheckpointRecord(record)
	}
	return clone
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkpointContainsCursor(values []checkpointResponseCursor, want checkpointResponseCursor) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func pendingToolCallIDs(pending []checkpointPendingToolCall) []string {
	ids := make([]string, 0, len(pending))
	for _, call := range pending {
		ids = append(ids, call.CallID)
	}
	return ids
}

func completedToolCallDigests(completed []toolEvidence) []string {
	digests := make([]string, 0, len(completed))
	for _, evidence := range completed {
		digests = append(digests, toolCallIDDigest(evidence.ID))
	}
	return digests
}

func completedToolIdentityDigests(completed []toolEvidence) []string {
	digests := make([]string, 0, len(completed))
	for _, evidence := range completed {
		digests = append(digests, toolCallIdentityDigest(evidence.Name, evidence.ArgumentsSHA256))
	}
	return digests
}

func mergeCompletedToolCallDigests(existing []string, completed []toolEvidence) []string {
	merged := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(existing)+len(completed))
	for _, digest := range existing {
		seen[digest] = struct{}{}
	}
	for _, evidence := range completed {
		digest := toolCallIDDigest(evidence.ID)
		if _, ok := seen[digest]; ok {
			continue
		}
		seen[digest] = struct{}{}
		merged = append(merged, digest)
	}
	return merged
}

func mergeCompletedToolIdentityDigests(existing []string, completed []toolEvidence) []string {
	merged := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(existing)+len(completed))
	for _, digest := range existing {
		seen[digest] = struct{}{}
	}
	for _, evidence := range completed {
		digest := toolCallIdentityDigest(evidence.Name, evidence.ArgumentsSHA256)
		if _, ok := seen[digest]; ok {
			continue
		}
		seen[digest] = struct{}{}
		merged = append(merged, digest)
	}
	return merged
}

func toolCallIDDigest(id string) string {
	return checkpointDigest(transportCheckpointCallIDDomain, id)
}

func checkpointAgentLedger(record *transportCheckpointRecord) agentLedger {
	if record == nil {
		return agentLedger{}
	}
	ledger := agentLedger{
		Completed:        append([]toolEvidence(nil), record.CompletedToolEvidence...),
		KnownCallDigests: append([]string(nil), record.CompletedToolIdentityDigests...),
	}
	for i := range ledger.Completed {
		ledger.Completed[i].hasResult = true
	}
	for _, pending := range record.PendingToolCalls {
		ledger.Pending = append(ledger.Pending, toolEvidence{
			ID: pending.CallID, Name: pending.Name, ArgumentsSHA256: pending.ArgumentsDigest,
		})
	}
	return ledger
}

// checkpointExecutionLedger preserves the durable checkpoint ledger while
// deriving the evidence view used for one model execution. A newly appended
// user message starts a fresh execution scope: completed operation identities
// from older turns must not prove or suppress execution in the new turn.
// Unresolved pending calls remain safety evidence because repeating an
// operation whose outcome is still unknown requires an explicit recovery
// decision.
func checkpointExecutionLedger(ledger agentLedger, outbound []oaiMsg) agentLedger {
	for _, message := range outbound {
		if message.SidecarGenerated || !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		priorPending := agentLedger{Pending: append([]toolEvidence(nil), ledger.Pending...)}
		return buildAgentLedger(outbound, priorPending)
	}
	return ledger
}

func checkpointToolResultIDs(messages []oaiMsg) []string {
	return checkpointToolResultIDsAfter(messages, 0)
}

func checkpointToolResultIDsAfter(messages []oaiMsg, acceptedCount int) []string {
	ids := make([]string, 0)
	ordinal := 0
	for _, message := range messages {
		if message.SidecarGenerated {
			continue
		}
		if ordinal >= acceptedCount && strings.EqualFold(strings.TrimSpace(message.Role), "tool") && message.ToolCallID != "" {
			ids = append(ids, message.ToolCallID)
		}
		ordinal++
	}
	return ids
}

func advancePendingToolCalls(existing []checkpointPendingToolCall, resolved []string, produced []oaiMsg, completed ...[]toolEvidence) ([]checkpointPendingToolCall, error) {
	resolvedSet := make(map[string]struct{}, len(resolved))
	for _, id := range resolved {
		resolvedSet[id] = struct{}{}
	}
	pending := make([]checkpointPendingToolCall, 0, len(existing))
	seen := make(map[string]struct{}, len(existing))
	if len(completed) > 0 {
		for _, evidence := range completed[0] {
			seen[evidence.ID] = struct{}{}
		}
	}
	for _, call := range existing {
		if _, ok := resolvedSet[call.CallID]; ok {
			continue
		}
		if _, duplicate := seen[call.CallID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate pending tool call ID", ErrCheckpointCanonicalization)
		}
		pending = append(pending, call)
		seen[call.CallID] = struct{}{}
	}
	for _, message := range produced {
		if message.SidecarGenerated {
			continue
		}
		for _, call := range message.ToolCalls {
			id, _ := call["id"].(string)
			fn, _ := call["function"].(map[string]any)
			name, _ := fn["name"].(string)
			if !validCheckpointText(id, transportCheckpointMaxIdentity) || !validCheckpointText(name, transportCheckpointMaxToolName) {
				return nil, fmt.Errorf("%w: pending tool call identity", ErrCheckpointCanonicalization)
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, fmt.Errorf("%w: duplicate pending tool call ID", ErrCheckpointCanonicalization)
			}
			arguments, err := canonicalCheckpointArguments(fn["arguments"])
			if err != nil {
				return nil, fmt.Errorf("%w: pending tool call arguments: %v", ErrCheckpointCanonicalization, err)
			}
			pending = append(pending, checkpointPendingToolCall{
				CallID:          id,
				Name:            name,
				ArgumentsDigest: toolArgumentsSHA256(string(arguments)),
			})
			seen[id] = struct{}{}
		}
	}
	if len(pending) > transportCheckpointMaxToolCalls {
		return nil, ErrCheckpointHistoryLimit
	}
	return pending, nil
}

func secureCheckpointPath(path string) error {
	dir := filepath.Dir(path)
	if err := secureCheckpointDirectory(dir); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("%w: secure file: %v", ErrCheckpointPersistence, err)
	}
	return nil
}

func writeCheckpointFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := secureCheckpointDirectory(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".transport-checkpoint-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func secureCheckpointDirectory(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("%w: resolve directory: %v", ErrCheckpointPersistence, err)
	}
	if checkpointDirectoryIsBroad(abs) {
		return fmt.Errorf("%w: checkpoint file requires a dedicated private directory", ErrCheckpointPersistence)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("%w: create directory: %v", ErrCheckpointPersistence, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("%w: resolve directory links: %v", ErrCheckpointPersistence, err)
	}
	if checkpointDirectoryIsBroad(resolved) {
		return fmt.Errorf("%w: checkpoint file requires a dedicated private directory", ErrCheckpointPersistence)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		return fmt.Errorf("%w: secure directory: %v", ErrCheckpointPersistence, err)
	}
	return nil
}

func checkpointDirectoryIsBroad(dir string) bool {
	unsafe := map[string]struct{}{
		string(filepath.Separator): {},
	}
	if cwd, err := os.Getwd(); err == nil {
		unsafe[filepath.Clean(cwd)] = struct{}{}
	}
	if home, err := os.UserHomeDir(); err == nil {
		unsafe[filepath.Clean(home)] = struct{}{}
	}
	unsafe[filepath.Clean(os.TempDir())] = struct{}{}
	_, broad := unsafe[filepath.Clean(dir)]
	return broad
}

func validCheckpointText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum
}

func readCheckpointFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return nil, err
	} else if info.Size() > transportCheckpointMaxFileBytes {
		return nil, ErrCheckpointCapacity
	}
	b, err := io.ReadAll(io.LimitReader(file, transportCheckpointMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > transportCheckpointMaxFileBytes {
		return nil, ErrCheckpointCapacity
	}
	return b, nil
}
