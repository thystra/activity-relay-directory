package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thystra/activity-relay-directory/internal/reachability"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

// reachabilityCoverageGate is intentionally process-local. After a restart,
// automatic pruning must wait for a new complete reachability pass rather than
// trusting stale scheduler state.
type reachabilityCoverageGate struct {
	mutex       sync.RWMutex
	ready       bool
	observedAt  time.Time
	completedAt time.Time
}

func (gate *reachabilityCoverageGate) RecordComplete(observedAt, completedAt time.Time) bool {
	if gate == nil {
		return false
	}
	observedAt = observedAt.UTC()
	completedAt = completedAt.UTC()
	if observedAt.Unix() < 0 || completedAt.Unix() < 0 || completedAt.Before(observedAt) {
		return false
	}
	gate.mutex.Lock()
	gate.ready = true
	gate.observedAt = observedAt
	gate.completedAt = completedAt
	gate.mutex.Unlock()
	return true
}

func (gate *reachabilityCoverageGate) ObservationForPruning(now time.Time) (time.Time, bool) {
	if gate == nil {
		return time.Time{}, false
	}
	now = now.UTC()
	gate.mutex.RLock()
	ready := gate.ready
	observedAt := gate.observedAt
	completedAt := gate.completedAt
	gate.mutex.RUnlock()
	if !ready || now.Before(observedAt) || now.Before(completedAt) ||
		now.Sub(observedAt) > storage.ReachabilityFreshness ||
		now.Sub(completedAt) > storage.ReachabilityFreshness {
		return time.Time{}, false
	}
	return observedAt, true
}

func runReachabilityMaintenance(
	ctx context.Context,
	repository storage.ReachabilityRepository,
	prober reachability.Prober,
	interval time.Duration,
	now func() time.Time,
	gate *reachabilityCoverageGate,
	onResult func(reachability.Result),
	onError func(error),
) {
	if ctx == nil || repository == nil || prober == nil || now == nil || gate == nil ||
		interval != storage.ReachabilityMaintenanceInterval {
		if onError != nil {
			onError(errors.New("reachability maintenance configuration is invalid"))
		}
		return
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		observedAt := now().UTC()
		result, err := reachability.Run(ctx, repository, prober, observedAt)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if onError != nil {
				onError(err)
			}
		} else {
			completedAt := now().UTC()
			if !result.Truncated {
				if !gate.RecordComplete(observedAt, completedAt) && onError != nil {
					onError(errors.New("reachability coverage time is invalid"))
				}
			}
			if onResult != nil {
				onResult(result)
			}
		}

		if !waitMaintenanceInterval(ctx, interval) {
			return
		}
	}
}

func waitMaintenanceInterval(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
