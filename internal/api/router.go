package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/blockbridge/avmcbbs/apps/mcstatus/internal/probe"
)

func NewRouter(service *probe.Service, apiTokens ...string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/v1/probe", handleProbe(service))
	mux.HandleFunc("/v1/probe/jobs", handleProbeJobs(service))
	mux.HandleFunc("/v1/probe/jobs/", handleProbeJobDetail(service))
	var token string
	if len(apiTokens) > 0 {
		token = apiTokens[0]
	}
	return withJSON(withInternalBearer(mux, token))
}

func withInternalBearer(next http.Handler, token string) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if token != "" && request.URL.Path != "/healthz" {
			provided := sha256.Sum256([]byte(request.Header.Get("Authorization")))
			if len(request.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
				writeErrorMessage(writer, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "mcstatus",
	})
}

func handleProbe(service *probe.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeMethodNotAllowed(writer)
			return
		}

		payload, ok := decodeRequest(writer, request)
		if !ok {
			return
		}

		result, err := service.Probe(request.Context(), payload)
		if err != nil {
			statusCode := http.StatusBadRequest
			if errors.Is(err, probe.ErrClosed) {
				statusCode = http.StatusServiceUnavailable
			}
			writeError(writer, statusCode, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	}
}

func handleProbeJobs(service *probe.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeMethodNotAllowed(writer)
			return
		}

		payload, ok := decodeRequest(writer, request)
		if !ok {
			return
		}

		job, err := service.Submit(payload)
		if err != nil {
			statusCode := http.StatusBadRequest
			if errors.Is(err, probe.ErrQueueFull) {
				statusCode = http.StatusTooManyRequests
			}
			if errors.Is(err, probe.ErrClosed) {
				statusCode = http.StatusServiceUnavailable
			}
			writeError(writer, statusCode, err)
			return
		}
		writeJSON(writer, http.StatusAccepted, job)
	}
}

func handleProbeJobDetail(service *probe.Service) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeMethodNotAllowed(writer)
			return
		}

		id := strings.TrimPrefix(request.URL.Path, "/v1/probe/jobs/")
		id = strings.TrimSpace(id)
		if id == "" {
			writeErrorMessage(writer, http.StatusBadRequest, "job id is required")
			return
		}

		job, ok := service.GetJob(id)
		if !ok {
			writeErrorMessage(writer, http.StatusNotFound, "job not found")
			return
		}
		writeJSON(writer, http.StatusOK, job)
	}
}

func decodeRequest(writer http.ResponseWriter, request *http.Request) (probe.Request, bool) {
	request.Body = http.MaxBytesReader(writer, request.Body, 32*1024)
	defer request.Body.Close()

	var payload probe.Request
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&payload); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorMessage(writer, http.StatusRequestEntityTooLarge, "request body exceeds limit")
			return probe.Request{}, false
		}
		writeErrorMessage(writer, http.StatusBadRequest, "invalid json body")
		return probe.Request{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorMessage(writer, http.StatusRequestEntityTooLarge, "request body exceeds limit")
			return probe.Request{}, false
		}
		writeErrorMessage(writer, http.StatusBadRequest, "expected one JSON document")
		return probe.Request{}, false
	}
	return payload, true
}

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(writer, request)
	})
}

func writeMethodNotAllowed(writer http.ResponseWriter) {
	writeErrorMessage(writer, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(writer http.ResponseWriter, statusCode int, err error) {
	writeErrorMessage(writer, statusCode, err.Error())
}

func writeErrorMessage(writer http.ResponseWriter, statusCode int, message string) {
	writeJSON(writer, statusCode, map[string]any{
		"error": message,
	})
}

func writeJSON(writer http.ResponseWriter, statusCode int, payload any) {
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(payload)
}
