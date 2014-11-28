package network

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/cloudfoundry-incubator/garden-linux/fences"
	"github.com/cloudfoundry-incubator/garden-linux/fences/network/subnets"
	"github.com/cloudfoundry-incubator/garden-linux/old/sysconfig"
	"github.com/cloudfoundry-incubator/garden/api"
)

type f struct { // FIXME: rename f to fenceBuilder
	subnets.Subnets
	mtu        uint32
	externalIP net.IP
	binPath    string
}

type FlatFence struct {
	Ipn              string
	ContainerIP      string
	ContainerIfcName string
	HostIfcName      string
	SubnetShareable  bool
	BridgeIfcName    string
}

// Builds a (network) Fence from a given network spec. If the network spec
// is empty, dynamically allocates a subnet and IP. Otherwise, if the network
// spec specifies a subnet IP, allocates that subnet, and an available
// dynamic IP address. If the network has non-empty host bits, this exact IP
// address is statically allocated. In all cases, if an IP cannot be allocated which
// meets the requirements, an error is returned.
//
// The given allocation is stored in the returned fence.
func (f *f) Build(spec string, sysconfig *sysconfig.Config, containerID string) (fences.Fence, error) {
	var ipSelector subnets.IPSelector = subnets.DynamicIPSelector
	var subnetSelector subnets.SubnetSelector = subnets.DynamicSubnetSelector

	if spec != "" {
		specifiedIP, ipn, err := net.ParseCIDR(suffixIfNeeded(spec))
		if err != nil {
			return nil, err
		}

		subnetSelector = subnets.StaticSubnetSelector{ipn}

		if !specifiedIP.Equal(subnets.NetworkIP(ipn)) {
			ipSelector = subnets.StaticIPSelector{specifiedIP}
		}
	}

	subnet, containerIP, err := f.Subnets.Allocate(subnetSelector, ipSelector)
	if err != nil {
		return nil, err
	}

	prefix := sysconfig.NetworkInterfacePrefix
	maxIdLen := 14 - len(prefix) // 14 is maximum interface name size - room for "-0"

	var ifaceName string
	if len(containerID) < maxIdLen {
		ifaceName = containerID
	} else {
		ifaceName = containerID[len(containerID)-maxIdLen:]
	}

	containerIfcName := prefix + ifaceName + "-1"
	hostIfcName := prefix + ifaceName + "-0"
	bridgeIfcName := prefix + "br-" + hexIP(subnet.IP)

	ones, _ := subnet.Mask.Size()
	subnetShareable := (ones < 30)

	return &Allocation{subnet, containerIP, containerIfcName, hostIfcName, subnetShareable, bridgeIfcName, f}, nil
}

func suffixIfNeeded(spec string) string {
	if !strings.Contains(spec, "/") {
		spec = spec + "/30"
	}

	return spec
}

// Rebuilds a Fence from the marshalled JSON from an existing Fence's MarshalJSON method.
// Returns an error if any of the allocations stored in the recovered fence are no longer
// available.
func (f *f) Rebuild(rm *json.RawMessage) (fences.Fence, error) {
	ff := FlatFence{}
	if err := json.Unmarshal(*rm, &ff); err != nil {
		return nil, err
	}

	_, ipn, err := net.ParseCIDR(ff.Ipn)
	if err != nil {
		return nil, err
	}

	if err := f.Subnets.Recover(ipn, net.ParseIP(ff.ContainerIP)); err != nil {
		return nil, err
	}

	return &Allocation{ipn, net.ParseIP(ff.ContainerIP), ff.ContainerIfcName, ff.HostIfcName, ff.SubnetShareable, ff.BridgeIfcName, f}, nil
}

type Allocation struct {
	*net.IPNet
	containerIP      net.IP
	containerIfcName string
	hostIfcName      string
	subnetShareable  bool
	bridgeIfcName    string
	fence            *f // FIXME: rename fence to fenceBldr
}

func (a *Allocation) String() string {
	return "Allocation{" + a.IPNet.String() + ", " + a.containerIP.String() + "}" // FIXME: fill this out
}

func (a *Allocation) Erect(containerPid int) error {

	err := ConfigureHost(a.hostIfcName, a.containerIfcName, subnets.GatewayIP(a.IPNet), a.subnetShareable, a.bridgeIfcName, a.IPNet, containerPid, int(a.fence.mtu))
	if err != nil {
		fmt.Println("ConfigureHost failed:", err)
		return err
	}

	// [ ! -d /var/run/netns ] && mkdir -p /var/run/netns
	err = os.MkdirAll("/var/run/netns", 0700)
	if err != nil {
		fmt.Println("MkdirAll of /var/run/netns failed:", err)
		return err
	}

	// [ -f /var/run/netns/$PID ] && rm -f /var/run/netns/$PID
	netnsPid := path.Join("/var", "run", "netns", strconv.Itoa(containerPid))
	err = os.RemoveAll(netnsPid)
	if err != nil {
		fmt.Println("RemoveAll of /var/run/netns/$PID failed", err)
		return err
	}

	// mkdir -p /sys
	err = os.MkdirAll("/sys", 0700)
	if err != nil {
		fmt.Println("MkdirAll /sys failed:", err)
		return err
	}

	// mount -n -t tmpfs tmpfs /sys  # otherwise netns exec fails
	// FIXME: replace with library call
	err = exec.Command("mount", "-n", "-t", "tmpfs", "tmpfs", "/sys").Run()
	if err != nil {
		fmt.Println("mount -n -t tmpfs tmpfs /sys failed:", err)
		return err
	}

	// umount /sys
	defer func() {
		if err := exec.Command("umount", "/sys").Run(); err != nil {
			fmt.Println("Failed to unmount /sys:", err)
		}
	}()

	// ln -s /proc/$PID/ns/net /var/run/netns/$PID
	procNetnsPid := path.Join("/proc", strconv.Itoa(containerPid), "ns", "net")
	err = exec.Command("ln", "-s", procNetnsPid, netnsPid).Run()
	if err != nil {
		fmt.Printf("ln -s %s %s failed: %s\n", procNetnsPid, netnsPid, err)
		return err
	}

	// ip netns exec $PID ./bin/net-fence -target=container \
	//                 -containerIfcName=$network_container_iface \
	//                 -containerIP=$network_container_ip \
	//                 -gatewayIP=$network_host_ip \
	//                 -subnet=$network_cidr \
	//                 -mtu=$container_iface_mtu
	netFencePath := path.Join(a.fence.binPath, "net-fence")
	cmd := exec.Command("ip", "netns", "exec", strconv.Itoa(containerPid), netFencePath,
		"-target=container",
		"-containerIfcName="+a.containerIfcName,
		"-containerIP="+a.containerIP.String(),
		"-gatewayIP="+subnets.GatewayIP(a.IPNet).String(),
		"-subnet="+a.IPNet.String(),
		"-mtu="+strconv.Itoa(int(a.fence.mtu)))
	op, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("ip netns exec of %s failed: %s\nOutput:\n%s\n", netFencePath, err, string(op))
		return err
	}

	return nil
}

func (a *Allocation) Dismantle() error {
	released, err := a.fence.Release(a.IPNet, a.containerIP)
	if released {
		deconfigureHost(a.hostIfcName, a.bridgeIfcName)
	} else {
		deconfigureHost(a.hostIfcName, "")
	}
	return err
}

func (a *Allocation) Info(i *api.ContainerInfo) {
	i.HostIP = subnets.GatewayIP(a.IPNet).String()
	i.ContainerIP = a.containerIP.String()
}

func (a *Allocation) MarshalJSON() ([]byte, error) {
	ff := FlatFence{a.IPNet.String(), a.containerIP.String(), a.containerIfcName, a.hostIfcName, a.subnetShareable, a.bridgeIfcName}
	return json.Marshal(ff)
}

func (a *Allocation) ConfigureProcess(env *[]string) error {
	suff, _ := a.IPNet.Mask.Size()

	*env = append(*env, fmt.Sprintf("network_host_ip=%s", subnets.GatewayIP(a.IPNet)),
		fmt.Sprintf("network_container_ip=%s", a.containerIP),
		fmt.Sprintf("network_cidr_suffix=%d", suff),
		fmt.Sprintf("container_iface_mtu=%d", a.fence.mtu),
		fmt.Sprintf("subnet_shareable=%v", a.subnetShareable),
		fmt.Sprintf("network_cidr=%s", a.IPNet.String()),
		fmt.Sprintf("external_ip=%s", a.fence.externalIP.String()),
		fmt.Sprintf("network_ip_hex=%s", hexIP(a.IPNet.IP))) // suitable for short bridge interface names

	return nil
}

func hexIP(ip net.IP) string {
	return hex.EncodeToString(ip)
}
