package net_fence_test

import (
	"github.com/cloudfoundry-incubator/garden-linux/net_fence"

	"flag"
	"github.com/cloudfoundry-incubator/garden-linux/net_fence/ip_pool"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"net"
)

var _ = Describe("Network Fence Flags", func() {

	Describe("The networkPool flag", func() {

		var (
			flagset *flag.FlagSet
			ipNet   *net.IPNet
			cmdline []string
		)

		JustBeforeEach(func() {
			net_fence.NewIpPoolFromIPNet = func(ipn *net.IPNet) (ip_pool.IPPool, error) {
				ipNet = ipn
				return nil, nil
			}

			flagset = &flag.FlagSet{}
			net_fence.InitializeFlags(flagset)

			flagset.Parse(cmdline)

		})

		Context("when not supplied", func() {
			BeforeEach(func() {
				cmdline = []string{}
			})

			It("configured the network pool with the default value", func() {
				err := net_fence.Initialize()
				Ω(err).ShouldNot(HaveOccurred())

				_, network, err := net.ParseCIDR(net_fence.DefaultNetworkPool)
				Ω(err).ShouldNot(HaveOccurred())
				Ω(ipNet).Should(Equal(network))
			})
		})

		Context("when supplied", func() {
			Context("and when it's valid", func() {
				BeforeEach(func() {
					cmdline = []string{"-networkPool=1.2.3.4/5"}
				})

				It("configures the network pool with the given value", func() {
					err := net_fence.Initialize()
					Ω(err).ShouldNot(HaveOccurred())

					_, network, err := net.ParseCIDR("1.2.3.4/5")
					Ω(err).ShouldNot(HaveOccurred())
					Ω(ipNet).Should(Equal(network))
				})
			})

			Context("and when it's not valid", func() {
				BeforeEach(func() {
					cmdline = []string{`-networkPool="1.2.3.4/5"`} // flags cannot contain quotes
				})

				It("returns an error", func() {
					err := net_fence.Initialize()
					Ω(err).Should(HaveOccurred())
				})

				It("names the invalid parameter in the error message", func() {
					err := net_fence.Initialize()
					Ω(err).Should(HaveOccurred())
					Ω(err.Error()).Should(ContainSubstring("networkPool"))
				})
			})
		})

	})

})
