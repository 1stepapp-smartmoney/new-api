package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	// ClientGoneDetected records that the downstream client disconnected
	// during the stream. EndReason is set to ClientGone at the same time;
	// these two fields together let support tell apart "client got the
	// full response" from "client gave up early after N chunks / M ms".
	clientGoneOnce     sync.Once
	ClientGoneDetected bool
	ClientGoneAtChunks int
	// ClientGoneAtMs is the elapsed milliseconds between the request start
	// (info.StartTime) and the moment the downstream client disconnected.
	// Useful for reconciling "client gave up after N seconds but upstream
	// kept going for another M seconds" scenarios.
	ClientGoneAtMs  int64
	ClientGoneError error

	mu         sync.Mutex
	Errors     []StreamErrorEntry
	ErrorCount int
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.EndReason = reason
		s.EndError = err
	})
}

// MarkClientGone idempotently flags that the downstream client disconnected
// mid-stream. atChunks should be info.ReceivedResponseCount at the moment of
// detection — it captures how much the client actually received before going
// away. atMs is the elapsed milliseconds since the request started (pass 0
// if unknown). Callers typically pair this with SetEndReason(ClientGone) so
// the two fields together let support reconcile "client got N chunks over
// M ms before disconnecting".
func (s *StreamStatus) MarkClientGone(atChunks int, atMs int64, err error) {
	if s == nil {
		return
	}
	s.clientGoneOnce.Do(func() {
		s.ClientGoneDetected = true
		s.ClientGoneAtChunks = atChunks
		s.ClientGoneAtMs = atMs
		s.ClientGoneError = err
	})
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	s.mu.Lock()
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	s.mu.Unlock()
	return b.String()
}
