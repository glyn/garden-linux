package system_info_test

import (
	. "github.com/cloudfoundry-incubator/garden-linux/old/system_info"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("SystemInfo", func() {
	var provider Provider

	BeforeEach(func() {
		provider = NewProvider("/")
	})

	It("provides nonzero memory and disk information", func() {
		totalMemory, err := provider.TotalMemory()
		Ω(err).ShouldNot(HaveOccurred())

		totalDisk, err := provider.TotalDisk()
		Ω(err).ShouldNot(HaveOccurred())

		Ω(totalMemory).Should(BeNumerically(">", 0))
		Ω(totalDisk).Should(BeNumerically(">", 0))
	})
})
