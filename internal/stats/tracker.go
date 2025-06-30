// internal/stats/tracker.go
package stats

import (
	"sort"
	"sync"
	"time"
)

// Tracker tracks connection statistics
type Tracker struct {
	TotalConnections int
	OpenConnections  int
	Durations        []time.Duration
	ResponseTimes1m  []time.Duration
	ResponseTimes5m  []time.Duration
	mu               sync.RWMutex
}

// Stats is an alias for Tracker to match the alternate interface
type Stats = Tracker

// NewTracker creates a new statistics tracker
func NewTracker() *Tracker {
	return &Tracker{
		Durations:       make([]time.Duration, 0),
		ResponseTimes1m: make([]time.Duration, 0),
		ResponseTimes5m: make([]time.Duration, 0),
	}
}

// New is an alias for NewTracker to match the alternate interface
func New() *Stats {
	return NewTracker()
}

// AddRequest adds a request duration to the statistics
func (t *Tracker) AddRequest(duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.TotalConnections++
	t.Durations = append(t.Durations, duration)

	// Keep only last minute for rt1
	t.ResponseTimes1m = append(t.ResponseTimes1m, duration)
	if len(t.ResponseTimes1m) > 60 {
		t.ResponseTimes1m = t.ResponseTimes1m[1:]
	}

	// Keep only last 5 minutes for rt5
	t.ResponseTimes5m = append(t.ResponseTimes5m, duration)
	if len(t.ResponseTimes5m) > 300 {
		t.ResponseTimes5m = t.ResponseTimes5m[1:]
	}

	// Keep only last 1000 overall durations
	if len(t.Durations) > 1000 {
		t.Durations = t.Durations[1:]
	}
}

// Add is an alias for AddRequest to match the alternate interface
func (t *Tracker) Add(duration time.Duration) {
	t.AddRequest(duration)
}

// IncrementOpen increments the count of open connections
func (t *Tracker) IncrementOpen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.OpenConnections++
}

// IncOpen is an alias for IncrementOpen to match the alternate interface
func (t *Tracker) IncOpen() {
	t.IncrementOpen()
}

// DecrementOpen decrements the count of open connections
func (t *Tracker) DecrementOpen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.OpenConnections--
}

// DecOpen is an alias for DecrementOpen to match the alternate interface
func (t *Tracker) DecOpen() {
	t.DecrementOpen()
}

// GetStats returns current statistics
// Returns: total connections, open connections, avg response time 1m, avg response time 5m, p50, p90 (all times in ms)
func (t *Tracker) GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ttl = t.TotalConnections
	opn = t.OpenConnections

	// Calculate rt1 (average response time last minute)
	if len(t.ResponseTimes1m) > 0 {
		var sum time.Duration
		for _, d := range t.ResponseTimes1m {
			sum += d
		}
		rt1 = float64(sum) / float64(len(t.ResponseTimes1m)) / float64(time.Millisecond)
	}

	// Calculate rt5 (average response time last 5 minutes)
	if len(t.ResponseTimes5m) > 0 {
		var sum time.Duration
		for _, d := range t.ResponseTimes5m {
			sum += d
		}
		rt5 = float64(sum) / float64(len(t.ResponseTimes5m)) / float64(time.Millisecond)
	}

	// Calculate percentiles from overall durations
	if len(t.Durations) > 0 {
		sorted := make([]time.Duration, len(t.Durations))
		copy(sorted, t.Durations)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i] < sorted[j]
		})

		p50Idx := len(sorted) * 50 / 100
		p90Idx := len(sorted) * 90 / 100

		if p50Idx < len(sorted) {
			p50 = float64(sorted[p50Idx]) / float64(time.Millisecond)
		}
		if p90Idx < len(sorted) {
			p90 = float64(sorted[p90Idx]) / float64(time.Millisecond)
		}
	}

	return
}

// StatsSnapshot represents a snapshot of statistics
type StatsSnapshot struct {
	TotalConnections  int     `json:"total_connections"`
	OpenConnections   int     `json:"open_connections"`
	AvgResponseTime1m float64 `json:"avg_response_time_1m"`
	AvgResponseTime5m float64 `json:"avg_response_time_5m"`
	P50ResponseTime   float64 `json:"p50_response_time"`
	P90ResponseTime   float64 `json:"p90_response_time"`
}

// Snapshot returns a snapshot of current statistics
func (t *Tracker) Snapshot() StatsSnapshot {
	ttl, opn, rt1, rt5, p50, p90 := t.GetStats()

	return StatsSnapshot{
		TotalConnections:  ttl,
		OpenConnections:   opn,
		AvgResponseTime1m: rt1,
		AvgResponseTime5m: rt5,
		P50ResponseTime:   p50,
		P90ResponseTime:   p90,
	}
}

// Reset resets all statistics
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.TotalConnections = 0
	t.OpenConnections = 0
	t.Durations = t.Durations[:0]
	t.ResponseTimes1m = t.ResponseTimes1m[:0]
	t.ResponseTimes5m = t.ResponseTimes5m[:0]
}

// GetConnectionCount returns the current connection counts
func (t *Tracker) GetConnectionCount() (total, open int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.TotalConnections, t.OpenConnections
}

// GetAverageResponseTimes returns average response times for different time windows
func (t *Tracker) GetAverageResponseTimes() (rt1m, rt5m float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Calculate rt1 (average response time last minute)
	if len(t.ResponseTimes1m) > 0 {
		var sum time.Duration
		for _, d := range t.ResponseTimes1m {
			sum += d
		}
		rt1m = float64(sum) / float64(len(t.ResponseTimes1m)) / float64(time.Millisecond)
	}

	// Calculate rt5 (average response time last 5 minutes)
	if len(t.ResponseTimes5m) > 0 {
		var sum time.Duration
		for _, d := range t.ResponseTimes5m {
			sum += d
		}
		rt5m = float64(sum) / float64(len(t.ResponseTimes5m)) / float64(time.Millisecond)
	}

	return rt1m, rt5m
}

// GetPercentiles returns response time percentiles
func (t *Tracker) GetPercentiles() (p50, p90, p95, p99 float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.Durations) == 0 {
		return 0, 0, 0, 0
	}

	sorted := make([]time.Duration, len(t.Durations))
	copy(sorted, t.Durations)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	p50Idx := len(sorted) * 50 / 100
	p90Idx := len(sorted) * 90 / 100
	p95Idx := len(sorted) * 95 / 100
	p99Idx := len(sorted) * 99 / 100

	if p50Idx < len(sorted) {
		p50 = float64(sorted[p50Idx]) / float64(time.Millisecond)
	}
	if p90Idx < len(sorted) {
		p90 = float64(sorted[p90Idx]) / float64(time.Millisecond)
	}
	if p95Idx < len(sorted) {
		p95 = float64(sorted[p95Idx]) / float64(time.Millisecond)
	}
	if p99Idx < len(sorted) {
		p99 = float64(sorted[p99Idx]) / float64(time.Millisecond)
	}

	return p50, p90, p95, p99
}
