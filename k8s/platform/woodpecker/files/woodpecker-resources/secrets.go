package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// generateSecret returns a URL-safe random string of at least n bytes of
// entropy. Both things it mints — HMAC shared secrets and nothing else — end up
// in environment variables and HTTP headers, so the alphabet is deliberately
// narrow.
func generateSecret(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// readSecretKey returns the value at ref, or "" when either the Secret or the
// key is absent. A missing Secret is an ordinary state here — on a cold
// bootstrap this job can run before the consuming namespace exists — so it is
// not an error.
func readSecretKey(ctx context.Context, k8s kubernetes.Interface, ref SecretRef) (string, error) {
	secret, err := k8s.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get secret %s: %w", ref, err)
	}
	return string(secret.Data[ref.Key]), nil
}

// writeSecretKeys creates the Secret or patches the given keys into it.
//
// The patch is additive on purpose: several of the Secrets written here are
// shared with another provisioner (archon-forgejo-user is forgejo-resources'),
// and replacing the object would drop keys this job knows nothing about.
func writeSecretKeys(ctx context.Context, k8s kubernetes.Interface, namespace, name string, data map[string]string) error {
	secrets := k8s.CoreV1().Secrets(namespace)

	_, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			StringData: data,
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}

	patch, err := json.Marshal(map[string]any{"stringData": data})
	if err != nil {
		return fmt.Errorf("marshal patch for secret %s/%s: %w", namespace, name, err)
	}
	if _, err := secrets.Patch(ctx, name, k8sTypes.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch secret %s/%s: %w", namespace, name, err)
	}
	return nil
}
