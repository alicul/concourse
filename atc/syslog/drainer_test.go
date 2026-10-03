package syslog_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/concourse/concourse/v8/atc"
	"github.com/concourse/concourse/v8/atc/db"
	"github.com/concourse/concourse/v8/atc/db/dbfakes"
	"github.com/concourse/concourse/v8/atc/event"
	"github.com/concourse/concourse/v8/atc/syslog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newFakeBuild(id int) db.Build {
	fakeEventSource := new(dbfakes.FakeEventSource)

	msg1 := json.RawMessage(`{"time":1533744538,"payload":"build ` + strconv.Itoa(id) + ` log"}`)
	fakeEventSource.NextReturnsOnCall(0, event.Envelope{
		Data:    &msg1,
		Event:   "log",
		EventID: "1",
	}, nil)

	msg2 := json.RawMessage(`{"time":1533744538,"status":"build ` + strconv.Itoa(id) + ` status"}`)
	fakeEventSource.NextReturnsOnCall(1, event.Envelope{
		Data:    &msg2,
		Event:   "status",
		EventID: "2",
	}, nil)

	msg3 := json.RawMessage(`{"time":1533744538,"version":{"version":"0.0.1"},"metadata":[{"name":"version","value":"0.0.1"}]}`)
	fakeEventSource.NextReturnsOnCall(2, event.Envelope{
		Data:    &msg3,
		Event:   "finish-get",
		EventID: "3",
	}, nil)

	msg4 := json.RawMessage(`{"time":1533744538,"selected_worker":"example-worker"}`)
	fakeEventSource.NextReturnsOnCall(3, event.Envelope{
		Data:    &msg4,
		Event:   "selected-worker",
		EventID: "4",
	}, nil)

	msg5 := json.RawMessage(`{"time":1533744538}`)
	fakeEventSource.NextReturnsOnCall(4, event.Envelope{
		Data:    &msg5,
		Event:   "initialize-task",
		EventID: "5",
	}, nil)

	fakeEventSource.NextReturnsOnCall(5, event.Envelope{}, db.ErrEndOfBuildEventStream)

	fakeEventSource.NextReturns(event.Envelope{}, db.ErrEndOfBuildEventStream)

	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.EventsReturns(fakeEventSource, nil)
	fakeBuild.IDReturns(id)

	return fakeBuild
}

var _ = Describe("Drainer", func() {
	var fakeBuildFactory *dbfakes.FakeBuildFactory
	var server *testServer

	BeforeEach(func() {
		fakeBuildFactory = new(dbfakes.FakeBuildFactory)
		fakeBuildFactory.GetDrainableBuildsReturns([]db.Build{newFakeBuild(123), newFakeBuild(345)}, nil)
	})

	AfterEach(func() {
		server.Close()
	})

	Context("when there are builds that have not been drained", func() {
		Context("when tls is not set", func() {
			BeforeEach(func() {
				server = newTestServer(nil)
			})

			It("drains all build events by tcp", func() {
				testDrainer := syslog.NewDrainer("tcp", server.Addr, "test", []string{}, fakeBuildFactory)
				err := testDrainer.Run(context.TODO())
				Expect(err).NotTo(HaveOccurred())

				got := <-server.Messages
				Expect(got).To(ContainSubstring("build 123 log"))
				Expect(got).To(ContainSubstring("build 345 log"))
				Expect(got).To(ContainSubstring(`get {"version":{"version":"0.0.1"},"metadata":[{"name":"version","value":"0.0.1"}]}`))
				Expect(got).To(ContainSubstring("build 123 status"))
				Expect(got).To(ContainSubstring("build 345 status"))
				Expect(got).To(ContainSubstring("selected worker: example-worker"))
				Expect(got).To(ContainSubstring("task initializing"))
			}, 0.2)

			DescribeTable("encodes resource results as complete JSON", func(eventType, action string, version atc.Version, metadata atc.Metadata) {
				data, err := json.Marshal(struct {
					Time     int64        `json:"time"`
					Version  atc.Version  `json:"version"`
					Metadata atc.Metadata `json:"metadata"`
				}{1533744538, version, metadata})
				Expect(err).NotTo(HaveOccurred())
				msg := json.RawMessage(data)
				events := new(dbfakes.FakeEventSource)
				events.NextReturnsOnCall(0, event.Envelope{Data: &msg, Event: atc.EventType(eventType), EventID: "1"}, nil)
				events.NextReturns(event.Envelope{}, db.ErrEndOfBuildEventStream)
				build := new(dbfakes.FakeBuild)
				build.EventsReturns(events, nil)
				fakeBuildFactory.GetDrainableBuildsReturns([]db.Build{build}, nil)

				drainer := syslog.NewDrainer("tcp", server.Addr, "test", nil, fakeBuildFactory)
				Expect(drainer.Run(context.TODO())).To(Succeed())
				parts := strings.SplitN(<-server.Messages, action+" ", 2)
				Expect(parts).To(HaveLen(2))
				var result struct {
					Version  atc.Version  `json:"version"`
					Metadata atc.Metadata `json:"metadata"`
				}
				Expect(json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &result)).To(Succeed())
				Expect(result.Version).To(Equal(version))
				Expect(result.Metadata).To(Equal(metadata))
			},
				Entry("get with quotes and backslashes", "finish-get", "get", atc.Version{"ref": `a"b\c`}, atc.Metadata{{Name: `a"b`, Value: "c\\d\ne"}}),
				Entry("put with quotes and backslashes", "finish-put", "put", atc.Version{"ref": `a"b\c`}, atc.Metadata{{Name: `a"b`, Value: "c\\d\ne"}}),
				Entry("get with null values", "finish-get", "get", atc.Version(nil), atc.Metadata(nil)),
				Entry("put with null values", "finish-put", "put", atc.Version(nil), atc.Metadata(nil)),
			)
		})

	})
})
