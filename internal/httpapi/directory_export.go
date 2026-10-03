package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/thystra/activity-relay-directory/internal/directoryexport"
)

const (
	directoryActiveDownloadPath      = "/downloads/active.txt"
	directoryAllDownloadPath         = "/downloads/all.txt"
	directoryUnavailableDownloadPath = "/downloads/unavailable.txt"
	directoryExportReadTimeout       = 5 * time.Second
)

func (handler *PublicListingHandler) serveDirectoryExport(
	response http.ResponseWriter,
	request *http.Request,
	scope directoryexport.Scope,
	filename string,
) {
	if !allowReadMethod(response, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writeDirectoryExportError(response, request, http.StatusBadRequest, "invalid directory download request")
		return
	}
	if handler == nil || handler.directoryRepository == nil || handler.now == nil || handler.semaphore == nil {
		writeDirectoryExportError(response, request, http.StatusServiceUnavailable, "directory download temporarily unavailable")
		return
	}

	select {
	case handler.semaphore <- struct{}{}:
		defer func() { <-handler.semaphore }()
	default:
		response.Header().Set("Retry-After", "1")
		writeDirectoryExportError(response, request, http.StatusTooManyRequests, "directory download request limit exceeded")
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), directoryExportReadTimeout)
	defer cancel()
	body, err := directoryexport.Render(ctx, handler.directoryRepository, directoryexport.Request{
		Scope:      scope,
		Format:     directoryexport.FormatHosts,
		ObservedAt: handler.now().UTC(),
	})
	if err != nil {
		writeDirectoryExportError(response, request, http.StatusServiceUnavailable, "directory download temporarily unavailable")
		return
	}

	response.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writeCacheablePublicRepresentation(response, request, "text/plain; charset=utf-8", body)
}

func writeDirectoryExportError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	message string,
) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write([]byte(message + "\n"))
	}
}
