// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpoint

import (
	"encoding/json"
	"fmt"

	commonconsts "github.com/ai-dynamo/dynamo/deploy/operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
)

type restoreRuntimeEnvConsumer string

const (
	restoreRuntimeEnvConsumerParsedConfig restoreRuntimeEnvConsumer = "parsed Python runtime config"
	restoreRuntimeEnvConsumerRuntime      restoreRuntimeEnvConsumer = "DistributedRuntime creation"
	restoreRuntimeEnvConsumerSystem       restoreRuntimeEnvConsumer = "runtime system server"
	restoreRuntimeEnvConsumerKubernetes   restoreRuntimeEnvConsumer = "Kubernetes discovery"
	restoreRuntimeEnvConsumerInfra        restoreRuntimeEnvConsumer = "optional runtime infrastructure"
)

type restoreRuntimeEnvSpec struct {
	Name              string
	Consumer          restoreRuntimeEnvConsumer
	DefaultWhenUnset  *string
	RefreshesConfig   bool
	SafeForAnnotation bool
}

func restoreRuntimeDefault(value string) *string {
	return &value
}

var restoreRuntimeEnvSpecs = []restoreRuntimeEnvSpec{
	// Runtime constructor args. These are parsed before checkpoint, so the
	// Python snapshot helper must refresh the parsed config as well as os.Environ
	// before calling create_runtime().
	{
		Name:              "DYN_DISCOVERY_BACKEND",
		Consumer:          restoreRuntimeEnvConsumerParsedConfig,
		DefaultWhenUnset:  restoreRuntimeDefault("etcd"),
		RefreshesConfig:   true,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_REQUEST_PLANE",
		Consumer:          restoreRuntimeEnvConsumerParsedConfig,
		DefaultWhenUnset:  restoreRuntimeDefault("tcp"),
		RefreshesConfig:   true,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_EVENT_PLANE",
		Consumer:          restoreRuntimeEnvConsumerParsedConfig,
		RefreshesConfig:   true,
		SafeForAnnotation: true,
	},

	// Runtime infrastructure env read when DistributedRuntime is created after
	// restore. Only addresses/endpoints belong here; credentials must remain in
	// Secret-backed env or mounted Secret files and are not copied to metadata.
	{
		Name:              "NATS_SERVER",
		Consumer:          restoreRuntimeEnvConsumerRuntime,
		SafeForAnnotation: true,
	},
	{
		Name:              "ETCD_ENDPOINTS",
		Consumer:          restoreRuntimeEnvConsumerRuntime,
		SafeForAnnotation: true,
	},

	// System server/readiness env read by runtime config after restore.
	{
		Name:              "DYN_SYSTEM_PORT",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_HOST",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_HEALTH_PATH",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_LIVE_PATH",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_STARTING_HEALTH_STATUS",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_HEALTH_CHECK_ENABLED",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_ENABLED",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},
	{
		Name:              "DYN_SYSTEM_USE_ENDPOINT_HEALTH_STATUS",
		Consumer:          restoreRuntimeEnvConsumerSystem,
		SafeForAnnotation: true,
	},

	// Kubernetes discovery mode is read when the restored runtime registers.
	{
		Name:              "DYN_KUBE_DISCOVERY_MODE",
		Consumer:          restoreRuntimeEnvConsumerKubernetes,
		SafeForAnnotation: true,
	},
	{
		Name:              "CONTAINER_NAME",
		Consumer:          restoreRuntimeEnvConsumerKubernetes,
		SafeForAnnotation: true,
	},

	// Optional non-secret infra. These are refreshable only for consumers that
	// initialize after restore; they do not retroactively affect model loading.
	{
		Name:              "MODEL_EXPRESS_URL",
		Consumer:          restoreRuntimeEnvConsumerInfra,
		SafeForAnnotation: true,
	},
	{
		Name:              "PROMETHEUS_ENDPOINT",
		Consumer:          restoreRuntimeEnvConsumerInfra,
		SafeForAnnotation: true,
	},
}

func validateRestoreRuntimeEnvSpecs() error {
	seen := map[string]struct{}{}
	for _, spec := range restoreRuntimeEnvSpecs {
		if spec.Name == "" {
			return fmt.Errorf("restore runtime env spec has empty name")
		}
		if _, ok := seen[spec.Name]; ok {
			return fmt.Errorf("duplicate restore runtime env spec %q", spec.Name)
		}
		seen[spec.Name] = struct{}{}
		if !spec.SafeForAnnotation {
			return fmt.Errorf("restore runtime env spec %q is not annotation-safe", spec.Name)
		}
		if spec.RefreshesConfig && spec.Consumer != restoreRuntimeEnvConsumerParsedConfig {
			return fmt.Errorf("restore runtime env spec %q refreshes parsed config but has consumer %q", spec.Name, spec.Consumer)
		}
		if spec.DefaultWhenUnset != nil && !spec.RefreshesConfig {
			return fmt.Errorf("restore runtime env spec %q has a parsed default but does not refresh parsed config", spec.Name)
		}
	}
	return nil
}

type restoreRuntimeConfig struct {
	Env map[string]*string `json:"env"`
}

// ApplyRestoreRuntimeConfigAnnotation records non-secret restore-time runtime
// configuration on the Pod metadata so it can be projected through Downward API.
//
// CRIU restores the checkpoint-time process environment. The restored Dynamo
// process reads this annotation from /etc/podinfo before creating
// DistributedRuntime, allowing it to use the restore Pod's NATS/etcd/discovery
// and system-server configuration instead of stale checkpoint-job values.
func ApplyRestoreRuntimeConfigAnnotation(
	annotations map[string]string,
	container *corev1.Container,
) error {
	if container == nil {
		return nil
	}
	return applyRestoreRuntimeConfigAnnotation(annotations, []*corev1.Container{container})
}

// ApplyRestoreRuntimeConfigAnnotationForTargets records restore-time runtime
// configuration for the restore target containers in the Pod metadata.
func ApplyRestoreRuntimeConfigAnnotationForTargets(
	annotations map[string]string,
	podSpec *corev1.PodSpec,
	targets []string,
) error {
	if podSpec == nil {
		return nil
	}
	if len(targets) == 0 {
		targets = []string{commonconsts.MainContainerName}
	}

	containers := make([]*corev1.Container, 0, len(targets))
	for _, name := range targets {
		var container *corev1.Container
		for i := range podSpec.Containers {
			if podSpec.Containers[i].Name == name {
				container = &podSpec.Containers[i]
				break
			}
		}
		if container == nil {
			return fmt.Errorf("checkpoint restore target %q does not exist in pod spec", name)
		}
		containers = append(containers, container)
	}

	return applyRestoreRuntimeConfigAnnotation(annotations, containers)
}

func applyRestoreRuntimeConfigAnnotation(
	annotations map[string]string,
	containers []*corev1.Container,
) error {
	if annotations == nil || len(containers) == 0 {
		return nil
	}
	if err := validateRestoreRuntimeEnvSpecs(); err != nil {
		return err
	}

	config := restoreRuntimeConfig{Env: make(map[string]*string, len(restoreRuntimeEnvSpecs))}
	for _, spec := range restoreRuntimeEnvSpecs {
		if !spec.SafeForAnnotation {
			continue
		}
		value, ok := commonLiteralEnvValue(containers, spec.Name)
		if ok {
			config.Env[spec.Name] = value
		}
	}

	payload, err := json.Marshal(config)
	if err != nil {
		return err
	}
	annotations[commonconsts.CheckpointRestoreRuntimeConfigAnnotation] = string(payload)
	return nil
}

func commonLiteralEnvValue(containers []*corev1.Container, name string) (*string, bool) {
	var common *string
	initialized := false
	for _, container := range containers {
		value, ok := literalEnvValue(container, name)
		if !ok {
			// Non-literal EnvVarSource values may refer to Secrets or Pod fields.
			// Do not copy them into annotations, and do not clear stale values
			// because the restore-time value is unknown to the operator.
			return nil, false
		}
		if !initialized {
			common = value
			initialized = true
			continue
		}
		if (value == nil) != (common == nil) {
			return nil, false
		}
		if value != nil && *value != *common {
			return nil, false
		}
	}
	return common, true
}

func literalEnvValue(container *corev1.Container, name string) (*string, bool) {
	for _, env := range container.Env {
		if env.Name != name {
			continue
		}
		if env.ValueFrom != nil {
			return nil, false
		}
		value := env.Value
		return &value, true
	}
	return nil, true
}
