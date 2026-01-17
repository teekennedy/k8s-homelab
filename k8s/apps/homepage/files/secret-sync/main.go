package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

type Config struct {
	OutputSecret OutputSecret `yaml:"output-secret"`
	Mappings     []Mapping    `yaml:"mappings"`
}

type OutputSecret struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Type        string            `yaml:"type"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

type Mapping struct {
	Env      string    `yaml:"env"`
	Source   SourceRef `yaml:"source"`
	Optional bool      `yaml:"optional"`
}

type SourceRef struct {
	Namespace string `yaml:"namespace"`
	Name      string `yaml:"name"`
	Key       string `yaml:"key"`
}

const defaultConfigPath = "/config/config.yaml"

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = defaultConfigPath
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}

	if err := validateConfig(cfg); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("create in-cluster config: %v", err)
	}

	client, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Fatalf("create k8s client: %v", err)
	}

	ctx := context.Background()
	outputData := make(map[string][]byte)

	for _, mapping := range cfg.Mappings {
		value, ok, err := readSecretKey(ctx, client, mapping)
		if err != nil {
			log.Fatalf("read %s from %s/%s: %v", mapping.Source.Key, mapping.Source.Namespace, mapping.Source.Name, err)
		}
		if !ok {
			continue
		}
		outputData[mapping.Env] = value
	}

	secretClient := client.CoreV1().Secrets(cfg.OutputSecret.Namespace)
	existing, err := secretClient.Get(ctx, cfg.OutputSecret.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Fatalf("output secret %s/%s does not exist", cfg.OutputSecret.Namespace, cfg.OutputSecret.Name)
		}
		log.Fatalf("get secret %s/%s: %v", cfg.OutputSecret.Namespace, cfg.OutputSecret.Name, err)
	}

	updated, err := updateSecret(ctx, secretClient, existing, cfg.OutputSecret, outputData)
	if err != nil {
		log.Fatalf("update secret %s/%s: %v", cfg.OutputSecret.Namespace, cfg.OutputSecret.Name, err)
	}
	if updated {
		log.Printf("updated secret %s/%s", cfg.OutputSecret.Namespace, cfg.OutputSecret.Name)
		return
	}
	log.Printf("secret %s/%s is already up to date", cfg.OutputSecret.Namespace, cfg.OutputSecret.Name)
}

func validateConfig(cfg Config) error {
	if cfg.OutputSecret.Name == "" {
		return errors.New("output-secret.name is required")
	}
	if cfg.OutputSecret.Namespace == "" {
		return errors.New("output-secret.namespace is required")
	}
	if len(cfg.Mappings) == 0 {
		return errors.New("at least one mapping is required")
	}
	for i, mapping := range cfg.Mappings {
		if mapping.Env == "" {
			return fmt.Errorf("mappings[%d].env is required", i)
		}
		if mapping.Source.Namespace == "" || mapping.Source.Name == "" || mapping.Source.Key == "" {
			return fmt.Errorf("mappings[%d].source requires namespace, name, and key", i)
		}
	}
	return nil
}

func readSecretKey(ctx context.Context, client *kubernetes.Clientset, mapping Mapping) ([]byte, bool, error) {
	secret, err := client.CoreV1().Secrets(mapping.Source.Namespace).Get(ctx, mapping.Source.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) && mapping.Optional {
			log.Printf("optional secret %s/%s not found", mapping.Source.Namespace, mapping.Source.Name)
			return nil, false, nil
		}
		return nil, false, err
	}

	value, ok := secret.Data[mapping.Source.Key]
	if !ok {
		if mapping.Optional {
			log.Printf("optional key %s missing from %s/%s", mapping.Source.Key, mapping.Source.Namespace, mapping.Source.Name)
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("missing key %s", mapping.Source.Key)
	}

	return value, true, nil
}

func updateSecret(ctx context.Context, secretClient corev1client.SecretInterface, existing *corev1.Secret, output OutputSecret, data map[string][]byte) (bool, error) {
	updated := existing.DeepCopy()
	updated.Data = data
	if output.Type != "" {
		updated.Type = corev1.SecretType(output.Type)
	}
	if output.Labels != nil {
		updated.Labels = output.Labels
	}
	if output.Annotations != nil {
		updated.Annotations = output.Annotations
	}

	if reflect.DeepEqual(existing.Data, updated.Data) &&
		reflect.DeepEqual(existing.Labels, updated.Labels) &&
		reflect.DeepEqual(existing.Annotations, updated.Annotations) &&
		existing.Type == updated.Type {
		return false, nil
	}

	_, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	return true, err
}
