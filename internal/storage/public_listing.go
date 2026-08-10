package storage

import "context"

const (
	// DefaultPublicListingPage is the public GET /v1/relays page size when the
	// caller omits an explicit limit.
	DefaultPublicListingPage = 50
	// MaximumPublicListingPage is the hard public page-size ceiling.
	MaximumPublicListingPage = 100
)

// PublicListingRepository reads one bounded, already-filtered public page.
// Implementations must exclude suspended, unregistered, pruned, and
// 30-day-or-older relays before returning data to the presentation layer.
type PublicListingRepository interface {
	ListPublicRelays(
		context.Context,
		HealthProjectionQuery,
	) (HealthProjectionPage, error)
}
