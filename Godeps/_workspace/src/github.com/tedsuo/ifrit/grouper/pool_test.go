package grouper_test

import (
	"os"
	"time"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/fake_runner"
	"github.com/tedsuo/ifrit/grouper"
)

var _ = Describe("Pool", func() {
	var (
		client      grouper.PoolClient
		pool        *grouper.Pool
		poolProcess ifrit.Process

		childRunner1 *fake_runner.TestRunner
		childRunner2 *fake_runner.TestRunner
		childRunner3 *fake_runner.TestRunner
	)

	BeforeEach(func() {
		childRunner1 = fake_runner.NewTestRunner()
		childRunner2 = fake_runner.NewTestRunner()
		childRunner3 = fake_runner.NewTestRunner()
	})
	AfterEach(func() {
		childRunner1.EnsureExit()
		childRunner2.EnsureExit()
		childRunner3.EnsureExit()
	})

	Describe("Insert", func() {
		var member1, member2, member3 grouper.Member

		BeforeEach(func() {
			member1 = grouper.Member{"child1", childRunner1}
			member2 = grouper.Member{"child2", childRunner2}
			member3 = grouper.Member{"child3", childRunner3}

			pool = grouper.NewPool(nil, 3, 2)
			client = pool.Client()
			poolProcess = ifrit.Envoke(pool)

			insert := client.Insert()
			Eventually(insert).Should(BeSent(member1))
			Eventually(insert).Should(BeSent(member2))
			Eventually(insert).Should(BeSent(member3))
		})

		AfterEach(func() {
			poolProcess.Signal(os.Kill)
			Eventually(poolProcess.Wait()).Should(Receive())
		})

		It("announces the events as processes move through their lifecycle", func() {
			entrance1, entrance2, entrance3 := grouper.EntranceEvent{}, grouper.EntranceEvent{}, grouper.EntranceEvent{}
			exit1, exit2, exit3 := grouper.ExitEvent{}, grouper.ExitEvent{}, grouper.ExitEvent{}

			entrances := client.NewEntranceListener()
			exits := client.NewExitListener()

			childRunner2.TriggerReady()
			Eventually(entrances).Should(Receive(&entrance2))
			Ω(entrance2.Member).Should(Equal(member2))

			childRunner1.TriggerReady()
			Eventually(entrances).Should(Receive(&entrance1))
			Ω(entrance1.Member).Should(Equal(member1))

			childRunner3.TriggerReady()
			Eventually(entrances).Should(Receive(&entrance3))
			Ω(entrance3.Member).Should(Equal(member3))

			childRunner2.TriggerExit(nil)
			Eventually(exits).Should(Receive(&exit2))
			Ω(exit2.Member).Should(Equal(member2))

			childRunner1.TriggerExit(nil)
			Eventually(exits).Should(Receive(&exit1))
			Ω(exit1.Member).Should(Equal(member1))

			childRunner3.TriggerExit(nil)
			Eventually(exits).Should(Receive(&exit3))
			Ω(exit3.Member).Should(Equal(member3))
		})

		It("announces the most recent events that have already occured, up to the buffer size", func() {
			entrance2, entrance3 := grouper.EntranceEvent{}, grouper.EntranceEvent{}
			exit2, exit3 := grouper.ExitEvent{}, grouper.ExitEvent{}

			childRunner1.TriggerReady()
			childRunner2.TriggerReady()
			childRunner3.TriggerReady()
			time.Sleep(time.Millisecond)

			entrances := client.NewEntranceListener()

			Eventually(entrances).Should(Receive(&entrance2))
			Ω(entrance2.Member).Should(Equal(member2))

			Eventually(entrances).Should(Receive(&entrance3))
			Ω(entrance3.Member).Should(Equal(member3))

			Consistently(entrances).ShouldNot(Receive())

			childRunner1.TriggerExit(nil)
			childRunner2.TriggerExit(nil)
			childRunner3.TriggerExit(nil)
			time.Sleep(time.Millisecond)

			exits := client.NewExitListener()
			Eventually(exits).Should(Receive(&exit2))
			Ω(exit2.Member).Should(Equal(member2))

			Eventually(exits).Should(Receive(&exit3))
			Ω(exit3.Member).Should(Equal(member3))

			Consistently(exits).ShouldNot(Receive())
		})
	})
})
