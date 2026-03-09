package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/dbfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Webhooks API", func() {
	var response *http.Response

	BeforeEach(func() {
		dbTeamFactory.FindTeamReturns(dbTeam, true, nil)
	})

	Describe("PUT /api/v1/teams/:team_name/webhooks/:webhook_name", func() {
		var setWebhook atc.Webhook

		BeforeEach(func() {
			setWebhook = atc.Webhook{
				Type:            "github",
				Secret:          "my-secret",
				SignatureHeader: "X-Hub-Signature-256",
				SignatureAlgo:   atc.SignatureAlgoHMACSHA256,
				Rules: []atc.WebhookMatcherRule{
					{SourceField: "uri", PayloadField: "repository.url"},
					{SourceField: "branch", PayloadField: "ref"},
				},
			}
		})

		JustBeforeEach(func() {
			payload, err := json.Marshal(setWebhook)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/a-team/webhooks/my-webhook", bytes.NewBuffer(payload))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated and authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when saving the webhook succeeds", func() {
				var fakeWebhook *dbfakes.FakeWebhook

				BeforeEach(func() {
					fakeWebhook = new(dbfakes.FakeWebhook)
					fakeWebhook.NameReturns("my-webhook")
					fakeWebhook.TypeReturns("github")
					fakeWebhook.SecretReturns("my-secret")
					fakeWebhook.SignatureHeaderReturns("X-Hub-Signature-256")
					fakeWebhook.SignatureAlgoReturns(atc.SignatureAlgoHMACSHA256)
					fakeWebhook.RulesReturns([]atc.WebhookMatcherRule{
						{SourceField: "uri", PayloadField: "repository.url"},
						{SourceField: "branch", PayloadField: "ref"},
					})
					
					dbTeam.SaveWebhookReturns(fakeWebhook, nil)
				})

				It("returns 201 Created", func() {
					body, _ := io.ReadAll(response.Body)
					Expect(response.StatusCode).To(Equal(http.StatusCreated), "Response body: %s", string(body))
				})

				It("calls SaveWebhook with the parsed webhook configuration", func() {
					Expect(dbTeam.SaveWebhookCallCount()).To(Equal(1))
					
					cfg := dbTeam.SaveWebhookArgsForCall(0)
					Expect(cfg.Name).To(Equal("my-webhook"))
					Expect(cfg.Type).To(Equal("github"))
					Expect(cfg.Secret).To(Equal("my-secret"))
					Expect(cfg.SignatureHeader).To(Equal("X-Hub-Signature-256"))
					Expect(cfg.SignatureAlgo).To(Equal(atc.SignatureAlgoHMACSHA256))
					Expect(cfg.Rules).To(Equal([]atc.WebhookMatcherRule{
						{SourceField: "uri", PayloadField: "repository.url"},
						{SourceField: "branch", PayloadField: "ref"},
					}))
				})

				It("returns the saved webhook in the response", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchJSON(`{
						"id": 0,
						"name": "my-webhook",
						"type": "github",
						"secret": "[configured]",
						"signature_header": "X-Hub-Signature-256",
						"signature_algo": "hmac-sha256",
						"team_id": 0,
						"url": "https://example.com/api/v1/teams//webhooks/my-webhook",
						"rules": [
							{"source_field": "uri", "payload_field": "repository.url"},
							{"source_field": "branch", "payload_field": "ref"}
						]
					}`))
				})
			})

			Context("when saving the webhook fails validation", func() {
				BeforeEach(func() {
					// e.g. Name conflict or something
					dbTeam.SaveWebhookReturns(nil, errors.New("db error"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})
})
