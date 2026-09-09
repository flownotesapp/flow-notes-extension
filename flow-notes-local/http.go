package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// The HTTP transport exists for development only: it is what lets you
// curl the helper and see exactly what it does, which is how most of this
// project's integration bugs were found. Chrome uses native messaging.
//
// Every route decodes a body and hands it to the same API the native
// transport uses, so the two cannot diverge.

const maxRequestBytes = 1 << 20

var methodRoutes = map[string]string{
	"POST /notes":              MethodNoteCreate,
	"POST /captures":           MethodNoteAppend,
	"POST /notes/delete":       MethodNoteDelete,
	"POST /notes/write":        MethodNoteWrite,
	"POST /notes/read":         MethodNoteRead,
	"POST /notes/open":         MethodNoteOpen,
	"POST /apple/notes/read":   MethodAppleRead,
	"POST /apple/notes":        MethodAppleCreate,
	"POST /apple/captures":     MethodAppleAppend,
	"POST /apple/notes/delete": MethodAppleDelete,
	"POST /apple/notes/open":   MethodAppleOpen,
	"POST /apple/notes/write":  MethodAppleWrite,
}

func routes(api *API) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		result, _ := api.Dispatch(MethodHealth, nil)
		writeJSON(w, http.StatusOK, result)
	})

	for pattern, method := range methodRoutes {
		method := method
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
			raw, err := decodeRaw(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}

			result, err := api.Dispatch(method, raw)
			if err != nil {
				log.Printf("%s failed: %v", method, err)
				writeError(w, statusForCode(CodeOf(err)), err.Error())
				return
			}
			writeJSON(w, http.StatusOK, result)
		})
	}

	return mux
}

func decodeRaw(r *http.Request) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errors.New("request body too large")
		}
		return nil, errors.New("invalid request body")
	}
	return raw, nil
}

func statusForCode(code string) int {
	switch code {
	case CodeUnsupported:
		return http.StatusNotImplemented
	case CodeForbidden:
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// withCORS restricts which browser origin may reach the dev server. The
// production path is native messaging, which has no origin header and no
// CORS at all — Chrome enforces access through the host manifest's
// allowed_origins instead.
func withCORS(allowedOrigin string, next http.Handler) http.Handler {
	allowedOrigin = strings.TrimSpace(allowedOrigin)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")

		if origin != "" {
			allowed := origin == allowedOrigin ||
				(allowedOrigin == "" && strings.HasPrefix(origin, "chrome-extension://"))
			if !allowed {
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			// Chrome blocks a request from an extension to loopback unless
			// the preflight grants private-network access explicitly.
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
