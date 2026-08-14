package httpapi

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
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
	Listing          publicListingResponse
	NextURL          string
	Stylesheet       string
	HasOperator      bool
	OperatorWebsite  string
	OperatorEmail    string
	OperatorEmailURL string
	FediverseID      string
	FediverseURL     string
}

func newHumanDirectoryRenderer() (func(humanDirectoryPage) ([]byte, error), error) {
	parsed, err := template.New("directory.html").Parse(humanDirectoryTemplateSource)
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

	listing, failure := handler.loadPublicListing(request)
	if failure != nil {
		if failure.retryAfter != "" {
			response.Header().Set("Retry-After", failure.retryAfter)
		}
		writeHumanDirectoryError(response, request, failure.status, humanDirectoryFailureMessage(failure))
		return
	}

	nextURL := ""
	if listing.Pagination.NextCursor != "" {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(listing.Pagination.Limit))
		values.Set("cursor", listing.Pagination.NextCursor)
		nextURL = "/?" + values.Encode()
	}

	operator := handler.operator
	operatorEmailURL := ""
	if operator.Email != "" {
		operatorEmailURL = (&url.URL{Scheme: "mailto", Opaque: operator.Email}).String()
	}

	body, err := handler.renderHumanDirectory(humanDirectoryPage{
		Listing:          listing,
		NextURL:          nextURL,
		Stylesheet:       directoryStylesheetPath,
		HasOperator:      !operator.Empty(),
		OperatorWebsite:  operator.Website,
		OperatorEmail:    operator.Email,
		OperatorEmailURL: operatorEmailURL,
		FediverseID:      operator.FediverseID,
		FediverseURL:     operator.FediverseURL,
	})
	if err != nil {
		writeHumanDirectoryError(response, request, http.StatusServiceUnavailable, "directory temporarily unavailable")
		return
	}

	response.Header().Set("Content-Security-Policy", humanDirectoryCSP)
	writeCacheablePublicRepresentation(response, request, humanDirectoryContentType, body)
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
