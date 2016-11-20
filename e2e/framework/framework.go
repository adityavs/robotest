package framework

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gravitational/robotest/infra"
	"github.com/gravitational/robotest/lib/loc"
	"github.com/gravitational/robotest/lib/system"
	"github.com/gravitational/trace"

	log "github.com/Sirupsen/logrus"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	web "github.com/sclevine/agouti"
)

var driver *web.WebDriver

func New() *T {
	f := &T{}

	BeforeEach(f.BeforeEach)
	AfterEach(f.AfterEach)

	return f
}

type T struct {
	Page *web.Page
}

func (r *T) BeforeEach() {
	if r.Page == nil {
		var err error
		r.Page, err = driver.NewPage()
		Expect(err).NotTo(HaveOccurred())
	}
}

func (r *T) InstallerPage(authType AuthType) *web.Page {
	url, err := InstallerURL()
	Expect(err).NotTo(HaveOccurred())
	EnsureUser(r.Page, url,
		TestContext.Login.Username,
		TestContext.Login.Password, authType)
	return r.Page
}

func (r *T) AfterEach() {
}

func CreateDriver() {
	driver = web.ChromeDriver()
	Expect(driver).NotTo(BeNil())
	Expect(driver.Start()).To(Succeed())
}

func CloseDriver() {
	Expect(driver.Stop()).To(Succeed())
}

// Cluster is the global instance of the cluster the tests are executed on
var Cluster infra.Infra

func SetupCluster() {
	config := infra.Config{ClusterName: TestContext.ClusterName}

	var provisioner infra.Provisioner
	var installerNode infra.Node
	if TestContext.Provisioner != "" {
		stateDir, err := newStateDir(TestContext.ClusterName)
		Expect(err).NotTo(HaveOccurred())

		provisioner, err = provisionerFromConfig(config, stateDir, TestContext.Provisioner)
		Expect(err).NotTo(HaveOccurred())

		installerNode, err = provisioner.Create()
		Expect(err).NotTo(HaveOccurred())
	}

	var err error
	var application *loc.Locator
	if TestContext.Wizard {
		Cluster, application, err = infra.NewWizard(config, provisioner, installerNode)
		TestContext.Application = application
	} else {
		Cluster, err = infra.New(config, TestContext.OpsCenterURL, provisioner)
	}
	Expect(err).NotTo(HaveOccurred())
}

func DestroyCluster() {
	if Cluster != nil {
		Expect(Cluster.Close()).To(Succeed())
		Expect(Cluster.Destroy()).To(Succeed())
	}
}

// CoreDump collects diagnostic information into the specified report directory
// after the tests
func CoreDump() {
	output := filepath.Join(TestContext.ReportDir, "crashreport.tar.gz")
	opsURL := fmt.Sprintf("--ops-url=%v", Cluster.OpsCenterURL())
	cmd := exec.Command("gravity", "site", "report", opsURL, TestContext.ClusterName, output)
	err := system.Exec(cmd, io.MultiWriter(os.Stderr, GinkgoWriter))
	if err != nil {
		Failf("failed to collect diagnostics: %v", err)
	}
}

// RoboDescribe is local wrapper function for ginkgo.Describe.
// It adds test namespacing.
// TODO: eventually benefit from safe test tags: https://github.com/kubernetes/kubernetes/pull/22401.
func RoboDescribe(text string, body func()) bool {
	return Describe("[robotest] "+text, body)
}

func newStateDir(clusterName string) (dir string, err error) {
	dir, err = ioutil.TempDir("", fmt.Sprintf("robotest-%v-", clusterName))
	if err != nil {
		return "", trace.Wrap(err)
	}
	log.Infof("state directory: %v", dir)
	return dir, nil
}
