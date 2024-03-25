/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ovncontroller

import (
	"fmt"
	"strings"

	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	ovnv1 "github.com/openstack-k8s-operators/ovn-operator/api/v1beta1"
	ovn_common "github.com/openstack-k8s-operators/ovn-operator/pkg/common"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func GetDaemonSetSpec(
	instance *ovnv1.OVNController,
	name string,
	images []string,
	volumeMounts [][]corev1.VolumeMount,
	volumes []corev1.Volume,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
	containerNames []string,
	containerCmds [][]string,
	containerArgs [][]string,
	preStopCmds [][]string,
	livenessProbes []*corev1.Probe,
) *appsv1.DaemonSet {

	runAsUser := int64(0)
	privileged := true

	envVars := map[string]env.Setter{}
	envVars["CONFIG_HASH"] = env.SetValue(configHash)

	containers := []corev1.Container{}
	for i, containername := range containerNames {
		container := corev1.Container{
			Name:    containername,
			Command: containerCmds[i],
			Args:    containerArgs[i],
			Lifecycle: &corev1.Lifecycle{
				PreStop: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{
						Command: preStopCmds[i],
					},
				},
			},
			Image: images[i],
			SecurityContext: &corev1.SecurityContext{
				Capabilities: &corev1.Capabilities{
					Add:  []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_NICE"},
					Drop: []corev1.Capability{},
				},
				RunAsUser:  &runAsUser,
				Privileged: &privileged,
			},
			Env:                      env.MergeEnvs([]corev1.EnvVar{}, envVars),
			VolumeMounts:             volumeMounts[i],
			Resources:                instance.Spec.Resources,
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		}
		if livenessProbes != nil && len(livenessProbes) > i {
			container.LivenessProbe = livenessProbes[i]
		}
		containers = append(containers, container)
	}

	daemonset := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: instance.RbacResourceName(),
					Containers:         containers,
				},
			},
		},
	}
	daemonset.Spec.Template.Spec.Volumes = volumes

	if instance.Spec.NodeSelector != nil && len(instance.Spec.NodeSelector) > 0 {
		daemonset.Spec.Template.Spec.NodeSelector = instance.Spec.NodeSelector
	}

	if annotations != nil && len(annotations) > 0 {
		daemonset.Spec.Template.ObjectMeta.Annotations = annotations
	}

	return daemonset
}

func CreateOVNDaemonSet(
	instance *ovnv1.OVNController,
	configHash string,
	labels map[string]string,
) *appsv1.DaemonSet {
	volumes := GetVolumes(instance.Name, instance.Namespace)
	commonVolumeMounts := []corev1.VolumeMount{}

	// add CA bundle if defined
	if instance.Spec.TLS.CaBundleSecretName != "" {
		volumes = append(volumes, instance.Spec.TLS.CreateVolume())
		commonVolumeMounts = append(commonVolumeMounts, instance.Spec.TLS.CreateVolumeMounts(nil)...)
	}

	ovnControllerVolumeMounts := append(GetOvnControllerVolumeMounts(), commonVolumeMounts...)

	// add OVN dbs cert and CA
	var ovnControllerTLSArgs []string
	if instance.Spec.TLS.Enabled() {
		svc := tls.Service{
			SecretName: *instance.Spec.TLS.GenericService.SecretName,
			CertMount:  ptr.To(ovn_common.OVNDbCertPath),
			KeyMount:   ptr.To(ovn_common.OVNDbKeyPath),
			CaMount:    ptr.To(ovn_common.OVNDbCaCertPath),
		}
		volumes = append(volumes, svc.CreateVolume(ovnv1.ServiceNameOvnController))
		ovnControllerVolumeMounts = append(ovnControllerVolumeMounts, svc.CreateVolumeMounts(ovnv1.ServiceNameOvnController)...)
		ovnControllerTLSArgs = []string{
			fmt.Sprintf("--certificate=%s", ovn_common.OVNDbCertPath),
			fmt.Sprintf("--private-key=%s", ovn_common.OVNDbKeyPath),
			fmt.Sprintf("--ca-cert=%s", ovn_common.OVNDbCaCertPath),
		}
	}

	var name string
	var containerNames []string
	var containerImages []string
	var containerCmds [][]string
	var containerArgs [][]string
	var preStopCmds [][]string
	var livenessProbes []*corev1.Probe
	var volumeMounts [][]corev1.VolumeMount

	name = "ovn-controller"
	containerImages = []string{instance.Spec.OvnContainerImage}
	containerNames = []string{"ovn-controller"}
	containerCmds = [][]string{{"/bin/bash", "-c"}}
	containerArgs = [][]string{{}}
	containerArgs[0] = []string{
		strings.Join(
			append(
				[]string{"ovn-controller"},
				append(ovnControllerTLSArgs, "--pidfile", "unix:/run/openvswitch/db.sock")...,
			),
			" ",
		),
	}

	preStopCmds = [][]string{{"/usr/share/ovn/scripts/ovn-ctl", "stop_controller"}}
	livenessProbes = nil
	volumeMounts = [][]corev1.VolumeMount{ovnControllerVolumeMounts}

	return GetDaemonSetSpec(instance, name, containerImages, volumeMounts, volumes, configHash, labels, nil, containerNames, containerCmds, containerArgs, preStopCmds, livenessProbes)
}

func CreateOVSDaemonSet(
	instance *ovnv1.OVNController,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
) *appsv1.DaemonSet {
	volumes := GetVolumes(instance.Name, instance.Namespace)
	commonVolumeMounts := []corev1.VolumeMount{}
	//
	// https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
	//
	ovsDbLivenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}

	ovsVswitchdLivenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}

	var name string
	var containerNames []string
	var containerImages []string
	var containerCmds [][]string
	var containerArgs [][]string
	var preStopCmds [][]string
	var livenessProbes []*corev1.Probe
	var volumeMounts [][]corev1.VolumeMount

	ovsDbLivenessProbe.Exec = &corev1.ExecAction{
		Command: []string{
			"/usr/bin/ovs-vsctl",
			"show",
		},
	}
	ovsVswitchdLivenessProbe.Exec = &corev1.ExecAction{
		Command: []string{
			"/usr/bin/ovs-appctl",
			"bond/show",
		},
	}
	name = "ovn-controller-ovs"
	containerImages = []string{instance.Spec.OvsContainerImage, instance.Spec.OvsContainerImage}
	containerNames = []string{"ovsdb-server", "ovs-vswitchd"}
	containerCmds = [][]string{{"/usr/bin/dumb-init"}, {"/bin/bash", "-c"}}
	containerArgs = [][]string{{"--single-child", "--", "/usr/local/bin/container-scripts/start-ovsdb-server.sh"}, {"/usr/local/bin/container-scripts/net_setup.sh && /usr/sbin/ovs-vswitchd --pidfile", "--mlockall"}}
	preStopCmds = [][]string{{"/usr/share/openvswitch/scripts/ovs-ctl", "stop", "--no-ovs-vswitchd"}, {"/usr/share/openvswitch/scripts/ovs-ctl", "stop", "--no-ovsdb-server"}}
	livenessProbes = []*corev1.Probe{ovsDbLivenessProbe, ovsVswitchdLivenessProbe}
	volumeMounts = [][]corev1.VolumeMount{append(GetOvsDbVolumeMounts(), commonVolumeMounts...), append(GetVswitchdVolumeMounts(), commonVolumeMounts...)}

	return GetDaemonSetSpec(instance, name, containerImages, volumeMounts, volumes, configHash, labels, annotations, containerNames, containerCmds, containerArgs, preStopCmds, livenessProbes)
}
