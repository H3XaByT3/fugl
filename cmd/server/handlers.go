package main

import (
	"encoding/json"
	"github.com/rot256/fugl"
	"golang.org/x/crypto/openpgp"
	"net/http"
	"sync"
	"time"
)

type ServerState struct {
	storeDir       string          // directory for storing new canaries
	latestCanary   *fugl.Canary    // cached latest canary (parsed proof)
	latestProof    string          // newest proof
	canaryKey      *openpgp.Entity // parsed public key
	canaryKeyArmor string          // ascii armored pgp key
	canaryLock     sync.RWMutex
}

func SendRequestError(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte(msg))
	return
}

/* Serves the public key */

type GetKeyHandler struct {
	state *ServerState
}

func (h *GetKeyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.state.canaryKeyArmor == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(h.state.canaryKeyArmor))
}

/* Returns canary status on this node */

type StatusHandler struct {
	state *ServerState
}

func (h *StatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.state.canaryLock.RLock()
	defer h.state.canaryLock.RUnlock()

	// Generate info struct
	var status fugl.CanaryStatus
	status.Version = fugl.CanaryVersion
	status.Key = h.state.canaryKeyArmor
	status.Enabled = h.state.latestProof != ""

	// Serialize
	resp, err := json.MarshalIndent(status, "", "    ")
	if err != nil {
		logError("JSON error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/json")
	w.WriteHeader(http.StatusFound)
	w.Write(resp)
}

/* Serves the latest published canary */

type LatestHandler struct {
	state *ServerState
}

func (h *LatestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.state.canaryLock.RLock()
	defer h.state.canaryLock.RUnlock()
	if h.state.latestProof == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(h.state.latestProof))
}

/* Add a new canary */

type SubmitHandler struct {
	state *ServerState
}

func (h *SubmitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// parse and verify signature
	proof := r.PostFormValue(fugl.SERVER_SUBMIT_FIELD_NAME)
	logDebug("New proof submission:\n", proof)
	canary, err := fugl.OpenProof(h.state.canaryKey, proof)
	if err != nil {
		SendRequestError(w, err.Error())
	}

	// check version field
	if canary.Version != fugl.CanaryVersion {
		logWarning("Invalid canary version field")
		SendRequestError(w, "Unsupported canary version")
		return
	}

	// verify deadline in future (avoid bricking)
	if time.Now().After(canary.Deadline.Time()) {
		SendRequestError(w, "Canary must have a deadline in the future")
		return
	}

	// take write lock
	h.state.canaryLock.Lock()
	defer h.state.canaryLock.Unlock()

	// verify deadline after previous deadline
	if h.state.latestCanary != nil {
		if !canary.Deadline.Time().After(h.state.latestCanary.Deadline.Time()) {
			SendRequestError(w, "New canary deadline must be after previous deadline")
			return
		}
	}

	// verify previous canary hash
	if h.state.latestProof != "" {
		hash := fugl.HashString(proof)
		if hash != canary.Previous {
			SendRequestError(w, "Canary must reference preceeding canary hash")
			return
		}
	}

	// save to disk
	err = fugl.SaveToDirectory(proof, h.state.storeDir, canary.Deadline.Time())
	if err != nil {
		logError("Failed to save valid proof to store:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.state.latestProof = proof
	h.state.latestCanary = canary
	logInfo("Succesfully added a new canary")
	w.WriteHeader(http.StatusNoContent)
}
