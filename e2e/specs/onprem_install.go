package specs

import (
	"github.com/gravitational/robotest/e2e/framework"
	"github.com/gravitational/robotest/e2e/model/ui"
	"github.com/gravitational/robotest/e2e/model/ui/defaults"
	installermodel "github.com/gravitational/robotest/e2e/model/ui/installer"
	"github.com/gravitational/robotest/e2e/model/ui/site"
	bandwagon "github.com/gravitational/robotest/e2e/specs/asserts/bandwagon"
	validation "github.com/gravitational/robotest/e2e/specs/asserts/installer"

	log "github.com/Sirupsen/logrus"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func VerifyOnpremInstall(f *framework.T) {
	var _ = framework.RoboDescribe("Onprem Installation", func() {
		ctx := framework.TestContext
		var domainName string

		BeforeEach(func() {
			domainName = ctx.ClusterName
		})

		shouldProvideLicense := func() {
			installer := installermodel.Open(f.Page, framework.InstallerURL())
			By("filling out license text field if required")
			installer.FillOutLicenseIfRequired(ctx.License)
		}

		shouldHandleNewDeploymentScreen := func() {
			installer := installermodel.Open(f.Page, framework.InstallerURL())
			By("entering domain name")
			Eventually(installer.IsCreateSiteStep, defaults.FindTimeout).Should(BeTrue())
			installer.CreateOnPremNewSite(domainName)
		}

		shouldHandleRequirementsScreen := func() {
			installer := installermodel.OpenWithSite(f.Page, domainName)
			Expect(installer.IsRequirementsReviewStep()).To(BeTrue())

			By("selecting a flavor")
			numInstallNodes := installer.SelectFlavorByLabel(ctx.FlavorLabel)

			provisioner := framework.Cluster.Provisioner()
			Expect(provisioner).NotTo(BeNil(), "expected valid provisioner for onprem installation")
			log.Infof("allocating %v nodes", numInstallNodes)
			_, err := provisioner.NodePool().Allocate(numInstallNodes)
			Expect(err).NotTo(HaveOccurred())

			By("veryfing requirements")
			profiles := installermodel.FindOnPremProfiles(f.Page)
			Expect(len(profiles)).To(Equal(1))

			By("executing the command on servers")
			framework.RunAgentCommand(profiles[0].Command)

			By("waiting for agent report with the servers")
			Eventually(profiles[0].GetAgentServers, defaults.AgentServerTimeout).Should(
				HaveLen(numInstallNodes))

			By("configuring the servers with IPs")
			agentServers := profiles[0].GetAgentServers()

			for _, s := range agentServers {
				s.SetIPByInfra(provisioner)
			}

			By("starting an installation")
			installer.StartInstallation()
		}

		shouldHandleInProgressScreen := func() {
			validation.WaitForComplete(f.Page, domainName)
		}

		shouldHandleBandwagonScreen := func() {
			enableRemoteAccess := ctx.ForceRemoteAccess || !ctx.Wizard
			useLocalEndpoint := ctx.ForceLocalEndpoint || ctx.Wizard
			bandwagon.Complete(
				f.Page,
				domainName,
				defaults.BandwagonUsername,
				defaults.BandwagonPassword,
				enableRemoteAccess, useLocalEndpoint)
		}

		shouldNavigateToSite := func() {
			By("opening a site page")
			useLocalEndpoint := ctx.ForceLocalEndpoint || ctx.Wizard
			if useLocalEndpoint {
				siteURL := framework.SiteURL()
				login := framework.Login{
					Username: defaults.BandwagonUsername,
					Password: defaults.BandwagonPassword,
				}
				ui.EnsureLocalUser(f.Page, siteURL, login)
			}
			site.Open(f.Page, domainName)
		}

		It("should install an application", func() {
			ui.EnsureUser(f.Page, framework.InstallerURL(), ctx.Login)
			shouldProvideLicense()
			shouldHandleNewDeploymentScreen()
			shouldHandleRequirementsScreen()
			shouldHandleInProgressScreen()
			shouldHandleBandwagonScreen()
			shouldNavigateToSite()
		})
	})
}
