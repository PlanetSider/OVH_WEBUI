package proxyguard

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	DefaultFailureThreshold = 3
	failureWindow           = 2 * time.Minute
	notificationCooldown    = 30 * time.Minute
)

type EventKind string

const (
	EventNone     EventKind = ""
	EventTrip     EventKind = "trip"
	EventReminder EventKind = "reminder"
	EventRecovery EventKind = "recovery"
)

type Status struct {
	AccountID           string    `json:"accountId"`
	Paused              bool      `json:"paused"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	LastError           string    `json:"lastError,omitempty"`
	LastFailureAt       time.Time `json:"lastFailureAt,omitempty"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitempty"`
	TrippedAt           time.Time `json:"trippedAt,omitempty"`
}

// Event is the atomic result of observing one account-level proxy outcome.
// Callers use the kind to perform side effects exactly once per transition.
type Event struct {
	Kind     EventKind
	Status   Status
	Duration time.Duration
}

type Guard struct {
	mu         sync.RWMutex
	threshold  int
	statuses   map[string]Status
	lastNotify map[string]time.Time
}

func New(threshold int) *Guard {
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	return &Guard{threshold: threshold, statuses: make(map[string]Status), lastNotify: make(map[string]time.Time)}
}

// ReportFailure records a failure and keeps the legacy void API for callers
// that only need the state transition.
func (g *Guard) ReportFailure(accountID string, cause error) {
	_ = g.ObserveFailure(accountID, cause)
}

// FailureStatus records a failure and returns the resulting status.
func (g *Guard) FailureStatus(accountID string, cause error) Status {
	return g.ObserveFailure(accountID, cause).Status
}

// ObserveFailure atomically evaluates the failure window and notification
// cooldown. Only one concurrent caller can receive EventTrip for a transition.
func (g *Guard) ObserveFailure(accountID string, cause error) Event {
	accountID = strings.TrimSpace(accountID)
	if g == nil || accountID == "" {
		return Event{Status: Status{AccountID: accountID}}
	}
	now := time.Now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	status := g.statuses[accountID]
	status.AccountID = accountID
	if !status.LastFailureAt.IsZero() && now.Sub(status.LastFailureAt) > failureWindow {
		status.ConsecutiveFailures = 0
	}
	status.ConsecutiveFailures++
	status.LastFailureAt = now
	status.LastError = sanitizeError(cause)

	event := Event{Kind: EventNone}
	if status.ConsecutiveFailures >= g.threshold && !status.Paused {
		status.Paused = true
		status.TrippedAt = now
		event.Kind = EventTrip
	} else if status.Paused {
		lastNotify := g.lastNotify[accountID]
		if lastNotify.IsZero() || now.Sub(lastNotify) >= notificationCooldown {
			event.Kind = EventReminder
		}
	}
	if event.Kind == EventTrip || event.Kind == EventReminder {
		g.lastNotify[accountID] = now
	}
	g.statuses[accountID] = status
	event.Status = status
	return event
}

// ReportSuccess records a successful request and keeps the legacy void API.
func (g *Guard) ReportSuccess(accountID string) {
	_ = g.ObserveSuccess(accountID)
}

// SuccessStatus records a successful request and returns the resulting status.
func (g *Guard) SuccessStatus(accountID string) Status {
	return g.ObserveSuccess(accountID).Status
}

// ObserveSuccess clears the failure window. A recovery event is emitted only
// when the account had actually been tripped, including a state seeded after a
// process restart from persisted proxyguard markers.
func (g *Guard) ObserveSuccess(accountID string) Event {
	accountID = strings.TrimSpace(accountID)
	if g == nil || accountID == "" {
		return Event{Status: Status{AccountID: accountID}}
	}
	now := time.Now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	status, exists := g.statuses[accountID]
	if !exists {
		status.AccountID = accountID
	}
	wasTripped := status.Paused || !status.TrippedAt.IsZero()
	duration := time.Duration(0)
	if wasTripped && !status.TrippedAt.IsZero() {
		duration = time.Since(status.TrippedAt)
		if duration < 0 {
			duration = 0
		}
	}
	status.AccountID = accountID
	status.Paused = false
	status.ConsecutiveFailures = 0
	status.LastError = ""
	status.LastSuccessAt = now
	status.TrippedAt = time.Time{}
	delete(g.lastNotify, accountID)
	g.statuses[accountID] = status
	event := Event{Kind: EventNone, Status: status, Duration: duration}
	if wasTripped {
		event.Kind = EventRecovery
	}
	return event
}

// SeedTripped restores the in-memory guard gate from persisted business
// markers. It does not emit a notification or mutate any business state.
func (g *Guard) SeedTripped(accountID string, at time.Time) {
	accountID = strings.TrimSpace(accountID)
	if g == nil || accountID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	g.mu.Lock()
	status := g.statuses[accountID]
	status.AccountID = accountID
	status.Paused = true
	if status.TrippedAt.IsZero() {
		status.TrippedAt = at
	}
	g.statuses[accountID] = status
	g.mu.Unlock()
}

func (g *Guard) Reset(accountID string) Status {
	return g.SuccessStatus(accountID)
}

func (g *Guard) IsPaused(accountID string) bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	status := g.statuses[strings.TrimSpace(accountID)]
	g.mu.RUnlock()
	return status.Paused
}

func (g *Guard) Get(accountID string) (Status, bool) {
	if g == nil {
		return Status{}, false
	}
	g.mu.RLock()
	status, ok := g.statuses[strings.TrimSpace(accountID)]
	g.mu.RUnlock()
	return status, ok
}

func (g *Guard) Snapshot() []Status {
	if g == nil {
		return []Status{}
	}
	g.mu.RLock()
	out := make([]Status, 0, len(g.statuses))
	for _, status := range g.statuses {
		out = append(out, status)
	}
	g.mu.RUnlock()
	return out
}

var credentialURLPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)

func sanitizeError(err error) string {
	if err == nil {
		return "proxy request failed"
	}
	message := strings.TrimSpace(err.Error())
	message = credentialURLPattern.ReplaceAllString(message, `${1}[redacted]@`)
	if len(message) > 240 {
		message = message[:240]
	}
	return fmt.Sprintf("%s", message)
}
