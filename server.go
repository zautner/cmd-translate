package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed web/*
var webAssets embed.FS

func runServer(addr string) error {
	webRoot, err := fs.Sub(webAssets, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", handleConfigAPI)
	mux.HandleFunc("GET /api/models", handleModelsAPI)
	mux.HandleFunc("POST /api/chat", handleChatAPI)
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("GUI: http://%s", trimHostPort(addr))
	return srv.ListenAndServe()
}

func trimHostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

type chatAPIRequest struct {
	Message  string        `json:"message"`
	History  []chatMessage `json:"history,omitempty"`
	Model    string        `json:"model,omitempty"`
	Provider string        `json:"provider,omitempty"`
	// GoogleAPIKey optional when GOOGLE_API_KEY is not set on the server (same as X-Google-API-Key header).
	GoogleAPIKey string `json:"google_api_key,omitempty"`
}

type chatAPIResponse struct {
	Reply          string `json:"reply"`               // full assistant text (may contain markdown)
	Command        string `json:"command,omitempty"`   // extracted shell command, if any
	Dangerous      bool   `json:"dangerous,omitempty"` // true when the command is flagged as dangerous
	Error          string `json:"error,omitempty"`
	NeedsGoogleKey bool   `json:"needs_google_key,omitempty"`
}

type modelsAPIResponse struct {
	Models         []string `json:"models,omitempty"`
	Error          string   `json:"error,omitempty"`
	NeedsGoogleKey bool     `json:"needs_google_key,omitempty"`
}

type configAPIResponse struct {
	GoogleKeyFromEnv bool `json:"google_key_from_env"`
}

func handleConfigAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(configAPIResponse{GoogleKeyFromEnv: GoogleKeyConfigured()})
}

func googleKeyFromRequest(r *http.Request, bodyKey string) string {
	if h := strings.TrimSpace(r.Header.Get("X-Google-API-Key")); h != "" {
		return h
	}
	return strings.TrimSpace(bodyKey)
}

func handleModelsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	key := googleKeyFromRequest(r, "")
	ids, err := ListModels(provider, key)
	if err != nil {
		status := http.StatusBadGateway
		resp := modelsAPIResponse{Error: err.Error()}
		if errors.Is(err, ErrGoogleAPIKeyRequired) {
			status = http.StatusBadRequest
			resp.NeedsGoogleKey = true
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	_ = json.NewEncoder(w).Encode(modelsAPIResponse{Models: ids})
}

func handleChatAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req chatAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(chatAPIResponse{Error: "invalid JSON body"})
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(chatAPIResponse{Error: "message is required"})
		return
	}

	log.Printf("chat: provider=%q model=%q message=%q", req.Provider, effectiveModel(req.Model, req.Provider), truncate(req.Message, 60))

	gkey := googleKeyFromRequest(r, req.GoogleAPIKey)
	reply, err := converse(req.History, req.Message, req.Model, req.Provider, gkey)
	if err != nil {
		status := http.StatusBadGateway
		resp := chatAPIResponse{Error: err.Error()}
		if errors.Is(err, ErrGoogleAPIKeyRequired) {
			status = http.StatusBadRequest
			resp.NeedsGoogleKey = true
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	_ = json.NewEncoder(w).Encode(chatAPIResponse{
		Reply:     reply.Text,
		Command:   reply.Command,
		Dangerous: reply.Dangerous,
	})
}
