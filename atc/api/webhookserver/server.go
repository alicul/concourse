package webhookserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

type Server struct {
	logger       lager.Logger
	teamFactory  db.TeamFactory
	checkFactory db.CheckFactory
	externalURL  string
}

func NewServer(
	logger lager.Logger,
	teamFactory db.TeamFactory,
	checkFactory db.CheckFactory,
	externalURL string,
) *Server {
	return &Server{
		logger:       logger,
		teamFactory:  teamFactory,
		checkFactory: checkFactory,
		externalURL:  externalURL,
	}
}

// SetWebhook handles PUT /api/v1/teams/:team_name/webhooks/:webhook_name
func (s *Server) SetWebhook(dbTeam db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookName := rata.Param(r, "webhook_name")
		logger := s.logger.Session("set-webhook", lager.Data{"webhook": webhookName})

		var request atc.Webhook
		err := json.NewDecoder(r.Body).Decode(&request)
		if err != nil {
			logger.Error("failed-to-decode-request", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if request.Type == "" {
			logger.Info("missing-type")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"type is required"}`))
			return
		}

		webhook, err := dbTeam.SaveWebhook(db.WebhookConfig{
			Name:            webhookName,
			Type:            request.Type,
			Secret:          request.Secret,
			Rules:           request.Rules,
			SignatureHeader: request.SignatureHeader,
			SignatureAlgo:   request.SignatureAlgo,
		})
		if err != nil {
			logger.Error("failed-to-save-webhook", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// When a secret is configured, HMAC is the sole authentication mechanism.
		// ?token= is deliberately omitted to prevent leakage in logs/delivery history.
		var webhookURL string
		if webhook.Secret() != "" {
			webhookURL = fmt.Sprintf("%s/api/v1/teams/%s/webhooks/%s",
				s.externalURL, dbTeam.Name(), webhookName)
		} else {
			webhookURL = fmt.Sprintf("%s/api/v1/teams/%s/webhooks/%s?token=%s",
				s.externalURL, dbTeam.Name(), webhookName, webhook.Token())
		}

		response := atc.Webhook{
			ID:              webhook.ID(),
			Name:            webhook.Name(),
			Type:            webhook.Type(),
			Token:           webhook.Token(),
			Rules:           webhook.Rules(),
			SignatureHeader: webhook.SignatureHeader(),
			SignatureAlgo:   webhook.SignatureAlgo(),
			TeamID:          webhook.TeamID(),
			URL:             webhookURL,
		}

		// Don't reveal the secret in the response, but indicate if one is set
		if webhook.Secret() != "" {
			response.Secret = "[configured]"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(response)
	})
}

// DestroyWebhook handles DELETE /api/v1/teams/:team_name/webhooks/:webhook_name
func (s *Server) DestroyWebhook(dbTeam db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookName := rata.Param(r, "webhook_name")
		logger := s.logger.Session("destroy-webhook", lager.Data{"webhook": webhookName})

		err := dbTeam.DestroyWebhook(webhookName)
		if err != nil {
			logger.Error("failed-to-destroy-webhook", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// ListWebhooks handles GET /api/v1/teams/:team_name/webhooks
func (s *Server) ListWebhooks(dbTeam db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("list-webhooks")

		webhooks, err := dbTeam.Webhooks()
		if err != nil {
			logger.Error("failed-to-list-webhooks", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		var response []atc.Webhook
		for _, wh := range webhooks {
			entry := atc.Webhook{
				ID:     wh.ID(),
				Name:   wh.Name(),
				Type:   wh.Type(),
				TeamID: wh.TeamID(),
			}
			if wh.Secret() != "" {
				entry.Secret = "[configured]"
			}
			response = append(response, entry)
		}

		if response == nil {
			response = []atc.Webhook{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})
}

// ReceiveWebhook handles POST /api/v1/teams/:team_name/webhooks/:webhook_name
// This endpoint is unauthenticated — validation is done via token or HMAC signature.
//
// Authentication priority:
// 1. HMAC signature in header (if webhook has a secret configured)
// 2. Token in query parameter (backward compatible)
//
// Resource matching priority:
// 1. Explicit JSONB filter (from resource's webhook subscription config)
// 2. Operator-configured webhook matcher (from base resource type defaults)
// 3. Fallback: trigger all subscribed resources of matching type (no filter, no matcher)
func (s *Server) ReceiveWebhook(w http.ResponseWriter, r *http.Request) {
	webhookName := rata.Param(r, "webhook_name")
	teamName := rata.Param(r, "team_name")

	logger := s.logger.Session("receive-webhook", lager.Data{
		"webhook": webhookName,
		"team":    teamName,
	})

	// Find the team
	team, found, err := s.teamFactory.FindTeam(teamName)
	if err != nil {
		logger.Error("failed-to-find-team", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		logger.Info("team-not-found")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Find the webhook
	webhook, found, err := team.FindWebhook(webhookName)
	if err != nil {
		logger.Error("failed-to-find-webhook", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		logger.Info("webhook-not-found")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Read the payload body (needed for both HMAC validation and matching)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("failed-to-read-body", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// --- Authentication ---
	if !s.validateWebhookAuth(logger, webhook, r, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Default to empty JSON object if no body
	payload := json.RawMessage(body)
	if len(body) == 0 {
		payload = json.RawMessage("{}")
	}

	// --- Resource matching ---
	resources, err := s.findMatchingResources(logger, team, webhook, payload)
	if err != nil {
		logger.Error("failed-to-find-matching-resources", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	logger.Info("matched-resources", lager.Data{"count": len(resources)})

	// --- Trigger checks ---
	checksCreated := 0
	var lastBuild db.Build
	for _, resource := range resources {
		pipeline, found, err := team.Pipeline(atc.PipelineRef{
			Name:         resource.PipelineName(),
			InstanceVars: resource.PipelineInstanceVars(),
		})
		if err != nil || !found {
			logger.Error("failed-to-get-pipeline", err, lager.Data{"pipeline": resource.PipelineName()})
			continue
		}

		resourceTypes, err := pipeline.ResourceTypes()
		if err != nil {
			logger.Error("failed-to-get-resource-types", err)
			continue
		}

		build, created, err := s.checkFactory.TryCreateCheck(
			lagerctx.NewContext(context.Background(), logger),
			resource,
			resourceTypes,
			nil,
			true,  // manually triggered (skip interval)
			false, // don't skip interval recursively
			true,  // write to DB
		)
		if err != nil {
			logger.Error("failed-to-create-check", err, lager.Data{"resource": resource.Name()})
			continue
		}

		if created {
			checksCreated++
			lastBuild = build
		}
	}

	response := struct {
		ChecksTriggered int        `json:"checks_triggered"`
		Build           *atc.Build `json:"build,omitempty"`
	}{
		ChecksTriggered: checksCreated,
	}

	if lastBuild != nil {
		b := present.Build(lastBuild, nil, nil)
		response.Build = &b
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// validateWebhookAuth validates the incoming webhook request.
//
// Authentication rules:
//   - Secret configured → signature header required; validated using the configured algo.
//     ?token= is NOT accepted — it is not included in the URL and falling back to it
//     would undermine the security guarantee.
//   - No secret configured → ?token= query parameter required (backward compatible).
//
// Signature header and algo are resolved from:
//  1. Per-webhook DB config (set via fly set-webhook)
//  2. Global operator matcher (CONCOURSE_WEBHOOK_MATCHERS)
//
// Supported algos:
//   "hmac-sha256" (default) — validates "sha256=<hex>" HMAC-SHA256 in the header
//   "plain"                 — compares the header value directly with the secret (GitLab style)
func (s *Server) validateWebhookAuth(logger lager.Logger, webhook db.Webhook, r *http.Request, body []byte) bool {
	if webhook.Secret() == "" {
		// Token-in-URL mode (no secret configured — backward compatible).
		token := r.URL.Query().Get("token")
		if token == "" {
			logger.Info("missing-token", lager.Data{"detail": "no ?token= query parameter"})
			return false
		}
		if !hmac.Equal([]byte(webhook.Token()), []byte(token)) {
			logger.Info("invalid-token")
			return false
		}
		return true
	}

	// HMAC/plain mode: ?token= is not accepted.
	sigHeader, sigAlgo := s.resolveSignatureConfig(webhook)
	if sigHeader == "" {
		logger.Info("missing-signature-header-config", lager.Data{
			"detail": "webhook has secret but no signature_header configured — cannot validate",
		})
		return false
	}

	sig := r.Header.Get(sigHeader)
	if sig == "" {
		logger.Info("missing-signature", lager.Data{
			"header": sigHeader,
			"detail": "webhook has secret configured but signature header was absent",
		})
		return false
	}

	switch sigAlgo {
	case atc.SignatureAlgoPlain:
		// GitLab style: the header value IS the secret.
		if !hmac.Equal([]byte(webhook.Secret()), []byte(sig)) {
			logger.Info("invalid-plain-signature")
			return false
		}
		return true
	default:
		// hmac-sha256 (default): header contains "sha256=<hex>".
		if !ValidateHMACSHA256(webhook.Secret(), body, sig) {
			logger.Info("invalid-hmac-signature")
			return false
		}
		return true
	}
}

// resolveSignatureConfig returns the (signatureHeader, signatureAlgo) to use
// for this webhook, checking per-webhook DB config first, then global matcher.
func (s *Server) resolveSignatureConfig(webhook db.Webhook) (header, algo string) {
	// Priority 1: per-webhook DB config (set via fly set-webhook).
	if webhook.SignatureHeader() != "" {
		algo = webhook.SignatureAlgo()
		if algo == "" {
			algo = atc.SignatureAlgoHMACSHA256
		}
		return webhook.SignatureHeader(), algo
	}
	// Priority 2: global operator matcher config.
	if m := atc.FindWebhookMatcher("", webhook.Type()); m != nil && m.SignatureHeader != "" {
		algo = m.SignatureAlgo
		if algo == "" {
			algo = atc.SignatureAlgoHMACSHA256
		}
		return m.SignatureHeader, algo
	}
	return "", ""
}

// ValidateHMACSHA256 validates a GitHub-style HMAC-SHA256 signature.
// The signature header value is expected to be in the format "sha256=<hex-digest>".
func ValidateHMACSHA256(secret string, body []byte, signatureHeader string) bool {
	// The signature format is "sha256=<hex>"
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}

	expectedSig := strings.TrimPrefix(signatureHeader, "sha256=")
	expectedBytes, err := hex.DecodeString(expectedSig)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	computedMAC := mac.Sum(nil)

	// Constant-time comparison to prevent timing attacks
	return hmac.Equal(computedMAC, expectedBytes)
}

// findMatchingResources finds resources that should be checked based on the
// webhook type and payload. It uses a priority chain:
// 1. Explicit JSONB filter (resources with non-empty filter in their subscription)
// 2. Operator-configured webhook matcher (from base resource type defaults)
// 3. Fallback: all subscribed resources of the matching type
func (s *Server) findMatchingResources(
	logger lager.Logger,
	team db.Team,
	webhook db.Webhook,
	payload json.RawMessage,
) ([]db.Resource, error) {
	// First, try JSONB containment matching (resources with explicit filters)
	filteredResources, err := team.FindResourcesByWebhookPayload(webhook.Type(), payload)
	if err != nil {
		return nil, err
	}

	if len(filteredResources) > 0 {
		logger.Info("matched-via-jsonb-filter", lager.Data{"count": len(filteredResources)})
		return filteredResources, nil
	}

	// No JSONB filter matches — try matcher-based filtering
	// Get all resources subscribed to this webhook type
	allSubscribed, err := team.FindResourcesByWebhookType(webhook.Type())
	if err != nil {
		return nil, err
	}

	if len(allSubscribed) == 0 {
		return nil, nil
	}

	// Try to apply matcher-based filtering.
	// For each resource, pick the matcher in priority order:
	//   1. Per-webhook DB matcher (set via fly set-webhook --rules-file)
	//   2. Global operator matcher (from CONCOURSE_WEBHOOK_MATCHERS config file)
	// If no matcher is found for a resource, it is included (fallback: trigger all).
	var matcherFiltered []db.Resource
	matcherUsed := false

	for _, resource := range allSubscribed {
		// Determine which matcher applies to this resource.
		var matcher *atc.WebhookMatcher

		// Priority 1: per-webhook DB rules.
		if m, hasMatcher := webhook.Matcher(); hasMatcher {
			matcher = m
		} else {
			// Priority 2: global operator config.
			matcher = atc.FindWebhookMatcher(resource.Type(), webhook.Type())
		}

		if matcher == nil {
			// No matcher at all — include this resource (fallback).
			continue
		}
		matcherUsed = true

		if matcher.MatchResourceSource(resource.Source(), payload) {
			matcherFiltered = append(matcherFiltered, resource)
		}
	}

	if matcherUsed {
		logger.Info("matched-via-matcher", lager.Data{"count": len(matcherFiltered)})
		return matcherFiltered, nil
	}

	// No matchers configured for any resource — return all subscribed resources.
	logger.Info("matched-via-fallback", lager.Data{"count": len(allSubscribed)})
	return allSubscribed, nil
}
