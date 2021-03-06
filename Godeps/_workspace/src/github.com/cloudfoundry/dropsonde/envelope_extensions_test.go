package dropsonde_test

import (
	"code.google.com/p/gogoprotobuf/proto"
	"github.com/cloudfoundry/dropsonde"
	"github.com/cloudfoundry/dropsonde/events"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("EnvelopeExtensions", func() {
	var testAppUuid = &events.UUID{
		Low:  proto.Uint64(1),
		High: proto.Uint64(2),
	}

	Describe("GetAppId", func() {
		Context("HttpStart", func() {
			It("returns the App ID if it has one", func() {
				envelope := &events.Envelope{
					EventType: events.Envelope_HttpStart.Enum(),
					HttpStart: &events.HttpStart{ApplicationId: testAppUuid},
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal("01000000-0000-0000-0200-000000000000"))
			})

			It("returns system app ID if there isn't an App ID", func() {
				envelope := &events.Envelope{
					EventType: events.Envelope_HttpStart.Enum(),
					HttpStart: &events.HttpStart{},
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal(dropsonde.SystemAppId))
			})
		})

		Context("HttpStop", func() {
			It("returns the App ID if it has one", func() {
				envelope := &events.Envelope{
					EventType: events.Envelope_HttpStop.Enum(),
					HttpStop:  &events.HttpStop{ApplicationId: testAppUuid},
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal("01000000-0000-0000-0200-000000000000"))
			})
		})

		Context("HttpStartStop", func() {
			It("returns the App ID if it has one", func() {
				envelope := &events.Envelope{
					EventType:     events.Envelope_HttpStartStop.Enum(),
					HttpStartStop: &events.HttpStartStop{ApplicationId: testAppUuid},
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal("01000000-0000-0000-0200-000000000000"))
			})
		})

		Context("LogMessage", func() {
			It("returns the App ID ", func() {
				envelope := &events.Envelope{
					EventType:  events.Envelope_LogMessage.Enum(),
					LogMessage: &events.LogMessage{AppId: proto.String("test-app-id")},
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal("test-app-id"))
			})
		})

		Context("Heartbeat", func() {
			It("returns the system app ID", func() {
				envelope := &events.Envelope{
					EventType: events.Envelope_Heartbeat.Enum(),
				}
				appId := dropsonde.GetAppId(envelope)
				Expect(appId).To(Equal(dropsonde.SystemAppId))
			})
		})
	})
})
