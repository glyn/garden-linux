package fake_container_pool

import (
	"io"
	"sync"
	"time"

	"github.com/cloudfoundry-incubator/garden/api"
	"github.com/cloudfoundry-incubator/garden/api/fakes"
)

type FakeContainer struct {
	*fakes.FakeContainer

	Spec api.ContainerSpec

	SnapshotError  error
	SavedSnapshots []io.Writer
	snapshotMutex  *sync.RWMutex

	StartError error
	Started    bool
	Mtu        uint32

	CleanedUp bool
}

func NewFakeContainer(spec api.ContainerSpec) *FakeContainer {
	return &FakeContainer{
		Spec: spec,

		FakeContainer: new(fakes.FakeContainer),

		snapshotMutex: new(sync.RWMutex),
	}
}

func (c *FakeContainer) ID() string {
	return c.Spec.Handle
}

func (c *FakeContainer) Handle() string {
	return c.Spec.Handle
}

func (c *FakeContainer) Properties() api.Properties {
	return c.Spec.Properties
}

func (c *FakeContainer) Start(mtu uint32) error {
	c.Started = true
	c.Mtu = mtu
	return c.StartError
}

func (c *FakeContainer) Cleanup() {
	c.CleanedUp = true
}

func (c *FakeContainer) GraceTime() time.Duration {
	return c.Spec.GraceTime
}

func (c *FakeContainer) Snapshot(snapshot io.Writer) error {
	if c.SnapshotError != nil {
		return c.SnapshotError
	}

	c.snapshotMutex.Lock()
	defer c.snapshotMutex.Unlock()

	c.SavedSnapshots = append(c.SavedSnapshots, snapshot)

	return nil
}
