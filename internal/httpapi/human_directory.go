package httpapi

import (
	"bytes"
	"context"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	"github.com/thystra/activity-relay-directory/internal/storage"
)

const (
	humanDirectoryContentType = "text/html; charset=utf-8"
	humanDirectoryCSP         = "default-src 'none'; style-src 'self'; style-src-elem 'self'; style-src-attr 'none'; img-src 'none'; script-src 'none'; font-src 'none'; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	directoryStylesheetPath   = "/assets/directory.css"
)

var (
	//go:embed templates/directory.html
	humanDirectoryTemplateSource string

	//go:embed assets/directory.css
	humanDirectoryStylesheet []byte
)

type humanDirectoryTierBlock struct {
	Tier        storage.DirectoryTier
	Title       string
	Description string
	Relays      []directoryProjectionRelay
}

type humanDirectoryPage struct {
	Listing                   directoryProjectionResponse
	Summary                   storage.DirectorySummary
	TierBlocks                []humanDirectoryTierBlock
	PreviousURL               string
	NextURL                   string
	ActiveDownloadURL         string
	AllDownloadURL            string
	UnavailableDownloadURL    string
	Stylesheet                string
	Version                   string
	DirectoryTitle            string
	DirectoryBannerURL        string
	HasOperator               bool
	HasOperatorLinks          bool
	OperatorWebsite           string
	OperatorEmail             string
	OperatorEmailURL          string
	FediverseID               string
	FediverseURL              string
	OperatorDiagnostics       []string
	SupportEntries            []config.SupportEntry
	RegistrationFilter        string
	AllRegistrationURL        string
	OpenRegistrationURL       string
	RestrictedRegistrationURL string
	ClosedRegistrationURL     string
}

func humanDirectoryTierTitle(tier storage.DirectoryTier) string {
	switch tier {
	case storage.DirectoryTierHeartbeatOnline:
		return "Tier 1 — Heartbeat + Online"
	case storage.DirectoryTierOnline:
		return "Tier 2 — Online, no current heartbeat"
	case storage.DirectoryTierUnavailable:
		return "Tier 3 — Offline / Unreachable"
	case storage.DirectoryTierGraveyard:
		return "Tier 4 — Graveyard"
	default:
		return "Unknown tier"
	}
}

func humanDirectoryTierDescription(tier storage.DirectoryTier) string {
	switch tier {
	case storage.DirectoryTierHeartbeatOnline:
		return "These relays send a directory heartbeat and were reachable at the latest check."
	case storage.DirectoryTierOnline:
		return "These relays are known to the directory and were reachable at the latest check, but do not currently send a directory heartbeat."
	case storage.DirectoryTierUnavailable:
		return "These relays could not be reached at the latest check but have been seen online within the last 30 days."
	case storage.DirectoryTierGraveyard:
		return "These relays have not been seen online for at least 30 days. They remain listed for historical reference and are checked periodically in case they return."
	default:
		return ""
	}
}

func buildHumanDirectoryTierBlocks(relays []directoryProjectionRelay) []humanDirectoryTierBlock {
	blocks := make([]humanDirectoryTierBlock, 0, 4)
	for tier := storage.DirectoryTierHeartbeatOnline; tier <= storage.DirectoryTierGraveyard; tier++ {
		block := humanDirectoryTierBlock{
			Tier:        tier,
			Title:       humanDirectoryTierTitle(tier),
			Description: humanDirectoryTierDescription(tier),
		}
		for _, relay := range relays {
			if relay.Tier == tier {
				block.Relays = append(block.Relays, relay)
			}
		}
		sort.SliceStable(block.Relays, func(i, j int) bool {
			left, right := participationRank(block.Relays[i].Profile.ParticipationMode), participationRank(block.Relays[j].Profile.ParticipationMode)
			if left != right {
				return left < right
			}
			return block.Relays[i].RelayActor < block.Relays[j].RelayActor
		})
		if len(block.Relays) != 0 {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func participationRank(value string) int {
	switch value {
	case "open":
		return 0
	case "restricted":
		return 1
	case "closed":
		return 2
	default:
		return 3
	}
}

func participationClass(value string) string {
	switch value {
	case "open", "restricted", "closed":
		return value
	default:
		return "unknown"
	}
}

func participationLabel(value string) string {
	switch value {
	case "open":
		return "Open"
	case "restricted":
		return "Restricted"
	case "closed":
		return "Closed"
	default:
		return "Not reported"
	}
}

func filterHumanDirectoryRelays(relays []directoryProjectionRelay, registration string) []directoryProjectionRelay {
	if registration == "" {
		return relays
	}
	filtered := make([]directoryProjectionRelay, 0, len(relays))
	for _, relay := range relays {
		if relay.Profile.ParticipationMode == registration {
			filtered = append(filtered, relay)
		}
	}
	return filtered
}

func humanRelayLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	label := parsed.Host
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if path != "" {
		label += path
	}
	return label
}

func humanDirectoryTime(value *string) string {
	if value == nil || *value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return *value
	}
	return parsed.UTC().Format("2006-01-02 15:04 UTC")
}

func humanHeartbeatLabel(state storage.PublicHeartbeatState) string {
	switch state {
	case storage.HeartbeatHealthy:
		return "Healthy"
	case storage.HeartbeatStale:
		return "Stale"
	case storage.HeartbeatDead:
		return "Dead"
	case storage.HeartbeatPrune:
		return "Inactive"
	case storage.HeartbeatNotObserved:
		return "No heartbeat"
	default:
		return "Unknown"
	}
}

func humanReachabilityLabel(state storage.ReachabilityState) string {
	switch state {
	case storage.ReachabilityReachable:
		return "Reachable"
	case storage.ReachabilityUnreachable:
		return "Unreachable"
	default:
		return "Not checked"
	}
}

func newHumanDirectoryRenderer() (func(humanDirectoryPage) ([]byte, error), error) {
	parsed, err := template.New("directory.html").Funcs(template.FuncMap{
		"relayLabel":         humanRelayLabel,
		"humanTime":          humanDirectoryTime,
		"heartbeatLabel":     humanHeartbeatLabel,
		"reachabilityLabel":  humanReachabilityLabel,
		"participationLabel": participationLabel,
		"participationClass": participationClass,
	}).Parse(humanDirectoryTemplateSource)
	if err != nil {
		return nil, err
	}
	return func(page humanDirectoryPage) ([]byte, error) {
		var body bytes.Buffer
		if err := parsed.Execute(&body, page); err != nil {
			return nil, err
		}
		return body.Bytes(), nil
	}, nil
}

func humanDirectoryCSPForBanner(bannerURL string) string {
	if bannerURL == "" {
		return humanDirectoryCSP
	}
	parsed, err := url.Parse(bannerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return humanDirectoryCSP
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return strings.Replace(humanDirectoryCSP, "img-src 'none'", "img-src 'self' "+origin, 1)
}

func (handler *PublicListingHandler) serveHumanDirectory(response http.ResponseWriter, request *http.Request) {
	handler.serveHumanDirectoryWithVersion(response, request, "")
}

func (handler *PublicListingHandler) serveHumanDirectoryWithVersion(
	response http.ResponseWriter,
	request *http.Request,
	version string,
) {
	if !allowReadMethod(response, request) {
		return
	}
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}
	if handler == nil || handler.renderHumanDirectory == nil {
		writeHumanDirectoryError(response, request, http.StatusServiceUnavailable, "directory temporarily unavailable")
		return
	}

	listing, failure := handler.loadHumanDirectoryProjection(request)
	if failure != nil {
		if failure.retryAfter != "" {
			response.Header().Set("Retry-After", failure.retryAfter)
		}
		writeHumanDirectoryError(response, request, failure.status, humanDirectoryFailureMessage(failure))
		return
	}

	registrationFilter := request.URL.Query().Get("registration")
	listing.Relays = filterHumanDirectoryRelays(listing.Relays, registrationFilter)

	summary, failure := handler.loadHumanDirectorySummary(request.Context(), listing.observedAt)
	if failure != nil {
		if failure.retryAfter != "" {
			response.Header().Set("Retry-After", failure.retryAfter)
		}
		writeHumanDirectoryError(response, request, failure.status, humanDirectoryFailureMessage(failure))
		return
	}

	previousURL := humanDirectoryPaginationURL("before", listing.Pagination.PreviousCursor, listing.Pagination.Limit, registrationFilter)
	nextURL := humanDirectoryPaginationURL("cursor", listing.Pagination.NextCursor, listing.Pagination.Limit, registrationFilter)

	operator := handler.operator
	directoryTitle := operator.DirectoryTitle
	if directoryTitle == "" {
		directoryTitle = "Activity-Relay Directory"
	}
	operatorEmailURL := ""
	if operator.Email != "" {
		operatorEmailURL = (&url.URL{Scheme: "mailto", Opaque: operator.Email}).String()
	}

	body, err := handler.renderHumanDirectory(humanDirectoryPage{
		Listing:                   listing,
		Summary:                   summary,
		TierBlocks:                buildHumanDirectoryTierBlocks(listing.Relays),
		PreviousURL:               previousURL,
		NextURL:                   nextURL,
		ActiveDownloadURL:         directoryActiveDownloadPath,
		AllDownloadURL:            directoryAllDownloadPath,
		UnavailableDownloadURL:    directoryUnavailableDownloadPath,
		Stylesheet:                directoryStylesheetPath,
		Version:                   version,
		DirectoryTitle:            directoryTitle,
		DirectoryBannerURL:        operator.DirectoryBannerURL,
		HasOperator:               !operator.Empty(),
		HasOperatorLinks:          operator.HasLinks(),
		OperatorWebsite:           operator.Website,
		OperatorEmail:             operator.Email,
		OperatorEmailURL:          operatorEmailURL,
		FediverseID:               operator.FediverseID,
		FediverseURL:              operator.FediverseURL,
		OperatorDiagnostics:       operator.Diagnostics,
		SupportEntries:            operator.Support,
		RegistrationFilter:        registrationFilter,
		AllRegistrationURL:        "/#relay-list",
		OpenRegistrationURL:       "/?registration=open#relay-list",
		RestrictedRegistrationURL: "/?registration=restricted#relay-list",
		ClosedRegistrationURL:     "/?registration=closed#relay-list",
	})
	if err != nil {
		writeHumanDirectoryError(response, request, http.StatusServiceUnavailable, "directory temporarily unavailable")
		return
	}

	response.Header().Set("Content-Security-Policy", humanDirectoryCSPForBanner(operator.DirectoryBannerURL))
	writeCacheablePublicRepresentation(response, request, humanDirectoryContentType, body)
}

func (handler *PublicListingHandler) loadHumanDirectorySummary(
	ctx context.Context,
	observedAt time.Time,
) (storage.DirectorySummary, *publicListingFailure) {
	if handler == nil || handler.summaryRepository == nil || handler.semaphore == nil || observedAt.IsZero() {
		return storage.DirectorySummary{}, directoryProjectionUnavailable()
	}

	select {
	case handler.semaphore <- struct{}{}:
		defer func() { <-handler.semaphore }()
	default:
		return storage.DirectorySummary{}, &publicListingFailure{
			status:     http.StatusTooManyRequests,
			code:       "rate_limited",
			message:    "directory summary request limit exceeded",
			retryAfter: "1",
		}
	}

	readCtx, cancel := context.WithTimeout(ctx, publicListingReadTimeout)
	defer cancel()
	summary, err := handler.summaryRepository.ReadDirectorySummary(readCtx, observedAt)
	if err != nil || !summary.Valid() {
		return storage.DirectorySummary{}, directoryProjectionUnavailable()
	}
	return summary, nil
}

func humanDirectoryPaginationURL(parameter, cursor string, limit int, registration string) string {
	if cursor == "" {
		return ""
	}
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	values.Set(parameter, cursor)
	if registration != "" {
		values.Set("registration", registration)
	}
	return "/?" + values.Encode() + "#relay-list"
}

func humanDirectoryFailureMessage(failure *publicListingFailure) string {
	if failure == nil {
		return "directory temporarily unavailable"
	}
	switch failure.status {
	case http.StatusBadRequest:
		return "invalid directory request"
	case http.StatusTooManyRequests:
		return "directory request limit exceeded"
	default:
		return "directory temporarily unavailable"
	}
}

func writeHumanDirectoryError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	message string,
) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", humanDirectoryCSP)
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write([]byte(message + "\n"))
	}
}

func serveDirectoryStylesheet(response http.ResponseWriter, request *http.Request) {
	if !allowReadMethod(response, request) {
		return
	}
	if request.URL.Path != directoryStylesheetPath {
		http.NotFound(response, request)
		return
	}
	if len(humanDirectoryStylesheet) == 0 {
		writeHumanDirectoryError(response, request, http.StatusServiceUnavailable, "directory temporarily unavailable")
		return
	}
	writeCacheablePublicRepresentation(response, request, "text/css; charset=utf-8", humanDirectoryStylesheet)
}
