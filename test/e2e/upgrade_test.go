//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	metal3api "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/bootstrap"
)

const hardwareDetailsRelease04 = `
{
  "cpu": {
    "arch": "x86_64",
    "count": 2,
    "flags": [
      "3dnowprefetch",
      "abm",
      "adx",
      "aes",
      "apic",
      "arat",
      "arch_capabilities",
      "avx",
      "avx2",
      "avx_vnni",
      "bmi1",
      "bmi2",
      "clflush",
      "clflushopt",
      "clwb",
      "cmov",
      "constant_tsc",
      "cpuid",
      "cpuid_fault",
      "cx16",
      "cx8",
      "de",
      "ept",
      "ept_ad",
      "erms",
      "f16c",
      "flexpriority",
      "fma",
      "fpu",
      "fsgsbase",
      "fsrm",
      "fxsr",
      "gfni",
      "hypervisor",
      "ibpb",
      "ibrs",
      "ibrs_enhanced",
      "invpcid",
      "lahf_lm",
      "lm",
      "mca",
      "mce",
      "md_clear",
      "mmx",
      "movbe",
      "movdir64b",
      "movdiri",
      "msr",
      "mtrr",
      "nopl",
      "nx",
      "ospke",
      "pae",
      "pat",
      "pclmulqdq",
      "pdpe1gb",
      "pge",
      "pku",
      "pni",
      "popcnt",
      "pse",
      "pse36",
      "rdpid",
      "rdrand",
      "rdseed",
      "rdtscp",
      "rep_good",
      "sep",
      "serialize",
      "sha_ni",
      "smap",
      "smep",
      "ss",
      "ssbd",
      "sse",
      "sse2",
      "sse4_1",
      "sse4_2",
      "ssse3",
      "stibp",
      "syscall",
      "tpr_shadow",
      "tsc",
      "tsc_adjust",
      "tsc_deadline_timer",
      "tsc_known_freq",
      "umip",
      "vaes",
      "vme",
      "vmx",
      "vnmi",
      "vpclmulqdq",
      "vpid",
      "waitpkg",
      "x2apic",
      "xgetbv1",
      "xsave",
      "xsavec",
      "xsaveopt",
      "xsaves",
      "xtopology"
    ],
    "model": "12th Gen Intel(R) Core(TM) i9-12900H"
  },
  "firmware": {
    "bios": {
      "date": "04/01/2014",
      "vendor": "SeaBIOS",
      "version": "1.15.0-1"
    }
  },
  "hostname": "bmo-e2e-1",
  "nics": [
    {
      "ip": "192.168.223.122",
      "mac": "00:60:2f:31:81:02",
      "model": "0x1af4 0x0001",
      "name": "enp1s0",
      "pxe": true
    },
    {
      "ip": "fe80::570a:edf2:a3a7:4eb8%enp1s0",
      "mac": "00:60:2f:31:81:02",
      "model": "0x1af4 0x0001",
      "name": "enp1s0",
      "pxe": true
    }
  ],
  "ramMebibytes": 4096,
  "storage": [
    {
      "name": "/dev/disk/by-path/pci-0000:04:00.0",
      "rotational": true,
      "sizeBytes": 21474836480,
      "type": "HDD",
      "vendor": "0x1af4"
    }
  ],
  "systemVendor": {
    "manufacturer": "QEMU",
    "productName": "Standard PC (Q35 + ICH9, 2009)"
  }
}
`

// RunUpgradeTest tests upgrade from an older version of BMO or Ironic --> main branch version with the following steps:
//   - Initiate the cluster with an the older version of either BMO or Ironic, and the latest Ironic/BMO version that is suitable with it
//   - Create a new namespace, and in it a BMH object with "disabled" annotation.
//   - Wait until the BMH gets to "available" state. Because of the "disabled" annotation, it won't get further provisioned.
//   - Upgrade BMO/Ironic to latest version.
//   - Patch the BMH object with proper specs, so that it could be provisioned.
//   - If the BMH is successfully provisioned, it means the upgraded BMO/Ironic recognized that BMH, hence the upgrade succeeded.
//
// The function returns the namespace object, with its cancelFunc. These can be used to clean up the created resources.
func RunUpgradeTest(ctx context.Context, input *BMOIronicUpgradeInput, upgradeClusterProxy framework.ClusterProxy) (*corev1.Namespace, context.CancelFunc) {
	bmoIronicNamespace := "baremetal-operator-system"
	initBMOKustomization := input.InitBMOKustomization
	initIronicKustomization := input.InitIronicKustomization
	upgradeEntityName := input.UpgradeEntityName
	specName := "upgrade"
	var upgradeDeploymentName, upgradeFromKustomization string
	switch upgradeEntityName {
	case bmoString:
		upgradeFromKustomization = initBMOKustomization
		upgradeDeploymentName = "baremetal-operator-controller-manager"
	case ironicString:
		upgradeFromKustomization = initIronicKustomization
		upgradeDeploymentName = "ironic-service"
	}
	upgradeFromKustomizationName := strings.ReplaceAll(filepath.Base(upgradeFromKustomization), ".", "-")
	testCaseName := fmt.Sprintf("%s-upgrade-from-%s", upgradeEntityName, upgradeFromKustomizationName)
	testCaseArtifactFolder := filepath.Join(artifactFolder, testCaseName)
	if input.DeployIronic {
		// Install Ironic
		By(fmt.Sprintf("Installing Ironic from kustomization %s on the upgrade cluster", initIronicKustomization))
		err := BuildAndApplyKustomization(ctx, &BuildAndApplyKustomizationInput{
			Kustomization:       initIronicKustomization,
			ClusterProxy:        upgradeClusterProxy,
			WaitForDeployment:   false,
			WatchDeploymentLogs: true,
			DeploymentName:      "ironic-service",
			DeploymentNamespace: bmoIronicNamespace,
			LogPath:             filepath.Join(testCaseArtifactFolder, "logs", "init-ironic"),
		})
		WaitForIronicReady(ctx, WaitForIronicInput{
			Client:    clusterProxy.GetClient(),
			Name:      "ironic",
			Namespace: bmoIronicNamespace,
			Intervals: e2eConfig.GetIntervals("ironic", "wait-deployment"),
		})
		Expect(err).NotTo(HaveOccurred())
	}
	if input.DeployBMO {
		// Install BMO
		By(fmt.Sprintf("Installing BMO from %s on the upgrade cluster", initBMOKustomization))
		err := FlakeAttempt(2, func() error {
			return BuildAndApplyKustomization(ctx, &BuildAndApplyKustomizationInput{
				Kustomization:       initBMOKustomization,
				ClusterProxy:        upgradeClusterProxy,
				WaitForDeployment:   true,
				WatchDeploymentLogs: true,
				DeploymentName:      "baremetal-operator-controller-manager",
				DeploymentNamespace: bmoIronicNamespace,
				LogPath:             filepath.Join(testCaseArtifactFolder, "logs", "init-bmo"),
				WaitIntervals:       e2eConfig.GetIntervals("default", "wait-deployment"),
			})
		})
		Expect(err).NotTo(HaveOccurred())
	}

	namespace, cancelWatches := framework.CreateNamespaceAndWatchEvents(ctx, framework.CreateNamespaceAndWatchEventsInput{
		Creator:             upgradeClusterProxy.GetClient(),
		ClientSet:           upgradeClusterProxy.GetClientSet(),
		Name:                "upgrade-" + input.UpgradeEntityName,
		LogFolder:           testCaseArtifactFolder,
		IgnoreAlreadyExists: true,
	})

	By("Creating a secret with BMH credentials")
	bmcCredentialsData := map[string]string{
		"username": bmc.User,
		"password": bmc.Password,
	}
	secretName := "bmc-credentials"
	CreateSecret(ctx, upgradeClusterProxy.GetClient(), namespace.Name, secretName, bmcCredentialsData)

	By("Creating a BMH with inspection disabled and hardware details added")
	bmh := metal3api.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade",
			Namespace: namespace.Name,
			Annotations: map[string]string{
				metal3api.InspectAnnotationPrefix: "disabled",
				// hardwareDetails of release0.4 is compatible to release0.3 and release0.5 as well
				// This can be changed to the new hardwareDetails once we no longer test release0.4
				metal3api.HardwareDetailsAnnotation: hardwareDetailsRelease04,
			},
		},
		Spec: metal3api.BareMetalHostSpec{
			Online: true,
			BMC: metal3api.BMCDetails{
				Address:                        bmc.Address,
				CredentialsName:                secretName,
				DisableCertificateVerification: bmc.DisableCertificateVerification,
			},
			BootMode:       metal3api.Legacy,
			BootMACAddress: bmc.BootMacAddress,
		},
	}
	err := upgradeClusterProxy.GetClient().Create(ctx, &bmh)
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for the BMH to become available")
	WaitForBmhInProvisioningState(ctx, WaitForBmhInProvisioningStateInput{
		Client: upgradeClusterProxy.GetClient(),
		Bmh:    bmh,
		State:  metal3api.StateAvailable,
	}, e2eConfig.GetIntervals(specName, "wait-available")...)

	// TODO(lentzi90): Since the introduction of IrSO, we should not be dealing with Deployments for Ironic.
	// We should split this test into Ironic upgrade and BMO upgrade and handle them separately.
	By(fmt.Sprintf("Upgrading %s deployment", input.UpgradeEntityName))
	clientSet := upgradeClusterProxy.GetClientSet()
	deploy, err := clientSet.AppsV1().Deployments(bmoIronicNamespace).Get(ctx, upgradeDeploymentName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	upgradeKustomization := input.UpgradeEntityKustomization
	err = FlakeAttempt(2, func() error {
		return BuildAndApplyKustomization(ctx, &BuildAndApplyKustomizationInput{
			Kustomization:       upgradeKustomization,
			ClusterProxy:        upgradeClusterProxy,
			WaitForDeployment:   false,
			WatchDeploymentLogs: true,
			DeploymentName:      upgradeDeploymentName,
			DeploymentNamespace: bmoIronicNamespace,
			LogPath:             filepath.Join(testCaseArtifactFolder, "logs", "bmo-upgrade-main"),
		})
	})
	Expect(err).NotTo(HaveOccurred())

	By(fmt.Sprintf("Waiting for %s update to rollout", input.UpgradeEntityName))
	Eventually(func() bool {
		return DeploymentRolledOut(ctx, upgradeClusterProxy, upgradeDeploymentName, bmoIronicNamespace, deploy.Status.ObservedGeneration+1)
	},
		e2eConfig.GetIntervals("ironic", "wait-deployment")...,
	).Should(BeTrue())
	if input.UpgradeEntityName == ironicString {
		WaitForIronicReady(ctx, WaitForIronicInput{
			Client:    clusterProxy.GetClient(),
			Name:      "ironic",
			Namespace: bmoIronicNamespace,
			Intervals: e2eConfig.GetIntervals("ironic", "wait-deployment"),
		})
	}

	By("Patching the BMH to test provisioning")
	// Using Eventually here since the webhook can take some time after the deployment is ready
	Eventually(func() error {
		return PatchBMHForProvisioning(ctx, PatchBMHForProvisioningInput{
			client:    upgradeClusterProxy.GetClient(),
			bmh:       &bmh,
			bmc:       bmc,
			e2eConfig: e2eConfig,
		})
	}, e2eConfig.GetIntervals("default", "wait-deployment")...).Should(Succeed())

	By("Waiting for the BMH to become provisioned")
	WaitForBmhInProvisioningState(ctx, WaitForBmhInProvisioningStateInput{
		Client: upgradeClusterProxy.GetClient(),
		Bmh:    bmh,
		State:  metal3api.StateProvisioned,
	}, e2eConfig.GetIntervals(specName, "wait-provisioned")...)
	return namespace, cancelWatches
}

var _ = Describe("Upgrade", Label("optional", "upgrade"), func() {

	var (
		upgradeClusterProxy    framework.ClusterProxy
		upgradeClusterProvider bootstrap.ClusterProvider
		entries                []TableEntry
		namespace              *corev1.Namespace
		cancelWatches          context.CancelFunc
		specName               = "upgrade"
	)

	for i := range e2eConfig.BMOIronicUpgradeSpecs {
		entries = append(entries, Entry(nil, ctx, &e2eConfig.BMOIronicUpgradeSpecs[i]))
	}

	BeforeEach(func() {
		// Before each test, we need to	initiate the cluster and/or prepare it to be ready for the test
		var kubeconfigPath string

		if e2eConfig.GetBoolVariable("UPGRADE_USE_EXISTING_CLUSTER") {
			kubeconfigPath = GetKubeconfigPath()
		} else {
			By("Creating a separate cluster for upgrade tests")
			upgradeClusterName := fmt.Sprintf("bmo-e2e-upgrade-%d", GinkgoParallelProcess())
			upgradeClusterProvider = bootstrap.CreateKindBootstrapClusterAndLoadImages(ctx, bootstrap.CreateKindBootstrapClusterAndLoadImagesInput{
				Name:              upgradeClusterName,
				Images:            e2eConfig.Images,
				ExtraPortMappings: e2eConfig.KindExtraPortMappings,
			})
			Expect(upgradeClusterProvider).ToNot(BeNil(), "Failed to create a cluster")
			kubeconfigPath = upgradeClusterProvider.GetKubeconfigPath()
		}
		Expect(kubeconfigPath).To(BeAnExistingFile(), "Failed to get the kubeconfig file for the cluster")
		scheme := runtime.NewScheme()
		framework.TryAddDefaultSchemes(scheme)
		err := metal3api.AddToScheme(scheme)
		Expect(err).NotTo(HaveOccurred())
		upgradeClusterProxy = framework.NewClusterProxy("bmo-e2e-upgrade", kubeconfigPath, scheme)

		if e2eConfig.GetBoolVariable("UPGRADE_DEPLOY_CERT_MANAGER") {
			By("Installing cert-manager on the upgrade cluster")
			cmVersion := e2eConfig.GetVariable("CERT_MANAGER_VERSION")
			err := installCertManager(ctx, upgradeClusterProxy, cmVersion)
			Expect(err).NotTo(HaveOccurred())
			By("Waiting for cert-manager webhook")
			Eventually(func() error {
				return checkCertManagerWebhook(ctx, upgradeClusterProxy)
			}, e2eConfig.GetIntervals("default", "wait-available")...).Should(Succeed())
			err = checkCertManagerAPI(upgradeClusterProxy)
			Expect(err).NotTo(HaveOccurred())
		}
		if e2eConfig.GetBoolVariable("UPGRADE_DEPLOY_IRSO") {
			BuildAndApplyKustomization(ctx, &BuildAndApplyKustomizationInput{
				Kustomization:       e2eConfig.GetVariable("IRSO_KUSTOMIZATION"),
				ClusterProxy:        clusterProxy,
				WaitForDeployment:   true,
				WatchDeploymentLogs: true,
				DeploymentName:      "ironic-standalone-operator-controller-manager",
				DeploymentNamespace: "ironic-standalone-operator-system",
				LogPath:             filepath.Join(artifactFolder, "logs", "ironic-standalone-operator-system"),
				WaitIntervals:       e2eConfig.GetIntervals("default", "wait-deployment"),
			})
		}
	})
	DescribeTable("",
		// Test function that runs for each table entry
		func(ctx context.Context, input *BMOIronicUpgradeInput) {
			namespace, cancelWatches = RunUpgradeTest(ctx, input, upgradeClusterProxy)
		},
		// Description function that generates test descriptions
		func(ctx context.Context, input *BMOIronicUpgradeInput) string {
			var upgradeFromKustomization string
			upgradeEntityName := input.UpgradeEntityName
			switch upgradeEntityName {
			case bmoString:
				upgradeFromKustomization = input.InitBMOKustomization
			case ironicString:
				upgradeFromKustomization = input.InitIronicKustomization
			}
			return fmt.Sprintf("Should upgrade %s from %s to latest version", input.UpgradeEntityName, upgradeFromKustomization)
		},
		entries,
	)

	AfterEach(func() {
		DumpResources(ctx, e2eConfig, clusterProxy, path.Join(artifactFolder, specName))
		if !skipCleanup {
			cleanup(ctx, upgradeClusterProxy, namespace, cancelWatches, e2eConfig.GetIntervals("default", "wait-namespace-deleted")...)
			if e2eConfig.GetBoolVariable("UPGRADE_USE_EXISTING_CLUSTER") {
				// Try to clean up as best as we can.
				// Note that we only delete the "normal" BMO kustomization. There could be small
				// differences between this and the initial or upgrade kustomization, but this also
				// cleans up the namespace, which should take care of everything except CRDs
				// and cluster-scoped RBAC, including Ironic if it was deployed.
				// There is a theoretical risk that we leak cluster-scoped resources for the
				// next test here, if there are differences between the kustomizations.
				cleanupBaremetalOperatorSystem(ctx, upgradeClusterProxy, e2eConfig.GetVariable("BMO_KUSTOMIZATION"))
			} else {
				// We are using a kind cluster for the upgrade tests, so we just delete the cluster.
				upgradeClusterProvider.Dispose(ctx)
			}
			upgradeClusterProxy.Dispose(ctx)
		}
	})
})

// cleanupBaremetalOperatorSystem removes the kustomization from the cluster and waits for the
// baremetal-operator-system namespace to be deleted.
func cleanupBaremetalOperatorSystem(ctx context.Context, clusterProxy framework.ClusterProxy, kustomization string) {
	BuildAndRemoveKustomization(ctx, kustomization, clusterProxy)
	// We need to ensure that the namespace actually gets deleted.
	WaitForNamespaceDeleted(ctx, WaitForNamespaceDeletedInput{
		Getter:    clusterProxy.GetClient(),
		Namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "baremetal-operator-system"}},
	}, e2eConfig.GetIntervals("default", "wait-namespace-deleted")...)
}
