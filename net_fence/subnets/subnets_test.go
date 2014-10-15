package subnets_test

import (
	"net"
	"runtime"

	"github.com/cloudfoundry-incubator/garden-linux/net_fence/subnets"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Subnet Pool", func() {
	var manager subnets.Manager
	var defaultSubnetPool *net.IPNet

	JustBeforeEach(func() {
		var err error
		manager, err = subnets.New(defaultSubnetPool)
		Ω(err).ShouldNot(HaveOccurred())
	})

	Describe("Capacity", func() {
		Context("when the dynamic allocation net is empty", func() {
			BeforeEach(func() {
				var err error
				_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/32")
				Ω(err).ShouldNot(HaveOccurred())
			})

			It("returns zero", func() {
				Ω(manager.Capacity()).Should(Equal(0))
			})
		})
		Context("when the dynamic allocation net is non-empty", func() {
			BeforeEach(func() {
				var err error
				_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/29")
				Ω(err).ShouldNot(HaveOccurred())
			})

			It("returns the correct number of subnets initially and repeatedly", func() {
				Ω(manager.Capacity()).Should(Equal(2))
				Ω(manager.Capacity()).Should(Equal(2))
			})

			It("returns the correct capacity after allocating subnets", func() {
				cap := manager.Capacity()

				_, err := manager.AllocateDynamically()
				Ω(err).ShouldNot(HaveOccurred())

				Ω(manager.Capacity()).Should(Equal(cap))

				_, err = manager.AllocateDynamically()
				Ω(err).ShouldNot(HaveOccurred())

				Ω(manager.Capacity()).Should(Equal(cap))
			})
		})
	})

	Describe("Allocating and Releasing", func() {

		Describe("Static Allocation", func() {
			Context("when the requested IP is within the dynamic allocation range", func() {
				BeforeEach(func() {
					var err error
					_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/29")
					Ω(err).ShouldNot(HaveOccurred())
				})

				It("returns an appropriate error", func() {
					_, static, err := net.ParseCIDR("10.2.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.AllocateStatically(static)
					Ω(err).Should(HaveOccurred())
					Ω(err).Should(Equal(subnets.ErrAlreadyAllocated))
				})
			})

			Context("when the requested subnet is not within the dynamic allocation range", func() {
				BeforeEach(func() {
					var err error
					_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/29")
					Ω(err).ShouldNot(HaveOccurred())
				})

				Context("the first request", func() {
					It("does not return an error", func() {
						_, static, err := net.ParseCIDR("10.9.3.4/30")
						Ω(err).ShouldNot(HaveOccurred())

						err = manager.AllocateStatically(static)
						Ω(err).ShouldNot(HaveOccurred())
					})
				})

				Context("after it has been allocated, a subsequent request", func() {
					var (
						static *net.IPNet
					)

					JustBeforeEach(func() {
						var err error
						_, static, err = net.ParseCIDR("10.9.3.4/30")
						Ω(err).ShouldNot(HaveOccurred())

						err = manager.AllocateStatically(static)
						Ω(err).ShouldNot(HaveOccurred())
					})

					It("returns an appropriate error", func() {
						err := manager.AllocateStatically(static)
						Ω(err).Should(HaveOccurred())
						Ω(err).Should(Equal(subnets.ErrAlreadyAllocated))
					})

					Context("but after it is released", func() {
						It("should allow allocation again", func() {
							err := manager.Release(static)
							Ω(err).ShouldNot(HaveOccurred())

							err = manager.AllocateStatically(static)
							Ω(err).ShouldNot(HaveOccurred())
						})
					})
				})
			})

		})

		Describe("Recovering", func() {
			BeforeEach(func() {
				var err error
				_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/29")
				Ω(err).ShouldNot(HaveOccurred())
			})

			Context("an allocation outside the dynamic allocation net", func() {

				It("recovers the first time", func() {
					_, static, err := net.ParseCIDR("10.9.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())
				})
				It("does not allow recovering twice", func() {
					_, static, err := net.ParseCIDR("10.9.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).Should(HaveOccurred())
				})
				It("does not allow allocating after recovery", func() {
					_, static, err := net.ParseCIDR("10.9.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.AllocateStatically(static)
					Ω(err).Should(HaveOccurred())
				})
			})

			Context("an allocation inside the dynamic allocation net", func() {

				It("recovers the first time", func() {
					_, static, err := net.ParseCIDR("10.2.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())
				})
				It("does not allow recovering twice", func() {
					_, static, err := net.ParseCIDR("10.2.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).Should(HaveOccurred())
				})
				It("does not dynamically allocate a recovered network", func() {
					_, static, err := net.ParseCIDR("10.2.3.4/30")
					Ω(err).ShouldNot(HaveOccurred())

					err = manager.Recover(static)
					Ω(err).ShouldNot(HaveOccurred())

					network, err := manager.AllocateDynamically()
					Ω(err).ShouldNot(HaveOccurred())

					Ω(network).ShouldNot(BeNil())
					Ω(network.String()).ShouldNot(Equal("10.2.3.4/30"))

					_, err = manager.AllocateDynamically()
					Ω(err).Should(HaveOccurred())
				})
			})

		})

		Describe("Dynamic Allocation", func() {
			Context("when the pool does not have sufficient IPs to allocate a subnet", func() {
				BeforeEach(func() {
					var err error
					_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/31")
					Ω(err).ShouldNot(HaveOccurred())
				})

				It("the first request returns an error", func() {
					_, err := manager.AllocateDynamically()
					Ω(err).Should(HaveOccurred())
				})
			})

			Context("when the pool has sufficient IPs to allocate a single subnet", func() {
				BeforeEach(func() {
					var err error
					_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/30")
					Ω(err).ShouldNot(HaveOccurred())
				})

				Context("the first request", func() {
					It("succeeds, and returns a /30 network within the subnet", func() {
						network, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						Ω(network).ShouldNot(BeNil())
						Ω(network.String()).Should(Equal("10.2.3.0/30"))
					})
				})

				Context("subsequent requests", func() {
					It("fail, and return an err", func() {
						_, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						_, err = manager.AllocateDynamically()
						Ω(err).Should(HaveOccurred())
					})
				})

				Context("when an allocated network is released", func() {
					It("a subsequent allocation succeeds, and returns the first network again", func() {
						// first
						allocated, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						// second - will fail (sanity check)
						_, err = manager.AllocateDynamically()
						Ω(err).Should(HaveOccurred())

						// release
						err = manager.Release(allocated)
						Ω(err).ShouldNot(HaveOccurred())

						// third - should work now because of release
						network, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						Ω(network).ShouldNot(BeNil())
						Ω(network.String()).Should(Equal(allocated.String()))
					})
				})

				Context("when a network is released twice", func() {
					It("returns an error", func() {
						// first
						allocated, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						// release
						err = manager.Release(allocated)
						Ω(err).ShouldNot(HaveOccurred())

						// release again
						err = manager.Release(allocated)
						Ω(err).Should(HaveOccurred())
						Ω(err).Should(Equal(subnets.ErrReleasedUnallocatedNetwork))
					})
				})
			})

			Context("when the pool has sufficient IPs to allocate two subnets", func() {
				BeforeEach(func() {
					var err error
					_, defaultSubnetPool, err = net.ParseCIDR("10.2.3.0/29")
					Ω(err).ShouldNot(HaveOccurred())
				})

				Context("the second request", func() {
					It("succeeds", func() {
						_, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						_, err = manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())
					})

					It("returns the second /30 network within the subnet", func() {
						_, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						network, err := manager.AllocateDynamically()
						Ω(err).ShouldNot(HaveOccurred())

						Ω(network).ShouldNot(BeNil())
						Ω(network.String()).Should(Equal("10.2.3.4/30"))
					})
				})

				It("allocates distinct networks concurrently", func() {
					prev := runtime.GOMAXPROCS(2)
					defer runtime.GOMAXPROCS(prev)

					Consistently(func() bool {
						_, network, err := net.ParseCIDR("10.0.0.0/29")
						Ω(err).ShouldNot(HaveOccurred())

						pool, err := subnets.New(network)
						Ω(err).ShouldNot(HaveOccurred())

						out := make(chan *net.IPNet)
						go func(out chan *net.IPNet) {
							defer GinkgoRecover()
							n1, err := pool.AllocateDynamically()
							Ω(err).ShouldNot(HaveOccurred())
							out <- n1
						}(out)

						go func(out chan *net.IPNet) {
							defer GinkgoRecover()
							n1, err := pool.AllocateDynamically()
							Ω(err).ShouldNot(HaveOccurred())
							out <- n1
						}(out)

						a := <-out
						b := <-out
						return a.IP.Equal(b.IP)
					}, "100ms", "2ms").ShouldNot(BeTrue())
				})

				PIt("releases networks concurrently", func() {})
			})
		})

	})
})
