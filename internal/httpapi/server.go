package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
)

const (
	statusSchemaVersion = 1
	readinessTimeout    = 2 * time.Second
)

// ReadinessCheck reports whether required runtime dependencies can serve
// requests. Its error is deliberately not exposed to public clients.
type ReadinessCheck func(context.Context) error

// NewHandler returns the initial public HTTP surface.
func NewHandler(
	cfg config.Config,
	version string,
	checkReadiness ReadinessCheck,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(response http.ResponseWriter, request *http.Request) {
		if !allowReadMethod(response, request) {
			return
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		if request.Method != http.MethodHead {
			_, _ = response.Write([]byte("ok\n"))
		}
	})

	mux.HandleFunc("/readyz", func(response http.ResponseWriter, request *http.Request) {
		if !allowReadMethod(response, request) {
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), readinessTimeout)
		defer cancel()
		if checkReadiness == nil || checkReadiness(ctx) != nil {
			response.Header().Set("Cache-Control", "no-store")
			response.Header().Set("Content-Type", "text/plain; charset=utf-8")
			response.WriteHeader(http.StatusServiceUnavailable)
			if request.Method != http.MethodHead {
				_, _ = response.Write([]byte("not ready\n"))
			}
			return
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusOK)
		if request.Method != http.MethodHead {
			_, _ = response.Write([]byte("ready\n"))
		}
	})

	mux.HandleFunc("/v1/status", func(response http.ResponseWriter, request *http.Request) {
		if !allowReadMethod(response, request) {
			return
		}

		writeJSON(response, request, http.StatusOK, map[string]any{
			"schema_version":         statusSchemaVersion,
			"service":                "activity-relay-directory",
			"version":                version,
			"public_base_url":        cfg.PublicBaseURL,
			"registration_enabled":   cfg.RegistrationEnabled,
			"registration_available": false,
		})
	})

	return securityHeaders(mux)
}

func allowReadMethod(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}

	response.Header().Set("Allow", "GET, HEAD")
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func writeJSON(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	value any,
) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)

	if request.Method == http.MethodHead {
		return
	}

	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set(
			"Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
		)
		next.ServeHTTP(response, request)
	})
}
