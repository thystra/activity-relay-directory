package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/thystra/activity-relay-directory/internal/config"
	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	statusSchemaVersion = 3
	readinessTimeout    = 2 * time.Second
)

// ReadinessCheck reports whether required runtime dependencies can serve
// requests. Its error is deliberately not exposed to public clients.
type ReadinessCheck func(context.Context) error

// EnrollmentStatus reads the current durable admission policy. Errors fail the
// public status request closed without exposing backend details.
type EnrollmentStatus func(context.Context) (bool, error)

// NewHandler returns the initial public HTTP surface.
func NewHandler(
	cfg config.Config,
	version string,
	checkReadiness ReadinessCheck,
) http.Handler {
	return NewHandlerWithRuntime(cfg, version, checkReadiness, nil, nil, nil)
}

// NewHandlerWithLifecycle returns the public HTTP surface and conditionally
// enables the three signed lifecycle routes only when both the runtime flag and
// the complete dependency graph are present.
func NewHandlerWithLifecycle(
	cfg config.Config,
	version string,
	checkReadiness ReadinessCheck,
	lifecycle *LifecycleHandler,
	enrollmentStatus EnrollmentStatus,
) http.Handler {
	return NewHandlerWithRuntime(
		cfg, version, checkReadiness, lifecycle, enrollmentStatus, nil,
	)
}

// NewHandlerWithRuntime composes independently gated lifecycle and public
// listing graphs behind the shared public status and security headers.
func NewHandlerWithRuntime(
	cfg config.Config,
	version string,
	checkReadiness ReadinessCheck,
	lifecycle *LifecycleHandler,
	enrollmentStatus EnrollmentStatus,
	publicListing *PublicListingHandler,
) http.Handler {
	mux := http.NewServeMux()
	lifecycleAvailable := cfg.LifecycleEnabled && lifecycle != nil
	publicListingAvailable := cfg.PublicListingEnabled && publicListing != nil

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

		enrollmentOpen := false
		if enrollmentStatus != nil {
			ctx, cancel := context.WithTimeout(request.Context(), readinessTimeout)
			defer cancel()
			var err error
			enrollmentOpen, err = enrollmentStatus(ctx)
			if err != nil {
				writeProtocolError(response, request, http.StatusServiceUnavailable, v1.ErrorInternal)
				return
			}
		}
		writeJSON(response, request, http.StatusOK, map[string]any{
			"schema_version":           statusSchemaVersion,
			"service":                  "activity-relay-directory",
			"version":                  version,
			"public_base_url":          cfg.PublicBaseURL,
			"lifecycle_enabled":        cfg.LifecycleEnabled,
			"lifecycle_available":      lifecycleAvailable,
			"enrollment_open":          enrollmentOpen,
			"public_listing_enabled":   cfg.PublicListingEnabled,
			"public_listing_available": publicListingAvailable,
		})
	})

	active := lifecycle
	if !lifecycleAvailable {
		active = nil
	}
	registerLifecycleRoute := func(path string, operation v1.Operation) {
		mux.HandleFunc(path, func(response http.ResponseWriter, request *http.Request) {
			active.serve(response, request, operation)
		})
	}
	registerLifecycleRoute(v1.RegisterEndpointPath, v1.OperationRegister)
	registerLifecycleRoute(v1.HeartbeatEndpointPath, v1.OperationHeartbeat)
	registerLifecycleRoute(v1.UnregisterEndpointPath, v1.OperationUnregister)

	if cfg.PublicListingEnabled {
		mux.HandleFunc("/v1/relays", func(response http.ResponseWriter, request *http.Request) {
			if !publicListingAvailable {
				if !allowReadMethod(response, request) {
					return
				}
				writePublicListingError(response, request, http.StatusServiceUnavailable, "temporarily_unavailable", "public listing temporarily unavailable")
				return
			}
			publicListing.serve(response, request)
		})
	}

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
