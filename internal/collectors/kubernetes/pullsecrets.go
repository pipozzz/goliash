// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/registry"
)

// pullSecretTTL is how long pull secrets are reused before they are read again.
const pullSecretTTL = 10 * time.Minute

var _ collectors.KeychainSource = (*Collector)(nil)

type pullSecretCache struct {
	mu       sync.Mutex
	keychain registry.Keychain
	at       time.Time
}

// ReadPullSecrets reports whether GOLIASH_READ_PULL_SECRETS allows reading the image
// pull secrets that running pods reference. It needs get on secrets, which the Helm
// chart grants only with rbac.readPullSecrets.
func ReadPullSecrets() bool {
	on, _ := strconv.ParseBool(os.Getenv("GOLIASH_READ_PULL_SECRETS"))
	return on
}

// RegistryKeychain returns the registry credentials of the image pull secrets that
// running pods in the selected namespaces reference. Only those secrets are read, and
// only when allowed; the secrets never leave the agent.
func (c *Collector) RegistryKeychain(ctx context.Context) (registry.Keychain, error) {
	if !c.readSecrets {
		return registry.Keychain{}, nil
	}
	c.secrets.mu.Lock()
	defer c.secrets.mu.Unlock()
	if c.secrets.keychain != nil && time.Since(c.secrets.at) < pullSecretTTL {
		return c.secrets.keychain, nil
	}
	pods, err := c.factory.Core().V1().Pods().Lister().List(labels.Everything())
	if err != nil {
		return nil, err
	}
	refs := map[[2]string]bool{}
	for _, p := range pods {
		if !c.selected(p.Namespace) || p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, s := range p.Spec.ImagePullSecrets {
			if s.Name != "" {
				refs[[2]string{p.Namespace, s.Name}] = true
			}
		}
	}
	ordered := make([][2]string, 0, len(refs))
	for r := range refs {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i][0]+"/"+ordered[i][1] < ordered[j][0]+"/"+ordered[j][1]
	})

	k := registry.Keychain{}
	var firstErr error
	for _, r := range ordered {
		s, err := c.cs.CoreV1().Secrets(r[0]).Get(ctx, r[1], metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case apierrors.IsForbidden(err):
			return nil, fmt.Errorf("reading image pull secret %s/%s is forbidden: grant get on secrets (Helm: rbac.readPullSecrets=true)", r[0], r[1])
		case err != nil:
			if firstErr == nil {
				firstErr = fmt.Errorf("image pull secret %s/%s: %w", r[0], r[1], err)
			}
			continue
		}
		var data []byte
		switch s.Type {
		case corev1.SecretTypeDockerConfigJson:
			data = s.Data[corev1.DockerConfigJsonKey]
		case corev1.SecretTypeDockercfg:
			data = s.Data[corev1.DockerConfigKey]
		default:
			continue
		}
		found, err := registry.ParseDockerConfig(data)
		if err != nil {
			continue // a malformed secret would not pull images either
		}
		k.Merge(found)
	}
	if firstErr != nil && len(k) == 0 {
		return nil, firstErr
	}
	c.secrets.keychain, c.secrets.at = k, time.Now()
	return k, nil
}
