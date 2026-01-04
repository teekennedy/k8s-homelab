package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices"

	"code.gitea.io/sdk/gitea"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
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

type Config struct {
	Organizations []Organization
	Repositories  []Repository
	Users         []User
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
}
