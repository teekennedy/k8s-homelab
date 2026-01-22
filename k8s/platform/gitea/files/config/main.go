package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	"code.gitea.io/sdk/gitea"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Organization struct {
	Name        string
	Description string
}

type Repository struct {
	Name        string
	Owner       string
	Description string
	Private     bool
	Migrate     struct {
		Source string
		Mirror bool
	}
}

type AccessToken struct {
	Name   string
	Scopes []string
}

type User struct {
	Name            string
	FullName        string `yaml:"fullName"`
	Email           string
	SecretName      string        `yaml:"secretName"`
	SecretNamespace string        `yaml:"secretNamespace"`
	AccessTokens    []AccessToken `yaml:"accessTokens"`
	Admin           bool          `yaml:"admin"`
}

type Runner struct {
	Name            string
	SecretName      string `yaml:"secretName"`
	SecretNamespace string `yaml:"secretNamespace"`
}

type Config struct {
	Organizations []Organization
	Repositories  []Repository
	Users         []User
	Runners       []Runner
}

const charset = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	"!@#$%^&*()-_=+[]{}<>?,."

func generatePassword(length int) (string, error) {
	bytes := make([]byte, length)
	charsetLen := byte(len(charset))

	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	for i, b := range bytes {
		bytes[i] = charset[b%charsetLen]
	}

	return string(bytes), nil
}

func getOrCreatePassword(ctx context.Context, k8sClient *kubernetes.Clientset, namespace, secretName, username string) (password string, err error) {
	secretClient := k8sClient.CoreV1().Secrets(namespace)
	secret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Get secret %s in %s: %v", secretName, namespace, err)
		password, err = generatePassword(32)
		if err != nil {
			return password, fmt.Errorf("generate password for secret %s: %w", secretName, err)
		}
		newSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
			StringData: map[string]string{"username": username, "password": password},
		}
		_, err = secretClient.Create(ctx, &newSecret, metav1.CreateOptions{})
		if err != nil {
			return password, fmt.Errorf("create secret %s: %w", secretName, err)
		}
	} else {
		log.Print("Found existing secret " + secretName)
		passwordBytes, ok := secret.Data["password"]
		if !ok {
			return password, fmt.Errorf("secret %s in namespace %s is missing password field", secretName, namespace)
		}
		return string(passwordBytes[:]), nil
	}
	return password, err
}

func getRunnerRegistrationToken(ctx context.Context, giteaHost, giteaUser, giteaPassword string) (string, error) {
	host := strings.TrimSuffix(giteaHost, "/")
	endpoint := host + "/api/v1/admin/runners/registration-token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create runner token request: %w", err)
	}
	req.SetBasicAuth(giteaUser, giteaPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request runner token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("request runner token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	token := resp.Header.Get("Token")
	if token == "" {
		token = resp.Header.Get("token")
	}
	if token == "" {
		return "", fmt.Errorf("runner token missing from response headers")
	}
	return token, nil
}

func upsertRunnerTokenSecret(ctx context.Context, k8sClient *kubernetes.Clientset, namespace, secretName, token string) error {
	secretClient := k8sClient.CoreV1().Secrets(namespace)
	_, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get secret %s in %s: %w", secretName, namespace, err)
		}
		newSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
			StringData: map[string]string{"token": token},
		}
		_, err = secretClient.Create(ctx, &newSecret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret %s in %s: %w", secretName, namespace, err)
		}
		return nil
	}

	b64Token := base64.StdEncoding.EncodeToString([]byte(token))
	patch := map[string]any{"data": map[string]string{"token": b64Token}}
	patchBytes, _ := json.Marshal(patch)

	_, err = secretClient.Patch(ctx, secretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("update secret %s in %s: %w", secretName, namespace, err)
	}
	return nil
}

func main() {
	ctx := context.Background()
	data, err := os.ReadFile("./config.yaml")
	if err != nil {
		log.Fatalf("Unable to read config file: %v", err)
	}

	config := Config{}

	err = yaml.Unmarshal([]byte(data), &config)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	giteaHost := os.Getenv("GITEA_HOST")
	giteaUser := os.Getenv("GITEA_USER")
	giteaPassword := os.Getenv("GITEA_PASSWORD")

	options := []gitea.ClientOption{gitea.SetBasicAuth(giteaUser, giteaPassword), gitea.SetContext(ctx)}
	client, err := gitea.NewClient(giteaHost, options...)
	if err != nil {
		log.Fatal(err)
	}

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("create in-cluster config: %v", err)
	}
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Fatalf("create k8s client: %v", err)
	}

	for _, org := range config.Organizations {
		var currOrg *gitea.Organization
		currOrg, _, err = client.GetOrg(org.Name)
		if err != nil {
			_, _, err = client.CreateOrg(gitea.CreateOrgOption{
				Name:        org.Name,
				Description: org.Description,
			})
			if err != nil {
				log.Printf("Create organization %s: %v", org.Name, err)
			}
		} else if currOrg.Description != org.Description {
			_, err = client.EditOrg(org.Name, gitea.EditOrgOption{
				Description: org.Description,
			})
			if err != nil {
				log.Printf("Edit organization %s: %v", org.Name, err)
			}
		}
	}

	for _, repo := range config.Repositories {
		if repo.Migrate.Source != "" {
			_, _, err = client.MigrateRepo(gitea.MigrateRepoOption{
				RepoName:       repo.Name,
				RepoOwner:      repo.Owner,
				CloneAddr:      repo.Migrate.Source,
				Service:        gitea.GitServicePlain,
				Mirror:         repo.Migrate.Mirror,
				Private:        repo.Private,
				MirrorInterval: "10m",
			})
			if err != nil {
				log.Printf("Migrate %s/%s: %v", repo.Owner, repo.Name, err)
			}
		} else {
			_, _, err = client.AdminCreateRepo(repo.Owner, gitea.CreateRepoOption{
				Name:        repo.Name,
				Description: repo.Description,
				Private:     repo.Private,
			})
			if err != nil {
				log.Printf("Create %s/%s: %v", repo.Owner, repo.Name, err)
			}
		}

		for _, user := range config.Users {
			log.Printf("Processing user %s with secret %s in %s", user.Name, user.SecretName, user.SecretNamespace)
			password, err := getOrCreatePassword(ctx, k8sClient, user.SecretNamespace, user.SecretName, user.Name)
			if err != nil {
				log.Printf("getOrCreatePassword for user %s: %v", user.Name, err)
			}
			mustChangePassword := false
			_, _, err = client.GetUserInfo(user.Name)
			if err != nil {
				_, _, err = client.AdminCreateUser(gitea.CreateUserOption{
					Username:           user.Name,
					LoginName:          user.Name,
					FullName:           user.FullName,
					Password:           password,
					MustChangePassword: &mustChangePassword,
					Email:              user.Email,
				})
				if err != nil {
					log.Printf("Create %s: %v", user.Name, err)
				}
			} else {
				_, err := client.AdminEditUser(user.Name, gitea.EditUserOption{
					LoginName:          user.Name,
					Email:              &user.Email,
					FullName:           &user.FullName,
					Password:           password,
					Admin:              &user.Admin,
					MustChangePassword: &mustChangePassword,
				})
				if err != nil {
					log.Printf("Edit %s: %v", user.Name, err)
				} else {
					log.Printf("Successfully updated user")
				}
			}

			if len(user.AccessTokens) > 0 {
				userOptions := []gitea.ClientOption{gitea.SetBasicAuth(user.Name, password), gitea.SetContext(ctx)}
				userClient, err := gitea.NewClient(giteaHost, userOptions...)
				if err != nil {
					log.Printf("Logging in as %s: %v", user.Name, err)
					continue
				}
				currTokenList, _, err := userClient.ListAccessTokens(gitea.ListAccessTokensOptions{ListOptions: gitea.ListOptions{Page: -1}})
				if err != nil {
					log.Printf("Listing current access tokens for %s: %v", user.Name, err)
					continue
				}
				currTokens := make(map[string]*gitea.AccessToken)
				for _, currToken := range currTokenList {
					currTokens[currToken.Name] = currToken
				}
				for _, token := range user.AccessTokens {
					var scopes []gitea.AccessTokenScope
					for _, s := range token.Scopes {
						scopes = append(scopes, gitea.AccessTokenScope(s))
					}
					slices.Sort(scopes)

					currToken, ok := currTokens[token.Name]
					if ok {
						slices.Sort(currToken.Scopes)

						if slices.Equal(scopes, currToken.Scopes) {
							log.Printf("Existing access token %s matches expected scopes", currToken.Name)
						} else {
							log.Printf("Existing access token %s differs from expected scopes. Deleting", currToken.Name)
							_, err = userClient.DeleteAccessToken(currToken.ID)
							if err != nil {
								log.Printf("Deleting %s: %v", currToken.Name, err)
								continue
							}
						}

					}

					log.Printf("Creating token %s for user %s", token.Name, user.Name)
					newToken, _, err := userClient.CreateAccessToken(gitea.CreateAccessTokenOption{
						Name:   token.Name,
						Scopes: scopes,
					})
					if err != nil {
						log.Printf("Creating %s: %v", token.Name, err)
						continue
					}
					secretClient := k8sClient.CoreV1().Secrets(user.SecretNamespace)
					b64Token := base64.URLEncoding.EncodeToString([]byte(newToken.Token))

					patch := map[string]any{"data": map[string]string{"token": b64Token}}
					patchBytes, _ := json.Marshal(patch)

					_, err = secretClient.Patch(ctx, user.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
					if err != nil {
						log.Printf("Updating secret %s for token %s: %v", user.SecretName, token.Name, err)
						continue
					}
					log.Printf("Successfully created token %s and updated secret %s", token.Name, user.SecretName)
				}

			}
		}
	}

	for _, runner := range config.Runners {
		secretClient := k8sClient.CoreV1().Secrets(runner.SecretNamespace)
		secret, err := secretClient.Get(ctx, runner.SecretName, metav1.GetOptions{})
		if err == nil {
			if token, ok := secret.Data["token"]; ok && len(token) > 0 {
				log.Printf("Runner token secret %s already populated, skipping", runner.SecretName)
				continue
			}
		} else if !apierrors.IsNotFound(err) {
			log.Printf("Get runner secret %s in %s: %v", runner.SecretName, runner.SecretNamespace, err)
			continue
		}

		token, err := getRunnerRegistrationToken(ctx, giteaHost, giteaUser, giteaPassword)
		if err != nil {
			log.Printf("Get runner registration token for %s: %v", runner.Name, err)
			continue
		}
		if err := upsertRunnerTokenSecret(ctx, k8sClient, runner.SecretNamespace, runner.SecretName, token); err != nil {
			log.Printf("Upsert runner token secret %s in %s: %v", runner.SecretName, runner.SecretNamespace, err)
			continue
		}
		log.Printf("Updated runner token secret %s for %s", runner.SecretName, runner.Name)
	}
}
