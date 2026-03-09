package metric

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// WebhookReceived represents metrics for a received webhook event.
type WebhookReceived struct {
	TeamName    string
	WebhookName string
	WebhookType string
	Status      int // HTTP status code
	Duration    time.Duration
	MatchType   string // "jsonb_filter", "matcher", "fallback", or "none"
	MatchCount  int    // Number of resources matched
	ChecksCount int    // Number of checks triggered
}

func (event WebhookReceived) Emit(logger lager.Logger) {
	// Emit duration
	Metrics.emit(
		logger.Session("webhook-request-duration"),
		Event{
			Name:  "webhook request duration",
			Value: event.Duration.Seconds(),
			Attributes: map[string]string{
				"team":         event.TeamName,
				"webhook_name": event.WebhookName,
				"webhook_type": event.WebhookType,
				"status":       statusString(event.Status),
			},
		},
	)

	// Emit request counter
	Metrics.emit(
		logger.Session("webhook-request-total"),
		Event{
			Name:  "webhook request",
			Value: 1,
			Attributes: map[string]string{
				"team":         event.TeamName,
				"webhook_name": event.WebhookName,
				"webhook_type": event.WebhookType,
				"status":       statusString(event.Status),
			},
		},
	)

	// Only emit match/check metrics for successful webhooks
	if event.Status == 200 {
		// Emit resources matched
		Metrics.emit(
			logger.Session("webhook-resources-matched"),
			Event{
				Name:  "webhook resources matched",
				Value: float64(event.MatchCount),
				Attributes: map[string]string{
					"team":         event.TeamName,
					"webhook_name": event.WebhookName,
					"webhook_type": event.WebhookType,
					"match_type":   event.MatchType,
				},
			},
		)

		// Emit checks triggered
		Metrics.emit(
			logger.Session("webhook-checks-triggered"),
			Event{
				Name:  "webhook checks triggered",
				Value: float64(event.ChecksCount),
				Attributes: map[string]string{
					"team":         event.TeamName,
					"webhook_name": event.WebhookName,
					"webhook_type": event.WebhookType,
				},
			},
		)
	}
}

func statusString(code int) string {
	// Group status codes for better cardinality
	if code >= 200 && code < 300 {
		return "2xx"
	} else if code >= 400 && code < 500 {
		return "4xx"
	} else if code >= 500 {
		return "5xx"
	}
	return "other"
}
