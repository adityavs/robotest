package framework

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gravitational/robotest/infra"
	"github.com/gravitational/robotest/lib/defaults"
	"github.com/gravitational/robotest/lib/loc"
	"github.com/gravitational/robotest/lib/system"
	"github.com/gravitational/trace"

	log "github.com/Sirupsen/logrus"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	web "github.com/sclevine/agouti"
)

// driver is a test-global web driver instance
var driver *web.WebDriver

// New creates a new instance of the framework.
// Creating a framework instance installs a set of BeforeEach/AfterEach to
// emulate BeforeAll/AfterAll for controlled access to resources that should
// only be created once per context
func New() *T {
	f := &T{}

	BeforeEach(f.BeforeEach)
	AfterEach(f.AfterEach)

	return f
}

// T defines a framework type.
// Framework stores attributes common to a single context
type T struct {
	Page *web.Page
}

// BeforeEach emulates BeforeAll for a context.
// It creates a new web page that is only initialized once per series of It
// grouped in any given context
func (r *T) BeforeEach() {
	if r.Page == nil {
		var err error
		r.Page, err = driver.NewPage()
		Expect(err).NotTo(HaveOccurred())
	}
}

func (r *T) AfterEach() {
}

// CreateDriver creates a new instance of the web driver
func CreateDriver() {
	driver = web.ChromeDriver()
	Expect(driver).NotTo(BeNil())
	Expect(driver.Start()).To(Succeed())
}

// CloseDriver stops and closes the test-global web driver
func CloseDriver() {
	Expect(driver.Stop()).To(Succeed())
}

// Distribute executes the specified command on nodes
func Distribute(command string, nodes ...infra.Node) {
	Expect(Cluster).NotTo(BeNil(), "requires a cluster")
	Expect(Cluster.Provisioner()).NotTo(BeNil(), "requires a provisioner")
	if len(nodes) == 0 {
		nodes = Cluster.Provisioner().NodePool().AllocedNodes()
		log.Infof("allocated nodes: %#v", nodes)
	}
	Expect(infra.Distribute(command, nodes...)).To(Succeed())
}

// Cluster is the global instance of the cluster the tests are executed on
var Cluster infra.Infra

// installerNode is the node with installer running on it in case the tests
// are running in wizard mode
var installerNode infra.Node

// InitializeCluster creates infrastructure according to configuration
func InitializeCluster() {
	config := infra.Config{ClusterName: TestContext.ClusterName}

	var err error
	var provisioner infra.Provisioner
	if testState != nil {
		if testState.Provisioner != "" {
			provisioner, err = provisionerFromState(config, *testState)
			Expect(err).NotTo(HaveOccurred())
		}
	} else {
		TestContext.StateDir, err = newStateDir(TestContext.ClusterName)
		Expect(err).NotTo(HaveOccurred())
		if TestContext.Provisioner != "" {
			provisioner, err = provisionerFromConfig(config, TestContext.StateDir, TestContext.Provisioner)
			Expect(err).NotTo(HaveOccurred())

			installerNode, err = provisioner.Create()
			Expect(err).NotTo(HaveOccurred())
		}
	}

	var application *loc.Locator
	if TestContext.Wizard {
		Cluster, application, err = infra.NewWizard(config, provisioner, installerNode)
		TestContext.Application = application
	} else {
		Cluster, err = infra.New(config, TestContext.OpsCenterURL, provisioner)
	}
	Expect(err).NotTo(HaveOccurred())

	switch {
	case testState == nil:
		log.Debug("init test state")
		testState = &TestState{
			OpsCenterURL: Cluster.OpsCenterURL(),
			StateDir:     TestContext.StateDir,
		}
		if Cluster.Provisioner() != nil {
			testState.Provisioner = TestContext.Provisioner
			provisionerState := Cluster.Provisioner().State()
			testState.ProvisionerState = &provisionerState
		}
		Expect(saveState(withBackup)).To(Succeed())
		TestContext.StateDir = testState.StateDir
	case testState != nil:
		if Cluster.Provisioner() != nil && TestContext.Onprem.InstallerURL != "" {
			// Get reference to installer node if the cluster was provisioned with installer
			installerNode, err = Cluster.Provisioner().NodePool().Node(testState.ProvisionerState.InstallerAddr)
			Expect(err).NotTo(HaveOccurred())
		}
	}
}

// Destroy destroys the infrastructure created previously in InitializeCluster
// and removes state directory
func Destroy() {
	if Cluster != nil {
		Expect(Cluster.Close()).To(Succeed())
		Expect(Cluster.Destroy()).To(Succeed())
	}
	// Clean up state
	err := os.Remove(stateConfigFile)
	if err != nil && !os.IsNotExist(err) {
		Failf("failed to remove state file %q: %v", stateConfigFile, err)
	}
	err = system.RemoveAll(TestContext.StateDir)
	if err != nil && !os.IsNotExist(err) {
		Failf("failed to cleanup state directory %q: %v", TestContext.StateDir, err)
	}
}

// UpdateState updates the state file with the current provisioner state.
// It validates the context to avoid updating a state file on an inactive
// or automatically provisioned cluster
func UpdateState() {
	if Cluster == nil || testState == nil {
		log.Infof("cluster inactive: skip UpdateState")
		return
	}
	if Cluster.Provisioner() != nil {
		provisionerState := Cluster.Provisioner().State()
		testState.ProvisionerState = &provisionerState
	}

	Expect(saveState(withoutBackup)).To(Succeed())
}

// CoreDump collects diagnostic information into the specified report directory
// after the tests
func CoreDump() {
	if Cluster == nil {
		log.Infof("cluster inactive: skip CoreDump")
		return
	}
	if TestContext.ServiceLogin.IsEmpty() {
		log.Infof("no service login configured: skip CoreDump")
		return
	}

	err := ConnectToOpsCenter(TestContext.OpsCenterURL, TestContext.ServiceLogin)
	if err != nil {
		// If connect to Ops Center fails, no site report can be collected
		// so bail out
		log.Errorf("failed to connect to the Ops Center %q: %v", TestContext.OpsCenterURL, err)
		return
	}

	output := filepath.Join(TestContext.ReportDir, "crashreport.tar.gz")
	stateDir := fmt.Sprintf("--state-dir=%v", TestContext.StateDir)
	opsURL := fmt.Sprintf("--ops-url=%v", Cluster.OpsCenterURL())
	cmd := exec.Command("gravity", "--insecure", stateDir, "site", "report", opsURL, TestContext.ClusterName, output)
	err = system.Exec(cmd, io.MultiWriter(os.Stderr, GinkgoWriter))
	if err != nil {
		log.Errorf("failed to collect site report: %v", err)
	}

	if Cluster.Provisioner() == nil {
		log.Infof("no provisioner: skip collecting provisioner logs")
		return
	}

	if installerNode != nil {
		// Collect installer log
		installerLog, err := os.Create(filepath.Join(TestContext.ReportDir, "installer.log"))
		Expect(err).NotTo(HaveOccurred())
		defer installerLog.Close()

		Expect(infra.ScpText(installerNode,
			Cluster.Provisioner().InstallerLogPath(), installerLog)).To(Succeed())
	}
	for _, node := range Cluster.Provisioner().NodePool().Nodes() {
		agentLog, err := os.Create(filepath.Join(TestContext.ReportDir,
			fmt.Sprintf("agent_%v.log", node.Addr())))
		Expect(err).NotTo(HaveOccurred())
		defer agentLog.Close()
		errCopy := infra.ScpText(node, defaults.AgentLogPath, agentLog)
		if errCopy != nil {
			log.Errorf("failed to fetch agent from %s: %v", node, errCopy)
		}
		// TODO: collect shrink operation agent logs
	}
}

// RoboDescribe is local wrapper function for ginkgo.Describe.
// It adds test namespacing.
// TODO: eventually benefit from safe test tags: https://github.com/kubernetes/kubernetes/pull/22401.
func RoboDescribe(text string, body func()) bool {
	return Describe("[robotest] "+text, body)
}

// RunAgentCommand interprets the specified command as agent command.
// It will modify the agent command line to start agent in background
// and will distribute the command on the specified nodes
func RunAgentCommand(command string, nodes ...infra.Node) {
	command, err := infra.ConfigureAgentCommandRunDetached(command)
	Expect(err).NotTo(HaveOccurred())
	Distribute(command, nodes...)
}

func saveState(withBackup backupFlag) error {
	file, err := os.Create(stateConfigFile)
	if err != nil {
		return trace.Wrap(err)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(testState)
	if err != nil {
		return trace.Wrap(err)
	}
	if withBackup {
		filename := fmt.Sprintf("%vbackup", filepath.Base(stateConfigFile))
		stateConfigBackup := filepath.Join(filepath.Dir(stateConfigFile), filename)
		return trace.Wrap(system.CopyFile(stateConfigBackup, stateConfigFile))
	}
	return nil
}

func newStateDir(clusterName string) (dir string, err error) {
	dir, err = ioutil.TempDir("", fmt.Sprintf("robotest-%v-", clusterName))
	if err != nil {
		return "", trace.Wrap(err)
	}
	log.Infof("state directory: %v", dir)
	return dir, nil
}

type backupFlag bool

const (
	withBackup    backupFlag = true
	withoutBackup backupFlag = false
)
