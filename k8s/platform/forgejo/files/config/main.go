package main

import (
	"bytes"
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
	"time"

	"code.gitea.io/sdk/gitea"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type Team struct {
	Name       string
	Permission string
	Members    []string
}

type Organization struct {
	Name        string
	Description string
	Teams       []Team
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
	Webhook *RepoWebhook `yaml:"webhook"`
}

// RepoWebhook creates a Gogs-payload-compatible Forgejo webhook (the format
// ArgoCD's /api/webhook endpoint natively understands, since ArgoCD has no
// Forgejo-specific handler). The shared secret is generated once and stored
// under SecretKey in the target Secret, which is expected to already exist
// (e.g. argocd-secret) — patched additively so unrelated keys are untouched.
type RepoWebhook struct {
	URL             string `yaml:"url"`
	BranchFilter    string `yaml:"branchFilter"`
	SecretName      string `yaml:"secretName"`
	SecretNamespace string `yaml:"secretNamespace"`
	SecretKey       string `yaml:"secretKey"`
}

type AccessToken struct {
	Name   string
	Scopes []string
	// RepoCredential, if set, also writes the token into an ArgoCD
	// repository-credential Secret (labeled argocd.argoproj.io/secret-type:
	// repository) so ArgoCD can auto-discover it by repo URL.
	RepoCredential *RepoCredential `yaml:"repoCredential"`
}

type RepoCredential struct {
	SecretName      string `yaml:"secretName"`
	SecretNamespace string `yaml:"secretNamespace"`
	URL             string `yaml:"url"`
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

type OAuth2App struct {
	Name            string
	RedirectURIs    []string `yaml:"redirectURIs"`
	SecretName      string   `yaml:"secretName"`
	SecretNamespace string   `yaml:"secretNamespace"`
	// Key names to store the issued credentials under. Consumers read these
	// keys directly as environment variables (Woodpecker, for one, mounts the
	// whole Secret via envFrom), so the names are the consumer's contract, not
	// ours — hence config rather than hardcoded.
	ClientIDKey     string `yaml:"clientIDKey"`
	ClientSecretKey string `yaml:"clientSecretKey"`
}

type OIDCClient struct {
	Name                  string
	ClientID              string   `yaml:"clientID"`
	ClientSecretName      string   `yaml:"clientSecretName"`
	ClientSecretNamespace string   `yaml:"clientSecretNamespace"`
	AutoDiscoverURL       string   `yaml:"autoDiscoverURL"`
	IconURL               string   `yaml:"iconURL"`
	Scopes                []string `yaml:"scopes"`
	GroupClaimName        string   `yaml:"groupClaimName"`
	AdminGroup            string   `yaml:"adminGroup"`
}

// PodExecTarget is where `forgejo admin auth` and any future CLI-only
// reconciliation runs: the running forgejo pod, which has an already-generated
// app.ini and DB connection. Shared across every config entry that needs pod
// exec, rather than repeated per entry.
type PodExecTarget struct {
	Namespace     string
	LabelSelector string `yaml:"labelSelector"`
	// Container name within the matched pod. The Forgejo chart names its main
	// container after the chart, so this is "forgejo".
	Container string `yaml:"container"`
}

type Config struct {
	Organizations []Organization
	Repositories  []Repository
	Users         []User
	Runners       []Runner
	OAuth2Apps    []OAuth2App    `yaml:"oauth2Apps"`
	PodExec       *PodExecTarget `yaml:"podExec"`
	OIDC          *OIDCClient    `yaml:"oidc"`
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

func waitForSecretKey(ctx context.Context, k8sClient *kubernetes.Clientset, namespace, name, key string, timeout time.Duration) (string, error) {
	secretClient := k8sClient.CoreV1().Secrets(namespace)
	deadline := time.Now().Add(timeout)
	for {
		secret, err := secretClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get secret %s in %s: %w", name, namespace, err)
		}
		if err == nil {
			if value, ok := secret.Data[key]; ok && len(value) > 0 {
				return string(value), nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for key %s in secret %s/%s", key, namespace, name)
		}
		log.Printf("Waiting for key %s in secret %s/%s", key, namespace, name)
		time.Sleep(5 * time.Second)
	}
}

func findPodByLabel(ctx context.Context, k8sClient *kubernetes.Clientset, namespace, labelSelector string) (string, error) {
	pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		return "", fmt.Errorf("list pods matching %q in %s: %w", labelSelector, namespace, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no running pods matching %q in %s", labelSelector, namespace)
	}
	return pods.Items[0].Name, nil
}

// execInPod has no REST API equivalent for `forgejo admin auth`, which reads
// and writes auth sources directly against the DB via the running pod's
// already-generated app.ini.
func execInPod(ctx context.Context, k8sConfig *rest.Config, k8sClient *kubernetes.Clientset, namespace, podName, container string, command []string) (stdout, stderr string, err error) {
	req := k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: container,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k8sConfig, http.MethodPost, req.URL())
	if err != nil {
		return "", "", fmt.Errorf("create executor: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
	})
	return stdoutBuf.String(), stderrBuf.String(), err
}

// configureOIDCAuthSource is idempotent by reconciling flags into the source on every
// run (unconditional update-oauth), the same way the rest of this job re-asserts state.
func configureOIDCAuthSource(ctx context.Context, k8sConfig *rest.Config, k8sClient *kubernetes.Clientset, pod *PodExecTarget, oidc *OIDCClient, clientSecret string) error {
	podName, err := findPodByLabel(ctx, k8sClient, pod.Namespace, pod.LabelSelector)
	if err != nil {
		return fmt.Errorf("find forgejo pod: %w", err)
	}

	listOut, listErr, err := execInPod(ctx, k8sConfig, k8sClient, pod.Namespace, podName, pod.Container,
		[]string{"forgejo", "admin", "auth", "list"})
	if err != nil {
		return fmt.Errorf("list auth sources: %w: %s", err, listErr)
	}

	var existingID string
	for _, line := range strings.Split(strings.TrimSpace(listOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "ID" {
			continue
		}
		if fields[1] == oidc.Name {
			existingID = fields[0]
			break
		}
	}

	args := []string{"forgejo", "admin", "auth"}
	if existingID != "" {
		args = append(args, "update-oauth", "--id", existingID)
	} else {
		args = append(args, "add-oauth")
	}
	args = append(args,
		"--name", oidc.Name,
		"--provider", "openidConnect",
		"--key", oidc.ClientID,
		"--secret", clientSecret,
		"--auto-discover-url", oidc.AutoDiscoverURL,
		"--scopes", strings.Join(oidc.Scopes, " "),
	)
	if oidc.GroupClaimName != "" {
		args = append(args, "--group-claim-name", oidc.GroupClaimName)
	}
	if oidc.AdminGroup != "" {
		args = append(args, "--admin-group", oidc.AdminGroup)
	}
	if oidc.IconURL != "" {
		args = append(args, "--icon-url", oidc.IconURL)
	}

	_, applyErr, err := execInPod(ctx, k8sConfig, k8sClient, pod.Namespace, podName, pod.Container, args)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", args[2], err, applyErr)
	}
	return nil
}

func getRunnerRegistrationToken(ctx context.Context, forgejoHost, forgejoUser, forgejoPassword string) (string, error) {
	host := strings.TrimSuffix(forgejoHost, "/")
	endpoint := host + "/api/v1/admin/runners/registration-token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create runner token request: %w", err)
	}
	req.SetBasicAuth(forgejoUser, forgejoPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request runner token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("request runner token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	body, _ := io.ReadAll(resp.Body)
	responseJSON := map[string]string{}
	if err := json.Unmarshal(body, &responseJSON); err != nil {
		return "", fmt.Errorf("parse runner token: %w", err)
	}

	token, ok := responseJSON["token"]
	if !ok {
		return "", fmt.Errorf("runner token missing from response json")
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

func upsertRepoCredentialSecret(ctx context.Context, k8sClient *kubernetes.Clientset, username, token string, target *RepoCredential) error {
	secretClient := k8sClient.CoreV1().Secrets(target.SecretNamespace)
	labels := map[string]string{"argocd.argoproj.io/secret-type": "repository"}
	data := map[string]string{
		"type":     "git",
		"url":      target.URL,
		"username": username,
		"password": token,
	}

	_, err := secretClient.Get(ctx, target.SecretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get secret %s in %s: %w", target.SecretName, target.SecretNamespace, err)
		}
		newSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      target.SecretName,
				Namespace: target.SecretNamespace,
				Labels:    labels,
			},
			StringData: data,
		}
		_, err = secretClient.Create(ctx, &newSecret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret %s in %s: %w", target.SecretName, target.SecretNamespace, err)
		}
		return nil
	}

	patch := map[string]any{
		"metadata":   map[string]any{"labels": labels},
		"stringData": data,
	}
	patchBytes, _ := json.Marshal(patch)

	_, err = secretClient.Patch(ctx, target.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("update secret %s in %s: %w", target.SecretName, target.SecretNamespace, err)
	}
	return nil
}

func upsertRepoWebhook(ctx context.Context, client *gitea.Client, k8sClient *kubernetes.Clientset, owner, repo string, wh *RepoWebhook) error {
	secretClient := k8sClient.CoreV1().Secrets(wh.SecretNamespace)
	secret, err := secretClient.Get(ctx, wh.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get secret %s in %s: %w", wh.SecretName, wh.SecretNamespace, err)
	}

	sharedSecret := string(secret.Data[wh.SecretKey])
	if sharedSecret == "" {
		sharedSecret, err = generatePassword(48)
		if err != nil {
			return fmt.Errorf("generate webhook secret: %w", err)
		}
		patch := map[string]any{"stringData": map[string]string{wh.SecretKey: sharedSecret}}
		patchBytes, _ := json.Marshal(patch)
		_, err = secretClient.Patch(ctx, wh.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("patch secret %s in %s: %w", wh.SecretName, wh.SecretNamespace, err)
		}
	}

	hooks, _, err := client.ListRepoHooks(owner, repo, gitea.ListHooksOptions{ListOptions: gitea.ListOptions{Page: -1}})
	if err != nil {
		return fmt.Errorf("list hooks for %s/%s: %w", owner, repo, err)
	}
	for _, h := range hooks {
		if h.Config["url"] == wh.URL {
			log.Printf("Webhook %s already exists on %s/%s", wh.URL, owner, repo)
			return nil
		}
	}

	_, _, err = client.CreateRepoHook(owner, repo, gitea.CreateHookOption{
		Type: gitea.HookTypeGogs,
		Config: map[string]string{
			"url":          wh.URL,
			"content_type": "json",
			"secret":       sharedSecret,
		},
		Events:       []string{"push"},
		BranchFilter: wh.BranchFilter,
		Active:       true,
	})
	if err != nil {
		return fmt.Errorf("create webhook for %s/%s: %w", owner, repo, err)
	}
	log.Printf("Created webhook %s for %s/%s", wh.URL, owner, repo)
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

	forgejoHost := os.Getenv("FORGEJO_HOST")
	forgejoUser := os.Getenv("FORGEJO_USER")
	forgejoPassword := os.Getenv("FORGEJO_PASSWORD")

	options := []gitea.ClientOption{gitea.SetBasicAuth(forgejoUser, forgejoPassword), gitea.SetContext(ctx)}
	client, err := gitea.NewClient(forgejoHost, options...)
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

	if config.OIDC != nil {
		if config.PodExec == nil {
			log.Print("OIDC client configured but podExec target is missing")
		} else {
			clientSecret, err := waitForSecretKey(ctx, k8sClient, config.OIDC.ClientSecretNamespace, config.OIDC.ClientSecretName, "client-secret", 3*time.Minute)
			if err != nil {
				log.Printf("Get OIDC client secret: %v", err)
			} else if err := configureOIDCAuthSource(ctx, k8sConfig, k8sClient, config.PodExec, config.OIDC, clientSecret); err != nil {
				log.Printf("Configure OIDC auth source %s: %v", config.OIDC.Name, err)
			} else {
				log.Printf("Configured OIDC auth source %s", config.OIDC.Name)
			}
		}
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

		for _, team := range org.Teams {
			existingTeams, _, err := client.ListOrgTeams(org.Name, gitea.ListTeamsOptions{})
			if err != nil {
				log.Printf("List teams for org %s: %v", org.Name, err)
				continue
			}

			var forgejoTeam *gitea.Team
			for _, t := range existingTeams {
				if t.Name == team.Name {
					forgejoTeam = t
					break
				}
			}

			if forgejoTeam == nil {
				perm := gitea.AccessModeOwner
				if team.Permission != "" {
					perm = gitea.AccessMode(team.Permission)
				}
				forgejoTeam, _, err = client.CreateTeam(org.Name, gitea.CreateTeamOption{
					Name:                    team.Name,
					Permission:              perm,
					IncludesAllRepositories: true,
					Units: []gitea.RepoUnitType{
						gitea.RepoUnitCode,
						gitea.RepoUnitIssues,
						gitea.RepoUnitPulls,
					},
				})
				if err != nil {
					log.Printf("Create team %s in org %s: %v", team.Name, org.Name, err)
					continue
				}
				log.Printf("Created team %s in org %s", team.Name, org.Name)
			}

			for _, member := range team.Members {
				_, _, err := client.GetTeamMember(forgejoTeam.ID, member)
				if err != nil {
					_, err = client.AddTeamMember(forgejoTeam.ID, member)
					if err != nil {
						log.Printf("Add member %s to team %s: %v", member, team.Name, err)
					} else {
						log.Printf("Added member %s to team %s", member, team.Name)
					}
				}
			}
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
				continue
			}
		}
		_, err = client.AdminEditUser(user.Name, gitea.EditUserOption{
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
			log.Printf("Successfully updated user %s", user.Name)
		}

		if len(user.AccessTokens) > 0 {
			userOptions := []gitea.ClientOption{gitea.SetBasicAuth(user.Name, password), gitea.SetContext(ctx)}
			userClient, err := gitea.NewClient(forgejoHost, userOptions...)
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
						continue
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

				if token.RepoCredential != nil {
					if err := upsertRepoCredentialSecret(ctx, k8sClient, user.Name, newToken.Token, token.RepoCredential); err != nil {
						log.Printf("Upsert repo credential secret for token %s: %v", token.Name, err)
						continue
					}
					log.Printf("Successfully updated repo credential secret %s", token.RepoCredential.SecretName)
				}
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

		if repo.Webhook != nil {
			if err := upsertRepoWebhook(ctx, client, k8sClient, repo.Owner, repo.Name, repo.Webhook); err != nil {
				log.Printf("Upsert webhook for %s/%s: %v", repo.Owner, repo.Name, err)
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

		token, err := getRunnerRegistrationToken(ctx, forgejoHost, forgejoUser, forgejoPassword)
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

	for _, app := range config.OAuth2Apps {
		log.Printf("Processing OAuth2 app %s", app.Name)

		if app.ClientIDKey == "" || app.ClientSecretKey == "" {
			log.Printf("OAuth2 app %s is missing clientIDKey/clientSecretKey, skipping", app.Name)
			continue
		}

		// Check if secret already has credentials populated
		secretClient := k8sClient.CoreV1().Secrets(app.SecretNamespace)
		secret, err := secretClient.Get(ctx, app.SecretName, metav1.GetOptions{})
		var existingClientID string
		secretPopulated := false
		if err == nil {
			clientID, hasClientID := secret.Data[app.ClientIDKey]
			clientSecret, hasClientSecret := secret.Data[app.ClientSecretKey]
			if hasClientID && len(clientID) > 0 && hasClientSecret && len(clientSecret) > 0 {
				secretPopulated = true
				existingClientID = string(clientID)
			}
		} else if !apierrors.IsNotFound(err) {
			log.Printf("Get OAuth2 app secret %s in %s: %v", app.SecretName, app.SecretNamespace, err)
			continue
		}

		// Check if the OAuth2 app already exists in Forgejo
		existingApps, _, err := client.ListOauth2(gitea.ListOauth2Option{})
		if err != nil {
			log.Printf("List OAuth2 apps: %v", err)
			continue
		}

		// If secret is populated, verify the app exists in Forgejo with a matching clientID
		if secretPopulated {
			appVerified := false
			for _, existing := range existingApps {
				if existing.Name == app.Name && existing.ClientID == existingClientID {
					appVerified = true
					break
				}
			}
			if appVerified {
				log.Printf("OAuth2 app secret %s already populated and verified in Forgejo, skipping", app.SecretName)
				continue
			}
			log.Printf("OAuth2 app secret %s is populated but app not found in Forgejo (clientID mismatch or missing), recreating", app.SecretName)
		}

		for _, existing := range existingApps {
			if existing.Name == app.Name {
				log.Printf("OAuth2 app %s already exists (ID %d), recreating", app.Name, existing.ID)
				_, err = client.DeleteOauth2(existing.ID)
				if err != nil {
					log.Printf("Delete existing OAuth2 app %s: %v", app.Name, err)
				}
				break
			}
		}

		newApp, _, err := client.CreateOauth2(gitea.CreateOauth2Option{
			Name:               app.Name,
			ConfidentialClient: true,
			RedirectURIs:       app.RedirectURIs,
		})
		if err != nil {
			log.Printf("Create OAuth2 app %s: %v", app.Name, err)
			continue
		}

		// Store credentials in K8s secret
		oauthSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      app.SecretName,
				Namespace: app.SecretNamespace,
			},
			StringData: map[string]string{
				app.ClientIDKey:     newApp.ClientID,
				app.ClientSecretKey: newApp.ClientSecret,
			},
		}

		_, createErr := secretClient.Create(ctx, &oauthSecret, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(createErr) {
			b64ClientID := base64.StdEncoding.EncodeToString([]byte(newApp.ClientID))
			b64ClientSecret := base64.StdEncoding.EncodeToString([]byte(newApp.ClientSecret))
			patch := map[string]any{"data": map[string]string{
				app.ClientIDKey:     b64ClientID,
				app.ClientSecretKey: b64ClientSecret,
			}}
			patchBytes, _ := json.Marshal(patch)
			_, createErr = secretClient.Patch(ctx, app.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
		}
		if createErr != nil {
			log.Printf("Store OAuth2 credentials for %s: %v", app.Name, createErr)
			continue
		}
		log.Printf("Created OAuth2 app %s and stored credentials in secret %s/%s", app.Name, app.SecretNamespace, app.SecretName)
	}
}
