// The subnets package provides a subnet pool from which networks may be dynamically acquired or
// statically reserved.
package subnets

import (
	"errors"
	"net"
	"sync"
)

// A Manager provides network allocations.
type Manager interface {
	// Dynamically allocates a /30 subnet, or returns an error if no more subnets can be allocated by this manager
	AllocateDynamically() (*net.IPNet, error)

	// Statically allocates a /30 subnet, and returns an error if the address has already been allocated
	AllocateStatically(*net.IPNet) error

	// Releases a previously-allocated network back to the pool
	Release(*net.IPNet) error

	// FIXME
	PoolFoxNetworkBad() string
}

type manager struct {
	network *net.IPNet
	pool    []*net.IPNet
	static  []*net.IPNet

	mutex sync.Mutex
}

var (
	ErrInsufficientSubnets        = errors.New("Insufficient subnets remaining in the pool")
	ErrInvalidRange               = errors.New("Invalid IP Range")
	ErrReleasedUnallocatedNetwork = errors.New("cannot release an unallocated network")
	ErrAlreadyAllocated           = errors.New("Subnet has already been allocated")
)

var slash30mask net.IPMask

func init() {
	_, maskedNetwork, err := net.ParseCIDR("1.1.1.1/30")
	if err != nil {
		panic("Does not compute")
	}

	slash30mask = maskedNetwork.Mask
}

func New(network *net.IPNet) (Manager, error) {
	min := network.IP
	max := make([]byte, len(min))
	for i, b := range network.Mask {
		max[i] = min[i] | ^b
	}

	pool := make([]*net.IPNet, 0)
	for ip := min; network.Contains(ip); ip = next(ip) {
		subnet := &net.IPNet{ip, slash30mask}
		ip = next(next(next(ip)))
		if network.Contains(ip) {
			pool = append(pool, subnet)
		}
	}

	return &manager{network: network, pool: pool}, nil
}

func (m *manager) PoolFoxNetworkBad() string {
	return m.network.String()
}

func (m *manager) AllocateStatically(request *net.IPNet) error {
	if m.network.Contains(request.IP) {
		return ErrAlreadyAllocated
	}

	for _, s := range m.static {
		if s.Contains(request.IP) {
			return ErrAlreadyAllocated
		}
	}

	m.static = append(m.static, request)

	return nil
}

func (m *manager) AllocateDynamically() (*net.IPNet, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if len(m.pool) == 0 {
		return nil, ErrInsufficientSubnets
	}

	acquired := m.pool[0]
	m.pool = m.pool[1:]

	return acquired, nil
}

func (m *manager) Release(network *net.IPNet) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, n := range m.pool {
		if n == network {
			return ErrReleasedUnallocatedNetwork
		}
	}

	found := -1
	for i, s := range m.static {
		if s == network {
			found = i
		}
	}

	if found > -1 {
		m.static = append(m.static[:found], m.static[found+1:]...)
	}

	m.pool = append(m.pool, network)
	return nil
}

func next(ip net.IP) net.IP {
	next := clone(ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			return next
		}
	}

	panic("overflowed maximum IP")
}

func clone(ip net.IP) net.IP {
	clone := make([]byte, len(ip))
	copy(clone, ip)
	return clone
}
