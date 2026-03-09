package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Team Webhooks", func() {
	Describe("SetWebhook", func() {
		var (
			expectedWebhook atc.Webhook
			requestConfig   concourse.WebhookConfig
		)

		BeforeEach(func() {
			expectedWebhook = atc.Webhook{
				ID:              123,
				Name:            "my-webhook",
				Type:            "github",
				Secret:          "[configured]",
				SignatureHeader: "X-Hub-Signature-256",
				SignatureAlgo:   "hmac-sha256",
				Rules: []atc.WebhookMatcherRule{
					{SourceField: "uri", PayloadField: "repository.url"},
				},
				TeamID: 1,
				URL:    "https://example.com/api/v1/teams/some-team/webhooks/my-webhook",
			}

			requestConfig = concourse.WebhookConfig{
				Name:            "my-webhook",
				Type:            "github",
				Secret:          "my-secret",
				SignatureHeader: "X-Hub-Signature-256",
				SignatureAlgo:   "hmac-sha256",
				Rules: []atc.WebhookMatcherRule{
					{SourceField: "uri", PayloadField: "repository.url"},
				},
			}

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/teams/some-team/webhooks/my-webhook"),
					ghttp.VerifyJSONRepresenting(atc.Webhook{
						Name:            "my-webhook",
						Type:            "github",
						Secret:          "my-secret",
						SignatureHeader: "X-Hub-Signature-256",
						SignatureAlgo:   "hmac-sha256",
						Rules: []atc.WebhookMatcherRule{
							{SourceField: "uri", PayloadField: "repository.url"},
						},
					}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, expectedWebhook),
				),
			)
		})

		It("sends the webhook config to the ATC and returns the response", func() {
			webhook, err := team.SetWebhook(requestConfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(webhook).To(Equal(expectedWebhook))
		})
	})

	Describe("DestroyWebhook", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/teams/some-team/webhooks/my-webhook"),
					ghttp.RespondWith(http.StatusNoContent, ""),
				),
			)
		})

		It("destroys the webhook securely", func() {
			err := team.DestroyWebhook("my-webhook")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("ListWebhooks", func() {
		var expectedWebhooks []atc.Webhook

		BeforeEach(func() {
			expectedWebhooks = []atc.Webhook{
				{
					Name: "hook-1",
					Type: "github",
				},
				{
					Name: "hook-2",
					Type: "gitlab",
				},
			}

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/teams/some-team/webhooks"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, expectedWebhooks),
				),
			)
		})

		It("returns the team's webhooks", func() {
			webhooks, err := team.ListWebhooks()
			Expect(err).NotTo(HaveOccurred())
			Expect(webhooks).To(Equal(expectedWebhooks))
		})
	})
})
