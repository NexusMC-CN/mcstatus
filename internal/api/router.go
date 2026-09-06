package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/blockbridge/avmcbbs/apps/mcstatus/internal/probe"
)

func NewRouter(service *probe.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/v1/probe", handleProbe(service))
	mux.HandleFunc("/v1/probe/jobs", handleProbeJobs(service))
	mux.HandleFunc("/v1/probe/jobs/", handleProbeJobDetail(service))
	return withJSON(mux)
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
			writeError(writer, http.StatusBadRequest, err)
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
	defer request.Body.Close()

	var payload probe.Request
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeErrorMessage(writer, http.StatusBadRequest, "invalid json body")
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
