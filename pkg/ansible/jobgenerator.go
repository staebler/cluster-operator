/*
Copyright 2018 The Kubernetes Authors.

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

package ansible

import (
	"path"
	"time"

	log "github.com/sirupsen/logrus"

	kbatch "k8s.io/api/batch/v1"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusteroperator "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
)

const (
	openshiftAnsibleContainerDir = "/usr/share/ansible/openshift-ansible/"
)

// JobGenerator is used to generate jobs that run ansible playbooks.
type JobGenerator interface {
	// GeneratePlaybookJob generates a job to run the specified playbook.
	// Note that neither the job nor the configmap will be created in the API
	// server. It is the responsibility of the caller to create the objects
	// in the API server.
	//
	// name - name to give the job and the configmap
	// hardware - details of the hardware of the target cluster
	// playbook - name of the playbook to run
	// inventory - inventory to pass to the playbook
	// vars - Ansible variables to the pass to the playbook
	GeneratePlaybookJob(name string, hardware *clusteroperator.ClusterHardwareSpec, playbook, inventory, vars string) (*kbatch.Job, *kapi.ConfigMap)
}

type jobGenerator struct {
	image           string
	imagePullPolicy kapi.PullPolicy
}

// NewJobGenerator creates a new JobGenerator that can be used to create
// Ansible jobs.
//
// openshiftAnsibleImage - name of the openshift-ansible image that the
//   jobs created by the job generator will use
// openshiftAnsibleImagePullPolicy - policy to use to pull the
//   openshift-ansible image
func NewJobGenerator(openshiftAnsibleImage string, openshiftAnsibleImagePullPolicy kapi.PullPolicy) JobGenerator {
	return &jobGenerator{
		image:           openshiftAnsibleImage,
		imagePullPolicy: openshiftAnsibleImagePullPolicy,
	}
}

func (r *jobGenerator) generateInventoryConfigMap(name, inventory, vars string, logger *log.Entry) *kapi.ConfigMap {
	logger.Debugln("Generating inventory/vars ConfigMap")
	cfgMap := &kapi.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: map[string]string{
			"hosts": inventory,
			"vars":  vars,
		},
	}
	logger.Infoln("configmap generated")

	return cfgMap
}

func (r *jobGenerator) GeneratePlaybookJob(name string, hardware *clusteroperator.ClusterHardwareSpec, playbook, inventory, vars string) (*kbatch.Job, *kapi.ConfigMap) {

	logger := log.WithField("playbook", playbook)

	logger.Infoln("generating ansible playbook job")

	cfgMap := r.generateInventoryConfigMap(name, inventory, vars, logger)

	playbookPath := path.Join(openshiftAnsibleContainerDir, playbook)
	env := []kapi.EnvVar{
		{
			Name:  "INVENTORY_FILE",
			Value: "/ansible/inventory/hosts",
		},
		{
			Name:  "PLAYBOOK_FILE",
			Value: playbookPath,
		},
		{
			Name:  "ANSIBLE_HOST_KEY_CHECKING",
			Value: "False",
		},
		{
			Name:  "OPTS",
			Value: "-vvv --private-key=/ansible/ssh/privatekey.pem -e @/ansible/inventory/vars",
		},
	}

	if hardware.AWS != nil && len(hardware.AWS.AccountSecret.Name) > 0 {
		env = append(env, []kapi.EnvVar{
			{
				Name: "AWS_ACCESS_KEY_ID",
				ValueFrom: &kapi.EnvVarSource{
					SecretKeyRef: &kapi.SecretKeySelector{
						LocalObjectReference: hardware.AWS.AccountSecret,
						Key:                  "aws_access_key_id",
					},
				},
			},
			{
				Name: "AWS_SECRET_ACCESS_KEY",
				ValueFrom: &kapi.EnvVarSource{
					SecretKeyRef: &kapi.SecretKeySelector{
						LocalObjectReference: hardware.AWS.AccountSecret,
						Key:                  "aws_secret_access_key",
					},
				},
			},
		}...)
	}

	// sshKeyFileMode is used to set the file permissions for the private SSH key
	sshKeyFileMode := int32(0600)

	podSpec := kapi.PodSpec{
		DNSPolicy:     kapi.DNSClusterFirst,
		RestartPolicy: kapi.RestartPolicyNever,

		Containers: []kapi.Container{
			{
				Name:            "ansible",
				Image:           r.image,
				ImagePullPolicy: r.imagePullPolicy,
				Env:             env,
				VolumeMounts: []kapi.VolumeMount{
					{
						Name:      "inventory",
						MountPath: "/ansible/inventory/",
					},
				},
			},
		},
		Volumes: []kapi.Volume{
			{
				// Mounts both our inventory and vars file.
				Name: "inventory",
				VolumeSource: kapi.VolumeSource{
					ConfigMap: &kapi.ConfigMapVolumeSource{
						LocalObjectReference: kapi.LocalObjectReference{
							Name: cfgMap.Name,
						},
					},
				},
			},
		},
	}

	if hardware.AWS != nil {
		if len(hardware.AWS.SSHSecret.Name) > 0 {
			podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, kapi.VolumeMount{
				Name:      "sshkey",
				MountPath: "/ansible/ssh/",
			})
			podSpec.Volumes = append(podSpec.Volumes, kapi.Volume{
				Name: "sshkey",
				VolumeSource: kapi.VolumeSource{
					Secret: &kapi.SecretVolumeSource{
						SecretName: hardware.AWS.SSHSecret.Name,
						Items: []kapi.KeyToPath{
							{
								Key:  "ssh-privatekey",
								Path: "privatekey.pem",
								Mode: &sshKeyFileMode,
							},
						},
					},
				},
			})
		}
		if len(hardware.AWS.SSLSecret.Name) > 0 {
			podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, kapi.VolumeMount{
				Name:      "sslkey",
				MountPath: "/ansible/ssl/",
			})
			podSpec.Volumes = append(podSpec.Volumes, kapi.Volume{
				Name: "sslkey",
				VolumeSource: kapi.VolumeSource{
					Secret: &kapi.SecretVolumeSource{
						SecretName: hardware.AWS.SSLSecret.Name,
					},
				},
			})
		}
	}

	completions := int32(1)
	deadline := int64((24 * time.Hour).Seconds())
	// Only run pod three times. Otherwise, it can take the job controller too
	// long to notice when the pod has completed. If all three pod runs fail,
	// jobSync will create a new job.
	backoffLimit := 3

	job := &kbatch.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kbatch.JobSpec{
			Completions:           &completions,
			ActiveDeadlineSeconds: &deadline,
			BackoffLimit:          &backoffLimit,
			Template: kapi.PodTemplateSpec{
				Spec: podSpec,
			},
		},
	}

	return job, cfgMap
}
