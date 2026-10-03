package main

import (
	"context"
	"errors"
	"time"

	"github.com/thystra/activity-relay-directory/internal/candidatemaintenance"
	"github.com/thystra/activity-relay-directory/internal/reachability"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

func runDiscoveryCandidateMaintenance(
	ctx context.Context,
	repository storage.DiscoveryCandidateMaintenanceRepository,
	prober reachability.Prober,
	interval time.Duration,
	now func() time.Time,
	onResult func(candidatemaintenance.Result),
	onError func(error),
) {
	if ctx == nil || repository == nil || prober == nil || now == nil ||
		interval != storage.DiscoveryCandidateMaintenanceInterval {
		if onError != nil {
			onError(errors.New("discovery candidate maintenance configuration is invalid"))
		}
		return
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		result, err := candidatemaintenance.Run(ctx, repository, prober, now().UTC())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if onError != nil {
				onError(err)
			}
		} else if onResult != nil {
			onResult(result)
		}
		if !waitMaintenanceInterval(ctx, interval) {
			return
		}
	}
}
