package httpapi

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHumanDirectoryBrowserPresentationContract(t *testing.T) {
	repository := &publicListingRepositoryStub{}
	handler, err := NewPublicListingHandler(
		repository,
		func() time.Time { return time.Unix(100, 0).UTC() },
	)
	if err != nil {
		t.Fatalf("NewPublicListingHandler() error = %v", err)
	}

	response := httptest.NewRecorder()
	handler.serveHumanDirectory(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("human directory status = %d", response.Code)
	}

	csp := response.Header().Get("Content-Security-Policy")
	for _, required := range []string{
		"default-src 'none'",
		"style-src 'self'",
		"style-src-attr 'none'",
		"script-src 'none'",
		"img-src 'none'",
		"font-src 'none'",
		"connect-src 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
	} {
		if !strings.Contains(csp, required) {
			t.Fatalf("human directory CSP missing %q: %q", required, csp)
		}
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("human directory CSP contains %q: %q", forbidden, csp)
		}
	}

	body := response.Body.String()
	for _, required := range []string{
		`rel="stylesheet" href="/assets/directory.css"`,
		"ActivityPub infrastructure",
		"Public relay directory",
		"Participating relays",
		"No relays are listed yet",
		"Health definitions",
		"Public metadata only",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("human directory body missing %q", required)
		}
	}

	styleResponse := httptest.NewRecorder()
	serveDirectoryStylesheet(
		styleResponse,
		httptest.NewRequest(http.MethodGet, directoryStylesheetPath, nil),
	)
	if styleResponse.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", styleResponse.Code)
	}
	if got := styleResponse.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("stylesheet Content-Type = %q", got)
	}
	stylesheet := styleResponse.Body.String()
	for _, required := range []string{
		"--accent: #3157d5",
		".site-header",
		".hero h1",
		".panel",
		".health-healthy",
		".empty-state",
	} {
		if !strings.Contains(stylesheet, required) {
			t.Fatalf("stylesheet missing public-presentation contract %q", required)
		}
	}
	for _, forbidden := range []string{"@import", "url(", "@font-face"} {
		if strings.Contains(stylesheet, forbidden) {
			t.Fatalf("stylesheet contains remote-capable construct %q", forbidden)
		}
	}
}

func TestHumanDirectoryHealthStateDoesNotDependOnColor(t *testing.T) {
	if !strings.Contains(
		humanDirectoryTemplateSource,
		`<span class="health health-{{.Health}}">{{.Health}}</span>`,
	) {
		t.Fatal("health badge no longer contains the visible textual health state")
	}
	for _, state := range []string{"healthy", "stale", "dead"} {
		if !strings.Contains(humanDirectoryTemplateSource, "<dt>"+state+"</dt>") {
			t.Fatalf("health definitions no longer visibly name %q", state)
		}
	}

	styleResponse := httptest.NewRecorder()
	serveDirectoryStylesheet(
		styleResponse,
		httptest.NewRequest(http.MethodGet, directoryStylesheetPath, nil),
	)
	if styleResponse.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", styleResponse.Code)
	}
	stylesheet := styleResponse.Body.String()

	for _, required := range []string{
		`.health-healthy {`,
		`border-style: solid;`,
		`.health-healthy::before {`,
		`content: "✓";`,
		`.health-stale {`,
		`border-style: dashed;`,
		`.health-stale::before {`,
		`content: "!";`,
		`.health-dead {`,
		`border-style: double;`,
		`.health-dead::before {`,
		`content: "×";`,
	} {
		if !strings.Contains(stylesheet, required) {
			t.Fatalf("stylesheet missing non-color health cue %q", required)
		}
	}
}

func TestHumanDirectoryPresentationColorContrast(t *testing.T) {
	styleResponse := httptest.NewRecorder()
	serveDirectoryStylesheet(
		styleResponse,
		httptest.NewRequest(http.MethodGet, directoryStylesheetPath, nil),
	)
	if styleResponse.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", styleResponse.Code)
	}
	stylesheet := styleResponse.Body.String()

	backgrounds := cssVariableHexValues(t, stylesheet, "--background")
	surfaces := cssVariableHexValues(t, stylesheet, "--surface")
	accents := cssVariableHexValues(t, stylesheet, "--accent")
	accentContrasts := cssVariableHexValues(t, stylesheet, "--accent-contrast")

	for _, values := range [][]string{backgrounds, surfaces, accents, accentContrasts} {
		if len(values) != 2 {
			t.Fatalf("expected light and dark CSS values, got %v", values)
		}
	}

	for _, name := range []string{"--text", "--muted", "--accent"} {
		values := cssVariableHexValues(t, stylesheet, name)
		if len(values) != 2 {
			t.Fatalf("%s values = %v", name, values)
		}
		for mode := range values {
			requireContrastAtLeast(t, name+" on background", values[mode], backgrounds[mode], 4.5)
			requireContrastAtLeast(t, name+" on surface", values[mode], surfaces[mode], 4.5)
		}
	}

	for _, name := range []string{"--success", "--warning", "--danger"} {
		values := cssVariableHexValues(t, stylesheet, name)
		if len(values) != 2 {
			t.Fatalf("%s values = %v", name, values)
		}
		for mode := range values {
			requireContrastAtLeast(t, name+" on relay-card background", values[mode], backgrounds[mode], 4.5)
		}
	}

	for mode := range accents {
		requireContrastAtLeast(
			t,
			"button text on accent",
			accentContrasts[mode],
			accents[mode],
			4.5,
		)
	}
}

func cssVariableHexValues(t *testing.T, stylesheet, name string) []string {
	t.Helper()
	prefix := name + ":"
	values := make([]string, 0, 2)
	for _, line := range strings.Split(stylesheet, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";"))
		if len(value) != 7 || value[0] != '#' {
			t.Fatalf("%s is not a six-digit hex color: %q", name, value)
		}
		values = append(values, value)
	}
	return values
}

func requireContrastAtLeast(t *testing.T, label, foreground, background string, minimum float64) {
	t.Helper()
	ratio := colorContrastRatio(t, foreground, background)
	if ratio < minimum {
		t.Fatalf(
			"%s contrast %s on %s = %.6f, want >= %.2f",
			label,
			foreground,
			background,
			ratio,
			minimum,
		)
	}
}

func colorContrastRatio(t *testing.T, a, b string) float64 {
	t.Helper()
	aLuminance := relativeLuminance(t, a)
	bLuminance := relativeLuminance(t, b)
	if aLuminance < bLuminance {
		aLuminance, bLuminance = bLuminance, aLuminance
	}
	return (aLuminance + 0.05) / (bLuminance + 0.05)
}

func relativeLuminance(t *testing.T, color string) float64 {
	t.Helper()
	if len(color) != 7 || color[0] != '#' {
		t.Fatalf("invalid hex color %q", color)
	}

	channel := func(offset int) float64 {
		value, err := strconv.ParseUint(color[offset:offset+2], 16, 8)
		if err != nil {
			t.Fatalf("parse color %q: %v", color, err)
		}
		srgb := float64(value) / 255.0
		if srgb <= 0.04045 {
			return srgb / 12.92
		}
		return math.Pow((srgb+0.055)/1.055, 2.4)
	}

	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}

func TestReverseProxyExamplesDoNotOverrideApplicationCSP(t *testing.T) {
	for _, path := range []string{
		"../../contrib/nginx/activity-relay-directory.conf.example",
		"../../contrib/apache/activity-relay-directory.conf.example",
		"../../contrib/caddy/Caddyfile.example",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		text := string(body)
		if strings.Contains(text, "Content-Security-Policy") {
			t.Fatalf("%s overrides the application's route-specific CSP", path)
		}
		for _, required := range []string{
			"X-Content-Type-Options",
			"Referrer-Policy",
			"X-Frame-Options",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s lost transport-level header %q", path, required)
			}
		}
	}

	body, err := os.ReadFile("../../docs/REVERSE-PROXY.md")
	if err != nil {
		t.Fatalf("ReadFile(reverse proxy docs) error = %v", err)
	}
	for _, required := range []string{
		"Application-owned Content-Security-Policy",
		"style-src 'self'",
		"must not replace or add a generic CSP",
	} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("reverse proxy documentation missing %q", required)
		}
	}
}
