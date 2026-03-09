package integration_test

import (
	"os"
	"os/exec"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("set-webhook", func() {
	var (
		rulesFileName string
	)

	BeforeEach(func() {
		// create a temp rules file
		rulesFile, err := os.CreateTemp("", "rules-*.yml")
		Expect(err).NotTo(HaveOccurred())
		rulesFileName = rulesFile.Name()

		err = os.WriteFile(rulesFileName, []byte(`
- source_field: uri
  payload_field: repository.url
- source_field: branch
  payload_field: ref
`), 0644)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.Remove(rulesFileName)
	})

	Context("when setting a webhook succeeds", func() {
		It("sends the config and prints success", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/teams/main/webhooks/my-webhook"),
					ghttp.VerifyJSONRepresenting(atc.Webhook{
						Name:            "my-webhook",
						Type:            "github",
						Secret:          "my-secret",
						SignatureHeader: "X-Hub-Signature-256",
						SignatureAlgo:   "hmac-sha256",
						Rules: []atc.WebhookMatcherRule{
							{SourceField: "uri", PayloadField: "repository.url"},
							{SourceField: "branch", PayloadField: "ref"},
						},
					}),
					ghttp.RespondWithJSONEncoded(201, atc.Webhook{
						Name:            "my-webhook",
						Type:            "github",
						Secret:          "[configured]",
						SignatureHeader: "X-Hub-Signature-256",
						SignatureAlgo:   "hmac-sha256",
						TeamID:          1,
						URL:             "http://example.com/api/v1/teams/main/webhooks/my-webhook",
						Rules: []atc.WebhookMatcherRule{
							{SourceField: "uri", PayloadField: "repository.url"},
							{SourceField: "branch", PayloadField: "ref"},
						},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "set-webhook",
				"--webhook", "my-webhook",
				"--type", "github",
				"--secret", "my-secret",
				"--signature-header", "X-Hub-Signature-256",
				"--signature-algo", "hmac-sha256",
				"--rules-file", rulesFileName,
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("webhook saved"))
			Expect(sess.Out).To(gbytes.Say("http://example.com/api/v1/teams/main/webhooks/my-webhook"))
			Expect(sess.Out).To(gbytes.Say("HMAC signature validation is enabled"))
			Expect(sess.Out).To(gbytes.Say("configured matcher rules.*2"))
			Expect(sess.Out).To(gbytes.Say("uri → repository.url"))
			Expect(sess.Out).To(gbytes.Say("branch → ref"))
		})
	})

	Context("when setting a webhook without a secret", func() {
		It("sends the config and prints the URL with token warning", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/teams/main/webhooks/my-webhook-no-secret"),
					ghttp.VerifyJSONRepresenting(atc.Webhook{
						Name: "my-webhook-no-secret",
						Type: "gitlab",
						// rules are sent empty
					}),
					ghttp.RespondWithJSONEncoded(201, atc.Webhook{
						Name:   "my-webhook-no-secret",
						Type:   "gitlab",
						Token:  "some-random-token",
						TeamID: 1,
						URL:    "http://example.com/api/v1/teams/main/webhooks/my-webhook-no-secret?token=some-random-token",
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "set-webhook",
				"--webhook", "my-webhook-no-secret",
				"--type", "gitlab",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("webhook saved"))
			Expect(sess.Out).To(gbytes.Say(`http://example.com/api/v1/teams/main/webhooks/my-webhook-no-secret\?token=some-random-token`))
			Expect(sess.Out).To(gbytes.Say("No HMAC secret configured. Authentication is via \\?token= in the URL only"))
		})
	})

	Context("when the API returns an error", func() {
		It("exits non-zero and shows an error", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/teams/main/webhooks/my-webhook"),
					ghttp.RespondWith(500, "internal server error"),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "set-webhook", "--webhook", "my-webhook", "--type", "github")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("internal server error"))
		})
	})
})
