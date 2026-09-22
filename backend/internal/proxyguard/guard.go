package proxyguard

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const DefaultFailureThreshold = 3

type Status struct {
	AccountID           string    `json:"accountId"`
	Paused              bool      `json:"paused"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	LastError           string    `json:"lastError,omitempty"`
	LastFailureAt       time.Time `json:"lastFailureAt,omitempty"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitempty"`
}

type Guard struct {
	mu        sync.RWMutex
	threshold int
	statuses  map[string]Status
}

func New(threshold int) *Guard {
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	return &Guard{threshold: threshold, statuses: make(map[string]Status)}
}

func (g *Guard) ReportFailure(accountID string, cause error) {
	g.recordFailure(accountID, cause)
}

func (g *Guard) FailureStatus(accountID string, cause error) Status {
	return g.recordFailure(accountID, cause)
}

func (g *Guard) recordFailure(accountID string, cause error) Status {
	accountID = strings.TrimSpace(accountID)
	if g == nil || accountID == "" {
		return Status{AccountID: accountID}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	status := g.statuses[accountID]
	status.AccountID = accountID
	status.ConsecutiveFailures++
	status.LastFailureAt = time.Now().UTC()
	status.LastError = sanitizeError(cause)
	if status.ConsecutiveFailures >= g.threshold {
		status.Paused = true
	}
	g.statuses[accountID] = status
	return status
}

func (g *Guard) ReportSuccess(accountID string) {
	g.recordSuccess(accountID)
}

func (g *Guard) SuccessStatus(accountID string) Status {
	return g.recordSuccess(accountID)
}

func (g *Guard) recordSuccess(accountID string) Status {
	accountID = strings.TrimSpace(accountID)
	if g == nil || accountID == "" {
		return Status{AccountID: accountID}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	status := g.statuses[accountID]
	status.AccountID = accountID
	status.Paused = false
	status.ConsecutiveFailures = 0
	status.LastError = ""
	status.LastSuccessAt = time.Now().UTC()
	g.statuses[accountID] = status
	return status
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
