package ocpbugs48050

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

func accessLogsHandler(w http.ResponseWriter, _ *http.Request) {
	crw, ok := w.(*customResponseWriter)
	if !ok {
		crw.httpError("Expected a CustomResponseWriter", http.StatusInternalServerError)
		return
	}

	var logs []ConnectionLog

	crw.connectionHandler.responseLogs.Range(func(key, value interface{}) bool {
		logEntry, ok := value.(*ConnectionLog)
		if ok && logEntry.Complete {
			logs = append(logs, *logEntry)
		}
		return true
	})

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].ConnID < logs[j].ConnID
	})

	body, err := json.Marshal(logs)
	if err != nil {
		crw.httpError(fmt.Sprintf("Failed to marshall access logs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	fmt.Fprint(w, string(body))
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, "/healthz")
}

func singleTransferEncodingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	fmt.Fprint(w, "/single-te")

}

func duplicateTransferEncodingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Add("Transfer-Encoding", "chunked")
	fmt.Fprint(w, "/duplicate-te")
}

func discoveryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	fmt.Fprint(w, "/duplicate-te /single-te /healthz")
}
