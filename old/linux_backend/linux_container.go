package linux_backend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudfoundry-incubator/garden-linux/old/linux_backend/bandwidth_manager"
	"github.com/cloudfoundry-incubator/garden-linux/old/linux_backend/cgroups_manager"
	"github.com/cloudfoundry-incubator/garden-linux/old/linux_backend/process_tracker"
	"github.com/cloudfoundry-incubator/garden-linux/old/linux_backend/quota_manager"
	"github.com/cloudfoundry-incubator/garden-linux/old/logging"
	"github.com/cloudfoundry-incubator/garden/api"
	"github.com/cloudfoundry/gunk/command_runner"
	"github.com/pivotal-golang/lager"
)

type UndefinedPropertyError struct {
	Key string
}

func (err UndefinedPropertyError) Error() string {
	return fmt.Sprintf("property does not exist: %s", err.Key)
}

type LinuxContainer struct {
	logger lager.Logger

	id     string
	handle string
	path   string

	properties      api.Properties
	propertiesMutex sync.RWMutex

	graceTime time.Duration

	state      State
	stateMutex sync.RWMutex

	events      []string
	eventsMutex sync.RWMutex

	resources *Resources

	portPool PortPool

	runner command_runner.CommandRunner

	cgroupsManager   cgroups_manager.CgroupsManager
	quotaManager     quota_manager.QuotaManager
	bandwidthManager bandwidth_manager.BandwidthManager

	processTracker process_tracker.ProcessTracker

	oomMutex    sync.RWMutex
	oomNotifier *exec.Cmd

	currentBandwidthLimits *api.BandwidthLimits
	bandwidthMutex         sync.RWMutex

	currentDiskLimits *api.DiskLimits
	diskMutex         sync.RWMutex

	currentMemoryLimits *api.MemoryLimits
	memoryMutex         sync.RWMutex

	currentCPULimits *api.CPULimits
	cpuMutex         sync.RWMutex

	netIns      []NetInSpec
	netInsMutex sync.RWMutex

	netOuts      []NetOutSpec
	netOutsMutex sync.RWMutex

	mtu uint32

	envvars []string
}

type NetInSpec struct {
	HostPort      uint32
	ContainerPort uint32
}

type NetOutSpec struct {
	Network string
	Port    uint32
}

type PortPool interface {
	Acquire() (uint32, error)
	Remove(uint32) error
	Release(uint32)
}

type State string

const (
	StateBorn    = State("born")
	StateActive  = State("active")
	StateStopped = State("stopped")
)

func NewLinuxContainer(
	logger lager.Logger,
	id, handle, path string,
	properties api.Properties,
	graceTime time.Duration,
	resources *Resources,
	portPool PortPool,
	runner command_runner.CommandRunner,
	cgroupsManager cgroups_manager.CgroupsManager,
	quotaManager quota_manager.QuotaManager,
	bandwidthManager bandwidth_manager.BandwidthManager,
	processTracker process_tracker.ProcessTracker,
	envvars []string,
) *LinuxContainer {
	return &LinuxContainer{
		logger: logger,

		id:     id,
		handle: handle,
		path:   path,

		properties: properties,

		graceTime: graceTime,

		state:  StateBorn,
		events: []string{},

		resources: resources,

		portPool: portPool,

		runner: runner,

		cgroupsManager:   cgroupsManager,
		quotaManager:     quotaManager,
		bandwidthManager: bandwidthManager,

		processTracker: processTracker,

		envvars: envvars,
	}
}

func (c *LinuxContainer) ID() string {
	return c.id
}

func (c *LinuxContainer) Handle() string {
	return c.handle
}

func (c *LinuxContainer) GraceTime() time.Duration {
	return c.graceTime
}

func (c *LinuxContainer) Properties() api.Properties {
	return c.properties
}

func (c *LinuxContainer) State() State {
	c.stateMutex.RLock()
	defer c.stateMutex.RUnlock()

	return c.state
}

func (c *LinuxContainer) Events() []string {
	c.eventsMutex.RLock()
	defer c.eventsMutex.RUnlock()

	events := make([]string, len(c.events))

	copy(events, c.events)

	return events
}

func (c *LinuxContainer) Resources() *Resources {
	return c.resources
}

func (c *LinuxContainer) Snapshot(out io.Writer) error {
	cLog := c.logger.Session("snapshot")

	cLog.Debug("saving")

	c.bandwidthMutex.RLock()
	defer c.bandwidthMutex.RUnlock()

	c.cpuMutex.RLock()
	defer c.cpuMutex.RUnlock()

	c.diskMutex.RLock()
	defer c.diskMutex.RUnlock()

	c.memoryMutex.RLock()
	defer c.memoryMutex.RUnlock()

	c.netInsMutex.RLock()
	defer c.netInsMutex.RUnlock()

	c.netOutsMutex.RLock()
	defer c.netOutsMutex.RUnlock()

	processSnapshots := []ProcessSnapshot{}

	for _, p := range c.processTracker.ActiveProcesses() {
		processSnapshots = append(
			processSnapshots,
			ProcessSnapshot{
				ID: p.ID(),
			},
		)
	}

	snapshot := ContainerSnapshot{
		ID:     c.id,
		Handle: c.handle,

		GraceTime: c.graceTime,

		State:  string(c.State()),
		Events: c.Events(),

		Limits: LimitsSnapshot{
			Bandwidth: c.currentBandwidthLimits,
			CPU:       c.currentCPULimits,
			Disk:      c.currentDiskLimits,
			Memory:    c.currentMemoryLimits,
		},

		Resources: ResourcesSnapshot{
			UserUID: c.resources.UserUID,
			RootUID: c.resources.RootUID,
			Ports:   c.resources.Ports,
		},

		NetIns:  c.netIns,
		NetOuts: c.netOuts,

		Processes: processSnapshots,

		Properties: c.Properties(),

		EnvVars: c.envvars,
	}

	var err error
	m, err := c.resources.Network.MarshalJSON()
	if err != nil {
		cLog.Error("failed-to-save", err, lager.Data{
			"snapshot": snapshot,
			"network":  c.resources.Network,
		})
		return err
	}

	var rm json.RawMessage = m
	snapshot.Resources.Network = &rm

	err = json.NewEncoder(out).Encode(snapshot)
	if err != nil {
		cLog.Error("failed-to-save", err, lager.Data{
			"snapshot": snapshot,
		})
		return err
	}

	cLog.Info("saved", lager.Data{
		"snapshot": snapshot,
	})

	return nil
}

func (c *LinuxContainer) Restore(snapshot ContainerSnapshot) error {
	cLog := c.logger.Session("restore")

	cLog.Debug("restoring")

	cRunner := logging.Runner{
		CommandRunner: c.runner,
		Logger:        cLog,
	}

	c.setState(State(snapshot.State))

	c.envvars = snapshot.EnvVars

	for _, ev := range snapshot.Events {
		c.registerEvent(ev)
	}

	if snapshot.Limits.Memory != nil {
		err := c.LimitMemory(*snapshot.Limits.Memory)
		if err != nil {
			cLog.Error("failed-to-limit-memory", err)
			return err
		}
	}

	for _, process := range snapshot.Processes {
		cLog.Info("restoring-process", lager.Data{
			"process": process,
		})

		c.processTracker.Restore(process.ID)
	}

	net := exec.Command(path.Join(c.path, "net.sh"), "setup")

	err := cRunner.Run(net)
	if err != nil {
		cLog.Error("failed-to-reenforce-network-rules", err)
		return err
	}

	for _, in := range snapshot.NetIns {
		_, _, err = c.NetIn(in.HostPort, in.ContainerPort)
		if err != nil {
			cLog.Error("failed-to-reenforce-port-mapping", err)
			return err
		}
	}

	for _, out := range snapshot.NetOuts {
		err = c.NetOut(out.Network, out.Port)
		if err != nil {
			cLog.Error("failed-to-reenforce-allowed-traffic", err)
			return err
		}
	}

	cLog.Info("restored")

	return nil
}

func (c *LinuxContainer) Start() error {
	cLog := c.logger.Session("start")

	cLog.Debug("starting")

	start := exec.Command(path.Join(c.path, "start.sh"))
	start.Env = []string{
		"id=" + c.id,
		"PATH=" + os.Getenv("PATH"),
	}

	cRunner := logging.Runner{
		CommandRunner: c.runner,
		Logger:        cLog,
	}

	err := cRunner.Run(start)
	if err != nil {
		cLog.Error("failed-to-start", err)
		return err
	}

	containerPid, err := c.wshdPid()
	if err != nil {
		cLog.Error("failed-to-get-wshd-pid", err)
		return err
	}

	err = c.resources.Network.Erect(containerPid)
	if err != nil {
		cLog.Error("failed-to-erect-network-fence", err)
		return err
	}

	c.setState(StateActive)

	cLog.Info("started")

	return nil
}

func (c *LinuxContainer) Cleanup() {
	cLog := c.logger.Session("cleanup")

	cLog.Debug("stopping-oom-notifier")
	c.stopOomNotifier()

	cLog.Info("done")
}

func (c *LinuxContainer) Stop(kill bool) error {
	stop := exec.Command(path.Join(c.path, "stop.sh"))

	if kill {
		stop.Args = append(stop.Args, "-w", "0")
	}

	err := c.runner.Run(stop)
	if err != nil {
		return err
	}

	c.stopOomNotifier()

	c.setState(StateStopped)

	return nil
}

func (c *LinuxContainer) GetProperty(key string) (string, error) {
	c.propertiesMutex.RLock()
	defer c.propertiesMutex.RUnlock()

	value, found := c.properties[key]
	if !found {
		return "", UndefinedPropertyError{key}
	}

	return value, nil
}

func (c *LinuxContainer) SetProperty(key string, value string) error {
	c.propertiesMutex.Lock()
	defer c.propertiesMutex.Unlock()

	c.properties[key] = value

	return nil
}

func (c *LinuxContainer) RemoveProperty(key string) error {
	c.propertiesMutex.Lock()
	defer c.propertiesMutex.Unlock()

	_, found := c.properties[key]
	if !found {
		return UndefinedPropertyError{key}
	}

	delete(c.properties, key)

	return nil
}

func (c *LinuxContainer) Info() (api.ContainerInfo, error) {
	cLog := c.logger.Session("info")

	memoryStat, err := c.cgroupsManager.Get("memory", "memory.stat")
	if err != nil {
		return api.ContainerInfo{}, err
	}

	cpuUsage, err := c.cgroupsManager.Get("cpuacct", "cpuacct.usage")
	if err != nil {
		return api.ContainerInfo{}, err
	}

	cpuStat, err := c.cgroupsManager.Get("cpuacct", "cpuacct.stat")
	if err != nil {
		return api.ContainerInfo{}, err
	}

	diskStat, err := c.quotaManager.GetUsage(cLog, c.resources.UserUID)
	if err != nil {
		return api.ContainerInfo{}, err
	}

	bandwidthStat, err := c.bandwidthManager.GetLimits(cLog)
	if err != nil {
		return api.ContainerInfo{}, err
	}

	mappedPorts := []api.PortMapping{}

	c.netInsMutex.RLock()

	for _, spec := range c.netIns {
		mappedPorts = append(mappedPorts, api.PortMapping{
			HostPort:      spec.HostPort,
			ContainerPort: spec.ContainerPort,
		})
	}

	c.netInsMutex.RUnlock()

	processIDs := []uint32{}
	for _, process := range c.processTracker.ActiveProcesses() {
		processIDs = append(processIDs, process.ID())
	}

	info := api.ContainerInfo{
		State:         string(c.State()),
		Events:        c.Events(),
		Properties:    c.Properties(),
		ContainerPath: c.path,
		ProcessIDs:    processIDs,
		MemoryStat:    parseMemoryStat(memoryStat),
		CPUStat:       parseCPUStat(cpuUsage, cpuStat),
		DiskStat:      diskStat,
		BandwidthStat: bandwidthStat,
		MappedPorts:   mappedPorts,
	}

	c.Resources().Network.Info(&info)
	return info, nil
}

func (c *LinuxContainer) wshdPid() (int, error) {
	pidPath := path.Join(c.path, "run", "wshd.pid")

	pidFile, err := os.Open(pidPath)
	if err != nil {
		return 0, err
	}
	defer pidFile.Close()

	var pid int
	_, err = fmt.Fscanf(pidFile, "%d", &pid)
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func (c *LinuxContainer) StreamIn(dstPath string, tarStream io.Reader) error {
	pid, err := c.wshdPid()
	if err != nil {
		return err
	}

	nsTarPath := path.Join(c.path, "bin", "nstar")
	tar := exec.Command(
		nsTarPath,
		strconv.Itoa(pid),
		"vcap",
		dstPath,
	)

	tar.Stdin = tarStream

	cLog := c.logger.Session("stream-in")

	cRunner := logging.Runner{
		CommandRunner: c.runner,
		Logger:        cLog,
	}

	return cRunner.Run(tar)
}

func (c *LinuxContainer) StreamOut(srcPath string) (io.ReadCloser, error) {
	workingDir := filepath.Dir(srcPath)
	compressArg := filepath.Base(srcPath)
	if strings.HasSuffix(srcPath, "/") {
		workingDir = srcPath
		compressArg = "."
	}

	pid, err := c.wshdPid()
	if err != nil {
		return nil, err
	}

	nsTarPath := path.Join(c.path, "bin", "nstar")
	tar := exec.Command(
		nsTarPath,
		strconv.Itoa(pid),
		"vcap",
		workingDir,
		compressArg,
	)

	tarRead, tarWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	tar.Stdout = tarWrite

	err = c.runner.Background(tar)
	if err != nil {
		return nil, err
	}

	// close our end of the tar pipe
	tarWrite.Close()

	go c.runner.Wait(tar)

	return tarRead, nil
}

func (c *LinuxContainer) LimitBandwidth(limits api.BandwidthLimits) error {
	cLog := c.logger.Session("limit-bandwidth")

	err := c.bandwidthManager.SetLimits(cLog, limits)
	if err != nil {
		return err
	}

	c.bandwidthMutex.Lock()
	defer c.bandwidthMutex.Unlock()

	c.currentBandwidthLimits = &limits

	return nil
}

func (c *LinuxContainer) CurrentBandwidthLimits() (api.BandwidthLimits, error) {
	c.bandwidthMutex.RLock()
	defer c.bandwidthMutex.RUnlock()

	if c.currentBandwidthLimits == nil {
		return api.BandwidthLimits{}, nil
	}

	return *c.currentBandwidthLimits, nil
}

func (c *LinuxContainer) LimitDisk(limits api.DiskLimits) error {
	cLog := c.logger.Session("limit-disk")

	err := c.quotaManager.SetLimits(cLog, c.resources.UserUID, limits)
	if err != nil {
		return err
	}

	c.diskMutex.Lock()
	defer c.diskMutex.Unlock()

	c.currentDiskLimits = &limits

	return nil
}

func (c *LinuxContainer) CurrentDiskLimits() (api.DiskLimits, error) {
	cLog := c.logger.Session("current-disk-limits")
	return c.quotaManager.GetLimits(cLog, c.resources.UserUID)
}

func (c *LinuxContainer) LimitMemory(limits api.MemoryLimits) error {
	err := c.startOomNotifier()
	if err != nil {
		return err
	}

	limit := fmt.Sprintf("%d", limits.LimitInBytes)

	// memory.memsw.limit_in_bytes must be >= memory.limit_in_bytes
	//
	// however, it must be set after memory.limit_in_bytes, and if we're
	// increasing the limit, writing memory.limit_in_bytes first will fail.
	//
	// so, write memory.limit_in_bytes before and after
	c.cgroupsManager.Set("memory", "memory.limit_in_bytes", limit)
	c.cgroupsManager.Set("memory", "memory.memsw.limit_in_bytes", limit)

	err = c.cgroupsManager.Set("memory", "memory.limit_in_bytes", limit)
	if err != nil {
		return err
	}

	c.memoryMutex.Lock()
	defer c.memoryMutex.Unlock()

	c.currentMemoryLimits = &limits

	return nil
}

func (c *LinuxContainer) CurrentMemoryLimits() (api.MemoryLimits, error) {
	limitInBytes, err := c.cgroupsManager.Get("memory", "memory.limit_in_bytes")
	if err != nil {
		return api.MemoryLimits{}, err
	}

	numericLimit, err := strconv.ParseUint(limitInBytes, 10, 0)
	if err != nil {
		return api.MemoryLimits{}, err
	}

	return api.MemoryLimits{uint64(numericLimit)}, nil
}

func (c *LinuxContainer) LimitCPU(limits api.CPULimits) error {
	limit := fmt.Sprintf("%d", limits.LimitInShares)

	err := c.cgroupsManager.Set("cpu", "cpu.shares", limit)
	if err != nil {
		return err
	}

	c.cpuMutex.Lock()
	defer c.cpuMutex.Unlock()

	c.currentCPULimits = &limits

	return nil
}

func (c *LinuxContainer) CurrentCPULimits() (api.CPULimits, error) {
	actualLimitInShares, err := c.cgroupsManager.Get("cpu", "cpu.shares")
	if err != nil {
		return api.CPULimits{}, err
	}

	numericLimit, err := strconv.ParseUint(actualLimitInShares, 10, 0)
	if err != nil {
		return api.CPULimits{}, err
	}

	return api.CPULimits{uint64(numericLimit)}, nil
}

func (c *LinuxContainer) Run(spec api.ProcessSpec, processIO api.ProcessIO) (api.Process, error) {
	wshPath := path.Join(c.path, "bin", "wsh")
	sockPath := path.Join(c.path, "run", "wshd.sock")

	user := "vcap"
	if spec.Privileged {
		user = "root"
	}

	if spec.User != "" {
		user = spec.User
	}

	args := []string{"--socket", sockPath, "--user", user}

	envVars := []string{}
	envVars = append(append(envVars, c.envvars...), spec.Env...)
	envVars = c.dedup(envVars)

	for _, envVar := range envVars {
		args = append(args, "--env", envVar)
	}

	if spec.Dir != "" {
		args = append(args, "--dir", spec.Dir)
	}

	args = append(args, spec.Path)

	wsh := exec.Command(wshPath, append(args, spec.Args...)...)

	setRLimitsEnv(wsh, spec.Limits)

	return c.processTracker.Run(wsh, processIO, spec.TTY)
}

func (c *LinuxContainer) Attach(processID uint32, processIO api.ProcessIO) (api.Process, error) {
	return c.processTracker.Attach(processID, processIO)
}

func (c *LinuxContainer) NetIn(hostPort uint32, containerPort uint32) (uint32, uint32, error) {
	if hostPort == 0 {
		randomPort, err := c.portPool.Acquire()
		if err != nil {
			return 0, 0, err
		}

		c.resources.AddPort(randomPort)

		hostPort = randomPort
	}

	if containerPort == 0 {
		containerPort = hostPort
	}

	net := exec.Command(path.Join(c.path, "net.sh"), "in")
	net.Env = []string{
		fmt.Sprintf("HOST_PORT=%d", hostPort),
		fmt.Sprintf("CONTAINER_PORT=%d", containerPort),
		"PATH=" + os.Getenv("PATH"),
	}

	err := c.runner.Run(net)
	if err != nil {
		return 0, 0, err
	}

	c.netInsMutex.Lock()
	defer c.netInsMutex.Unlock()

	c.netIns = append(c.netIns, NetInSpec{hostPort, containerPort})

	return hostPort, containerPort, nil
}

func (c *LinuxContainer) NetOut(network string, port uint32) error {
	net := exec.Command(path.Join(c.path, "net.sh"), "out")

	if port != 0 {
		net.Env = []string{
			"NETWORK=" + network,
			fmt.Sprintf("PORT=%d", port),
			"PATH=" + os.Getenv("PATH"),
		}
	} else {
		if network == "" {
			return fmt.Errorf("network and/or port must be provided")
		}

		net.Env = []string{
			"NETWORK=" + network,
			"PORT=",
			"PATH=" + os.Getenv("PATH"),
		}
	}

	err := c.runner.Run(net)
	if err != nil {
		return err
	}

	c.netOutsMutex.Lock()
	defer c.netOutsMutex.Unlock()

	c.netOuts = append(c.netOuts, NetOutSpec{network, port})

	return nil
}

func (c *LinuxContainer) CurrentEnvVars() []string {
	return c.envvars
}

func (c *LinuxContainer) setState(state State) {
	c.stateMutex.Lock()
	defer c.stateMutex.Unlock()

	c.state = state
}

func (c *LinuxContainer) registerEvent(event string) {
	c.eventsMutex.Lock()
	defer c.eventsMutex.Unlock()

	c.events = append(c.events, event)
}

func (c *LinuxContainer) startOomNotifier() error {
	c.oomMutex.Lock()
	defer c.oomMutex.Unlock()

	if c.oomNotifier != nil {
		return nil
	}

	oomPath := path.Join(c.path, "bin", "oom")

	c.oomNotifier = exec.Command(oomPath, c.cgroupsManager.SubsystemPath("memory"))

	err := c.runner.Start(c.oomNotifier)
	if err != nil {
		return err
	}

	go c.watchForOom(c.oomNotifier)

	return nil
}

func (c *LinuxContainer) stopOomNotifier() {
	c.oomMutex.RLock()
	defer c.oomMutex.RUnlock()

	if c.oomNotifier != nil {
		c.runner.Kill(c.oomNotifier)
	}
}

func (c *LinuxContainer) watchForOom(oom *exec.Cmd) {
	err := c.runner.Wait(oom)
	if err == nil {
		c.registerEvent("out of memory")
		c.Stop(false)
	}

	// TODO: handle case where oom notifier itself failed? kill container?
}

func parseMemoryStat(contents string) (stat api.ContainerMemoryStat) {
	scanner := bufio.NewScanner(strings.NewReader(contents))

	scanner.Split(bufio.ScanWords)

	for scanner.Scan() {
		field := scanner.Text()

		if !scanner.Scan() {
			break
		}

		value, err := strconv.ParseUint(scanner.Text(), 10, 0)
		if err != nil {
			continue
		}

		switch field {
		case "cache":
			stat.Cache = value
		case "rss":
			stat.Rss = value
		case "mapped_file":
			stat.MappedFile = value
		case "pgpgin":
			stat.Pgpgin = value
		case "pgpgout":
			stat.Pgpgout = value
		case "swap":
			stat.Swap = value
		case "pgfault":
			stat.Pgfault = value
		case "pgmajfault":
			stat.Pgmajfault = value
		case "inactive_anon":
			stat.InactiveAnon = value
		case "active_anon":
			stat.ActiveAnon = value
		case "inactive_file":
			stat.InactiveFile = value
		case "active_file":
			stat.ActiveFile = value
		case "unevictable":
			stat.Unevictable = value
		case "hierarchical_memory_limit":
			stat.HierarchicalMemoryLimit = value
		case "hierarchical_memsw_limit":
			stat.HierarchicalMemswLimit = value
		case "total_cache":
			stat.TotalCache = value
		case "total_rss":
			stat.TotalRss = value
		case "total_mapped_file":
			stat.TotalMappedFile = value
		case "total_pgpgin":
			stat.TotalPgpgin = value
		case "total_pgpgout":
			stat.TotalPgpgout = value
		case "total_swap":
			stat.TotalSwap = value
		case "total_pgfault":
			stat.TotalPgfault = value
		case "total_pgmajfault":
			stat.TotalPgmajfault = value
		case "total_inactive_anon":
			stat.TotalInactiveAnon = value
		case "total_active_anon":
			stat.TotalActiveAnon = value
		case "total_inactive_file":
			stat.TotalInactiveFile = value
		case "total_active_file":
			stat.TotalActiveFile = value
		case "total_unevictable":
			stat.TotalUnevictable = value
		}
	}

	return
}

func parseCPUStat(usage, statContents string) (stat api.ContainerCPUStat) {
	cpuUsage, err := strconv.ParseUint(strings.Trim(usage, "\n"), 10, 0)
	if err != nil {
		return
	}

	stat.Usage = cpuUsage

	scanner := bufio.NewScanner(strings.NewReader(statContents))

	scanner.Split(bufio.ScanWords)

	for scanner.Scan() {
		field := scanner.Text()

		if !scanner.Scan() {
			break
		}

		value, err := strconv.ParseUint(scanner.Text(), 10, 0)
		if err != nil {
			continue
		}

		switch field {
		case "user":
			stat.User = value
		case "system":
			stat.System = value
		}
	}

	return
}

func setRLimitsEnv(cmd *exec.Cmd, rlimits api.ResourceLimits) {
	if rlimits.As != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_AS=%d", *rlimits.As))
	}

	if rlimits.Core != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_CORE=%d", *rlimits.Core))
	}

	if rlimits.Cpu != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_CPU=%d", *rlimits.Cpu))
	}

	if rlimits.Data != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_DATA=%d", *rlimits.Data))
	}

	if rlimits.Fsize != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_FSIZE=%d", *rlimits.Fsize))
	}

	if rlimits.Locks != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_LOCKS=%d", *rlimits.Locks))
	}

	if rlimits.Memlock != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_MEMLOCK=%d", *rlimits.Memlock))
	}

	if rlimits.Msgqueue != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_MSGQUEUE=%d", *rlimits.Msgqueue))
	}

	if rlimits.Nice != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_NICE=%d", *rlimits.Nice))
	}

	if rlimits.Nofile != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_NOFILE=%d", *rlimits.Nofile))
	}

	if rlimits.Nproc != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_NPROC=%d", *rlimits.Nproc))
	}

	if rlimits.Rss != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_RSS=%d", *rlimits.Rss))
	}

	if rlimits.Rtprio != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_RTPRIO=%d", *rlimits.Rtprio))
	}

	if rlimits.Sigpending != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_SIGPENDING=%d", *rlimits.Sigpending))
	}

	if rlimits.Stack != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RLIMIT_STACK=%d", *rlimits.Stack))
	}
}

func (c *LinuxContainer) dedup(envVars []string) []string {
	seenArgs := map[string]string{}
	result := []string{}
	for i := len(envVars) - 1; i >= 0; i-- {
		envVar := envVars[i]
		keyValue := strings.SplitN(envVar, "=", 2)
		_, containsKey := seenArgs[keyValue[0]]
		if len(keyValue) == 2 && !containsKey {
			result = append([]string{envVar}, result...)
			seenArgs[keyValue[0]] = envVar
		}
	}
	return (result)
}
