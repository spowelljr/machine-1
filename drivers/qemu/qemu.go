package qemu

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/ssh"
	"github.com/docker/machine/libmachine/state"
)

const (
	isoFilename        = "boot2docker.iso"
	privateNetworkName = "docker-machines"

	defaultSSHUser = "docker"
)

type Driver struct {
	*drivers.BaseDriver
	EnginePort       int
	FirstQuery       bool

	Memory           int
	DiskSize         int
	CPU              int
	Program          string
	Network          string
	PrivateNetwork   string
	Boot2DockerURL   string
	NetworkBridge    string
	CaCertPath       string
	PrivateKeyPath   string
	DiskPath         string
	CacheMode        string
	IOMode           string
	connectionString string
	//	conn             *libvirt.Connect
	//	VM               *libvirt.Domain
	vmLoaded        bool
	UserDataFile    string
	CloudConfigRoot string
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.IntFlag{
			Name:  "qemu-memory",
			Usage: "Size of memory for host in MB",
			Value: 1024,
		},
		mcnflag.IntFlag{
			Name:  "qemu-disk-size",
			Usage: "Size of disk for host in MB",
			Value: 20000,
		},
		mcnflag.IntFlag{
			Name:  "qemu-cpu-count",
			Usage: "Number of CPUs",
			Value: 1,
		},
		mcnflag.StringFlag{
			Name:  "qemu-program",
			Usage: "Name of program to run",
			Value: "qemu-system-x86_64",
		},
		// TODO - support for multiple networks
		mcnflag.StringFlag{
			Name:  "qemu-network",
			Usage: "Name of network to connect to",
			Value: "default",
		},
		mcnflag.StringFlag{
			EnvVar: "QEMU_BOOT2DOCKER_URL",
			Name:   "qemu-boot2docker-url",
			Usage:  "The URL of the boot2docker image. Defaults to the latest available version",
			Value:  "",
		},
		mcnflag.StringFlag{
			Name:  "qemu-network-bridge",
			Usage: "Name of the virtual bridge to be used for networking (currently unused)",
			Value: "virbr0",
		},
		mcnflag.StringFlag{
			Name:  "qemu-cache-mode",
			Usage: "Disk cache mode: default, none, writethrough, writeback, directsync, or unsafe",
			Value: "default",
		},
		mcnflag.StringFlag{
			Name:  "qemu-io-mode",
			Usage: "Disk IO mode: threads, native",
			Value: "threads",
		},
		mcnflag.StringFlag{
			EnvVar: "QEMU_SSH_USER",
			Name:   "qemu-ssh-user",
			Usage:  "SSH username",
			Value:  defaultSSHUser,
		},
		mcnflag.StringFlag{
			Name:  "qemu-userdata",
			Usage: "cloud-config userdata file",
		},
		/* Not yet implemented
		mcnflag.Flag{
			Name:  "qemu-no-share",
			Usage: "Disable the mount of your home directory",
		},
		*/
	}
}

func (d *Driver) GetMachineName() string {
	return d.MachineName
}

func (d *Driver) GetSSHHostname() (string, error) {
	return "localhost", nil
	//return d.GetIP()
}

func (d *Driver) GetSSHKeyPath() string {
	return d.ResolveStorePath("id_rsa")
}

func (d *Driver) GetSSHPort() (int, error) {
	if d.SSHPort == 0 {
		d.SSHPort = 22
	}

	return d.SSHPort, nil
}

func (d *Driver) GetEnginePort() (int, error) {
	if d.EnginePort == 0 {
		d.EnginePort = 2376
	}

	return d.EnginePort, nil
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = "docker"
	}

	return d.SSHUser
}

func (d *Driver) DriverName() string {
	return "qemu"
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	log.Debugf("SetConfigFromFlags called")
	d.Memory = flags.Int("qemu-memory")
	d.DiskSize = flags.Int("qemu-disk-size")
	d.CPU = flags.Int("qemu-cpu-count")
	d.Program = flags.String("qemu-program")
	d.Network = flags.String("qemu-network")
	d.Boot2DockerURL = flags.String("qemu-boot2docker-url")
	d.NetworkBridge = flags.String("qemu-network-bridge")
	d.CacheMode = flags.String("qemu-cache-mode")
	d.IOMode = flags.String("qemu-io-mode")

	d.SwarmMaster = flags.Bool("swarm-master")
	d.SwarmHost = flags.String("swarm-host")
	d.SwarmDiscovery = flags.String("swarm-discovery")
	d.SSHUser = flags.String("qemu-ssh-user")
	d.UserDataFile = flags.String("qemu-userdata")
	d.EnginePort = 2376
	d.FirstQuery = true
	d.SSHPort = 22
	d.DiskPath = d.ResolveStorePath(fmt.Sprintf("%s.img", d.MachineName))
	return nil
}

func (d *Driver) GetURL() (string, error) {
	log.Debugf("GetURL called")
	ip, err := d.GetIP()
	if err != nil {
		log.Warnf("Failed to get IP: %s", err)
		return "", err
	}
	if ip == "" {
		return "", nil
	}
	port, err := d.GetEnginePort()
	if (d.FirstQuery) {
		d.FirstQuery = false
		port = 2376
	}
	return fmt.Sprintf("tcp://%s:%d", ip, port), nil
}

func NewDriver(hostName, storePath string) drivers.Driver {
	return &Driver{
		PrivateNetwork: privateNetworkName,
		BaseDriver: &drivers.BaseDriver{
			SSHUser:     defaultSSHUser,
			MachineName: hostName,
			StorePath:   storePath,
		},
	}
}

func (d *Driver) GetIP() (string, error) {
	return "127.0.0.1", nil
}

func (d *Driver) GetPort() (int, error) {
	return d.EnginePort, nil
}

func (d *Driver) GetState() (state.State, error) {

	ret, err := d.RunQMPCommand("query-status")
	if err != nil {
		return state.Error, err
	}
	// RunState is one of:
	// 'debug', 'inmigrate', 'internal-error', 'io-error', 'paused',
	// 'postmigrate', 'prelaunch', 'finish-migrate', 'restore-vm',
	// 'running', 'save-vm', 'shutdown', 'suspended', 'watchdog',
	// 'guest-panicked'
	switch ret["status"] {
	case "running":
		return state.Running, nil
	case "paused":
		return state.Paused, nil
	case "shutdown":
		return state.Stopped, nil
	}
	return state.None, nil
}

func (d *Driver) PreCreateCheck() error {
	return nil
}

func (d *Driver) Create() error {
	var err error
	d.SSHPort, err = getAvailableTCPPort()
	if err != nil {
		return err
	}
	d.EnginePort, err = getAvailableTCPPort()
	if err != nil {
		return err
	}
	b2dutils := mcnutils.NewB2dUtils(d.StorePath)
	if err := b2dutils.CopyIsoToMachineDir(d.Boot2DockerURL, d.MachineName); err != nil {
		return err
	}

	log.Infof("Creating SSH key...")
	if err := ssh.GenerateSSHKey(d.sshKeyPath()); err != nil {
		return err
	}

	log.Infof("Creating Disk image...")
	if err := d.generateDiskImage(d.DiskSize); err != nil {
		return err
	}

	if d.UserDataFile != "" {
		log.Infof("Creating Userdata Disk...")
		if d.CloudConfigRoot, err = d.generateUserdataDisk(d.UserDataFile); err != nil {
			return err
		}
	}

	log.Infof("Starting QEMU VM...")
	if err := d.Start(); err != nil {
		return err
	}
	return nil
}

func getAvailableTCPPort() (int, error) {
	port := 0
	for i := 0; i <= 10; i++ {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		defer ln.Close()
		addr := ln.Addr().String()
		addrParts := strings.SplitN(addr, ":", 2)
		p, err := strconv.Atoi(addrParts[1])
		if err != nil {
			return 0, err
		}
		if p != 0 {
			port = p
			return port, nil
		}
		time.Sleep(1)
	}
	return 0, fmt.Errorf("unable to allocate tcp port")
}

func (d *Driver) Start() error {
	// fmt.Printf("Init qemu %s\n", i.VM)
	machineDir := filepath.Join(d.StorePath, "machines", d.GetMachineName())

	startCmd := []string{
		"-display", "none",
		"-m", fmt.Sprintf("%d", d.Memory),
		"-smp", fmt.Sprintf("%d", d.CPU),
		"-boot", "d",
		"-cdrom", filepath.Join(machineDir, "boot2docker.iso"),
		"-qmp", fmt.Sprintf("unix:%s,server,nowait", d.monitorPath()),
	}

	startCmd = append(startCmd,
		//		"-netdev", "user,id=network0",
		//		"-device", "virtio-net,netdev=network0",
		//		"-netdev", fmt.Sprintf("bridge,id=network1,br=%s", d.NetworkBridge),
		//		"-redir", fmt.Sprintf("tcp:%d::22", d.SSHPort),
		//		"-device", "virtio-net,netdev=network1",
		"-net", "nic,vlan=0,model=virtio",
		"-net", fmt.Sprintf("user,vlan=0,hostfwd=tcp::%d-:22,hostfwd=tcp::%d-:2376,hostname=%s", d.SSHPort, d.EnginePort, d.GetMachineName()),
	)

	startCmd = append(startCmd, "-daemonize")

	// other options
	// "-enable-kvm" if its available
	if _, err := os.Stat("/dev/kvm"); err == nil {
		startCmd = append(startCmd, "-enable-kvm")
	}

	if d.CloudConfigRoot != "" {
		startCmd = append(startCmd,
			"-fsdev",
			fmt.Sprintf("local,security_model=passthrough,readonly,id=fsdev0,path=%s", d.CloudConfigRoot))
		startCmd = append(startCmd, "-device", "virtio-9p-pci,id=fs0,fsdev=fsdev0,mount_tag=config-2")
	}

	// last argument is always the name of the disk image
	startCmd = append(startCmd, d.diskPath())

	if stdout, stderr, err := cmdOutErr(d.Program, startCmd...); err != nil {
		fmt.Printf("OUTPUT: %s\n", stdout)
		fmt.Printf("ERROR: %s\n", stderr)
		return err
		//if err := cmdStart(d.Program, startCmd...); err != nil {
		//	return err
	}
	log.Infof("Waiting for VM to start (ssh -p %d docker@localhost)...", d.SSHPort)

	//return ssh.WaitForTCP(fmt.Sprintf("localhost:%d", d.SSHPort))
	return WaitForTCPWithDelay(fmt.Sprintf("localhost:%d", d.SSHPort), time.Second)
}

func cmdOutErr(cmdStr string, args ...string) (string, string, error) {
	cmd := exec.Command(cmdStr, args...)
	log.Debugf("executing: %v %v", cmdStr, strings.Join(args, " "))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	stderrStr := stderr.String()
	log.Debugf("STDOUT: %v", stdout.String())
	log.Debugf("STDERR: %v", stderrStr)
	if err != nil {
		if ee, ok := err.(*exec.Error); ok && ee == exec.ErrNotFound {
			err = fmt.Errorf("mystery error: %s", ee)
		}
	} else {
		// also catch error messages in stderr, even if the return code
		// looks OK
		if strings.Contains(stderrStr, "error:") {
			err = fmt.Errorf("%v %v failed: %v", cmdStr, strings.Join(args, " "), stderrStr)
		}
	}
	return stdout.String(), stderrStr, err
}

func cmdStart(cmdStr string, args ...string) error {
	cmd := exec.Command(cmdStr, args...)
	log.Debugf("executing: %v %v", cmdStr, strings.Join(args, " "))
	return cmd.Start()
}

func (d *Driver) Stop() error {
	// _, err := d.RunQMPCommand("stop")
	_, err := d.RunQMPCommand("system_powerdown")
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) Remove() error {
	s, err := d.GetState()
	if err != nil {
		return err
	}
	if s == state.Running {
		if err := d.Kill(); err != nil {
			return err
		}
	}
	_, err = d.RunQMPCommand("quit")
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) Restart() error {
	s, err := d.GetState()
	if err != nil {
		return err
	}

	if s == state.Running {
		if err := d.Stop(); err != nil {
			return err
		}
	}
	return d.Start()
}

func (d *Driver) Kill() error {
	// _, err := d.RunQMPCommand("quit")
	_, err := d.RunQMPCommand("system_powerdown")
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) StartDocker() error {
	return fmt.Errorf("hosts without a driver cannot start docker")
}

func (d *Driver) StopDocker() error {
	return fmt.Errorf("hosts without a driver cannot stop docker")
}

func (d *Driver) GetDockerConfigDir() string {
	return ""
}

func (d *Driver) Upgrade() error {
	return fmt.Errorf("hosts without a driver cannot be upgraded")
}

//func (d *Driver) GetSSHCommand(args ...string) (*exec.Cmd, error) {
//	return ssh.GetSSHCommand("localhost", d.SSHPort, "docker", d.sshKeyPath(), args...), nil
//}

func (d *Driver) sshKeyPath() string {
	machineDir := filepath.Join(d.StorePath, "machines", d.GetMachineName())
	return filepath.Join(machineDir, "id_rsa")
}

func (d *Driver) publicSSHKeyPath() string {
	return d.sshKeyPath() + ".pub"
}

func (d *Driver) diskPath() string {
	machineDir := filepath.Join(d.StorePath, "machines", d.GetMachineName())
	return filepath.Join(machineDir, "disk.qcow2")
}

func (d *Driver) monitorPath() string {
	machineDir := filepath.Join(d.StorePath, "machines", d.GetMachineName())
	return filepath.Join(machineDir, "monitor")
}

// Make a boot2docker VM disk image.
func (d *Driver) generateDiskImage(size int) error {
	log.Debugf("Creating %d MB hard disk image...", size)

	magicString := "boot2docker, please format-me"

	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	// magicString first so the automount script knows to format the disk
	file := &tar.Header{Name: magicString, Size: int64(len(magicString))}
	if err := tw.WriteHeader(file); err != nil {
		return err
	}
	log.Infof("1")
	if _, err := tw.Write([]byte(magicString)); err != nil {
		return err
	}
	// .ssh/key.pub => authorized_keys
	log.Infof("2")
	file = &tar.Header{Name: ".ssh", Typeflag: tar.TypeDir, Mode: 0700}
	if err := tw.WriteHeader(file); err != nil {
		return err
	}
	log.Infof("3")
	pubKey, err := ioutil.ReadFile(d.publicSSHKeyPath())
	if err != nil {
		return err
	}
	log.Infof("4")
	file = &tar.Header{Name: ".ssh/authorized_keys", Size: int64(len(pubKey)), Mode: 0644}
	if err := tw.WriteHeader(file); err != nil {
		return err
	}
	log.Infof("5")
	if _, err := tw.Write([]byte(pubKey)); err != nil {
		return err
	}
	log.Infof("6")
	file = &tar.Header{Name: ".ssh/authorized_keys2", Size: int64(len(pubKey)), Mode: 0644}
	if err := tw.WriteHeader(file); err != nil {
		return err
	}
	log.Infof("7")
	if _, err := tw.Write([]byte(pubKey)); err != nil {
		return err
	}
	log.Infof("8")
	if err := tw.Close(); err != nil {
		return err
	}
	log.Infof("9")
	rawFile := fmt.Sprintf("%s.raw", d.diskPath())
	if err := ioutil.WriteFile(rawFile, buf.Bytes(), 0644); err != nil {
		return nil
	}
	log.Infof("10")
	if stdout, stderr, err := cmdOutErr("qemu-img", "convert", "-f", "raw", "-O", "qcow2", rawFile, d.diskPath()); err != nil {
		fmt.Printf("OUTPUT: %s\n", stdout)
		fmt.Printf("ERROR: %s\n", stderr)
		return err
	}
	log.Infof("11")
	if stdout, stderr, err := cmdOutErr("qemu-img", "resize", d.diskPath(), fmt.Sprintf("+%dMB", size)); err != nil {
		fmt.Printf("OUTPUT: %s\n", stdout)
		fmt.Printf("ERROR: %s\n", stderr)
		return err
	}
	log.Debugf("DONE writing to %s and %s", rawFile, d.diskPath())

	return nil
}

func (d *Driver) generateUserdataDisk(userdataFile string) (string, error) {
	// Start with virtio, add ISO & FAT format later
	// Start with local file, add wget/fetct URL? (or if URL, use datasource..)
	log.Infof("1")
	userdata, err := ioutil.ReadFile(userdataFile)
	if err != nil {
		return "", err
	}

	log.Infof("2")
	machineDir := filepath.Join(d.StorePath, "machines", d.GetMachineName())
	ccRoot := filepath.Join(machineDir, "cloud-config")
	os.MkdirAll(ccRoot, 0755)

	log.Infof("3")
	userDataDir := filepath.Join(ccRoot, "openstack/latest")
	os.MkdirAll(userDataDir, 0755)

	log.Infof("4")
	writeFile := filepath.Join(userDataDir, "user_data")
	if err := ioutil.WriteFile(writeFile, userdata, 0644); err != nil {
		return "", err
	}
	log.Infof("5")

	return ccRoot, nil

}

func (d *Driver) RunQMPCommand(command string) (map[string]interface{}, error) {

	// connect to monitor
	conn, err := net.Dial("unix", d.monitorPath())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// initial QMP response
	var buf [1024]byte
	nr, err := conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	type qmpInitialResponse struct {
		QMP struct {
			Version struct {
				QEMU struct {
					Micro int `json:"micro"`
					Minor int `json:"minor"`
					Major int `json:"major"`
				} `json:"qemu"`
				Package string `json:"package"`
			} `json:"version"`
			Capabilities []string `json:"capabilities"`
		} `jason:"QMP"`
	}

	var initialResponse qmpInitialResponse
	json.Unmarshal(buf[:nr], &initialResponse)

	// run 'qmp_capabilities' to switch to command mode
	// { "execute": "qmp_capabilities" }
	type qmpCommand struct {
		Command string `json:"execute"`
	}
	jsonCommand, err := json.Marshal(qmpCommand{Command: "qmp_capabilities"})
	if err != nil {
		return nil, err
	}
	_, err = conn.Write(jsonCommand)
	if err != nil {
		return nil, err
	}
	nr, err = conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	type qmpResponse struct {
		Return map[string]interface{} `json:"return"`
	}
	var response qmpResponse
	err = json.Unmarshal(buf[:nr], &response)
	if err != nil {
		return nil, err
	}
	// expecting empty response
	if len(response.Return) != 0 {
		return nil, fmt.Errorf("qmp_capabilities failed: %v", response.Return)
	}

	// { "execute": command }
	jsonCommand, err = json.Marshal(qmpCommand{Command: command})
	if err != nil {
		return nil, err
	}
	_, err = conn.Write(jsonCommand)
	if err != nil {
		return nil, err
	}
	nr, err = conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(buf[:nr], &response)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(command, "query-") {
		return response.Return, nil
	}
	// non-query commands should return an empty response
	if len(response.Return) != 0 {
		return nil, fmt.Errorf("%s failed: %v", command, response.Return)
	}
	return response.Return, nil
}

func WaitForTCPWithDelay(addr string, duration time.Duration) error {
	for {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			continue
		}
		defer conn.Close()
		if _, err = conn.Read(make([]byte, 1)); err != nil {
			time.Sleep(duration)
			continue
		}
		break
	}
	return nil
}
