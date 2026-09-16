package httpapi

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

type humanDirectoryPage struct {
	Listing             directoryProjectionResponse
	PreviousURL         string
	NextURL             string
	Stylesheet          string
	HasOperator         bool
	HasOperatorLinks    bool
	OperatorWebsite     string
	OperatorEmail       string
	OperatorEmailURL    string
	FediverseID         string
	FediverseURL        string
	OperatorDiagnostics []string
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
		"relayLabel":        humanRelayLabel,
		"humanTime":         humanDirectoryTime,
		"heartbeatLabel":    humanHeartbeatLabel,
		"reachabilityLabel": humanReachabilityLabel,
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

func (handler *PublicListingHandler) serveHumanDirectory(response http.ResponseWriter, request *http.Request) {
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

	previousURL := humanDirectoryPaginationURL("before", listing.Pagination.PreviousCursor, listing.Pagination.Limit)
	nextURL := humanDirectoryPaginationURL("cursor", listing.Pagination.NextCursor, listing.Pagination.Limit)

	operator := handler.operator
	operatorEmailURL := ""
	if operator.Email != "" {
		operatorEmailURL = (&url.URL{Scheme: "mailto", Opaque: operator.Email}).String()
	}

	body, err := handler.renderHumanDirectory(humanDirectoryPage{
		Listing:             listing,
		PreviousURL:         previousURL,
		NextURL:             nextURL,
		Stylesheet:          directoryStylesheetPath,
		HasOperator:         !operator.Empty(),
		HasOperatorLinks:    operator.HasLinks(),
		OperatorWebsite:     operator.Website,
		OperatorEmail:       operator.Email,
		OperatorEmailURL:    operatorEmailURL,
		FediverseID:         operator.FediverseID,
		FediverseURL:        operator.FediverseURL,
		OperatorDiagnostics: operator.Diagnostics,
	})
	if err != nil {
		writeHumanDirectoryError(response, request, http.StatusServiceUnavailable, "directory temporarily unavailable")
		return
	}

	response.Header().Set("Content-Security-Policy", humanDirectoryCSP)
	writeCacheablePublicRepresentation(response, request, humanDirectoryContentType, body)
}

func humanDirectoryPaginationURL(parameter, cursor string, limit int) string {
	if cursor == "" {
		return ""
	}
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	values.Set(parameter, cursor)
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
