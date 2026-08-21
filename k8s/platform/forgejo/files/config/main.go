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
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	for i, b := range bytes {
		bytes[i] = charset[b%charsetLen]
	}

	return string(bytes), nil
}

func getOrCreatePassword(ctx context.Context, k8sClient *kubernetes.Clientset, namespace, secretName, username string) (string, error) {
	secretClient := k8sClient.CoreV1().Secrets(namespace)
	secret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Get secret %s in %s: %v", secretName, namespace, err)
		password, err := generatePassword(32)
		if err != nil {
			return "", fmt.Errorf("generate password for secret %s: %w", secretName, err)
		}
		newSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
			},
			StringData: map[string]string{"username": username, "password": password},
		}
		if _, err := secretClient.Create(ctx, &newSecret, metav1.CreateOptions{}); err != nil {
			return "", fmt.Errorf("create secret %s: %w", secretName, err)
		}
		return password, nil
	}

	log.Print("Found existing secret " + secretName)
	passwordBytes, ok := secret.Data["password"]
	if !ok {
		return "", fmt.Errorf("secret %s in namespace %s is missing password field", secretName, namespace)
	}
	return string(passwordBytes), nil
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

	args := buildOIDCAuthArgs(oidc, clientSecret, existingID)

	_, applyErr, err := execInPod(ctx, k8sConfig, k8sClient, pod.Namespace, podName, pod.Container, args)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", args[2], err, applyErr)
	}
	return nil
}

// buildOIDCAuthArgs builds the `forgejo admin auth` CLI args to add or update
// the OIDC auth source, split out from configureOIDCAuthSource to keep its
// cyclomatic complexity down.
func buildOIDCAuthArgs(oidc *OIDCClient, clientSecret, existingID string) []string {
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
	return args
}

func getRunnerRegistrationToken(ctx context.Context, forgejoHost, forgejoUser, forgejoPassword string) (string, error) {
	host := strings.TrimSuffix(forgejoHost, "/")
	endpoint := host + "/api/v1/admin/runners/registration-token"
	// forgejoHost is trusted operator-supplied config (chart values/env), not user input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) //nolint:gosec // trusted config-derived host, not attacker-controlled
	if err != nil {
		return "", fmt.Errorf("create runner token request: %w", err)
	}
	req.SetBasicAuth(forgejoUser, forgejoPassword)

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // trusted config-derived host, not attacker-controlled
	if err != nil {
		return "", fmt.Errorf("request runner token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	if err := yaml.Unmarshal(data, &config); err != nil {
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

	syncOIDC(ctx, k8sConfig, k8sClient, config.PodExec, config.OIDC)
	syncOrganizations(client, config.Organizations)
	syncUsers(ctx, k8sClient, client, forgejoHost, config.Users)
	syncRepositories(ctx, client, k8sClient, config.Repositories)
	syncRunners(ctx, k8sClient, forgejoHost, forgejoUser, forgejoPassword, config.Runners)
	syncOAuth2Apps(ctx, client, k8sClient, config.OAuth2Apps)
}

// syncOIDC configures the OIDC auth source, if configured.
func syncOIDC(ctx context.Context, k8sConfig *rest.Config, k8sClient *kubernetes.Clientset, pod *PodExecTarget, oidc *OIDCClient) {
	if oidc == nil {
		return
	}
	if pod == nil {
		log.Print("OIDC client configured but podExec target is missing")
		return
	}
	clientSecret, err := waitForSecretKey(ctx, k8sClient, oidc.ClientSecretNamespace, oidc.ClientSecretName, "client-secret", 3*time.Minute)
	if err != nil {
		log.Printf("Get OIDC client secret: %v", err)
		return
	}
	if err := configureOIDCAuthSource(ctx, k8sConfig, k8sClient, pod, oidc, clientSecret); err != nil {
		log.Printf("Configure OIDC auth source %s: %v", oidc.Name, err)
		return
	}
	log.Printf("Configured OIDC auth source %s", oidc.Name)
}

func syncOrganizations(client *gitea.Client, orgs []Organization) {
	for _, org := range orgs {
		syncOrganization(client, org)
	}
}

func syncOrganization(client *gitea.Client, org Organization) {
	currOrg, _, err := client.GetOrg(org.Name)
	if err != nil {
		if _, _, err := client.CreateOrg(gitea.CreateOrgOption{
			Name:        org.Name,
			Description: org.Description,
		}); err != nil {
			log.Printf("Create organization %s: %v", org.Name, err)
		}
	} else if currOrg.Description != org.Description {
		if _, err := client.EditOrg(org.Name, gitea.EditOrgOption{
			Description: org.Description,
		}); err != nil {
			log.Printf("Edit organization %s: %v", org.Name, err)
		}
	}

	for _, team := range org.Teams {
		syncOrgTeam(client, org.Name, team)
	}
}

func syncOrgTeam(client *gitea.Client, orgName string, team Team) {
	existingTeams, _, err := client.ListOrgTeams(orgName, gitea.ListTeamsOptions{})
	if err != nil {
		log.Printf("List teams for org %s: %v", orgName, err)
		return
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
		var err error
		forgejoTeam, _, err = client.CreateTeam(orgName, gitea.CreateTeamOption{
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
			log.Printf("Create team %s in org %s: %v", team.Name, orgName, err)
			return
		}
		log.Printf("Created team %s in org %s", team.Name, orgName)
	}

	for _, member := range team.Members {
		syncTeamMember(client, forgejoTeam, member)
	}
}

// member and team.Name are operator-supplied config (chart values), not attacker input.
func syncTeamMember(client *gitea.Client, team *gitea.Team, member string) {
	if _, _, err := client.GetTeamMember(team.ID, member); err == nil {
		return
	}
	if _, err := client.AddTeamMember(team.ID, member); err != nil {
		log.Printf("Add member %s to team %s: %v", member, team.Name, err) //nolint:gosec // trusted config-derived values, not attacker-controlled
		return
	}
	log.Printf("Added member %s to team %s", member, team.Name) //nolint:gosec // trusted config-derived values, not attacker-controlled
}

func syncUsers(ctx context.Context, k8sClient *kubernetes.Clientset, client *gitea.Client, forgejoHost string, users []User) {
	for _, user := range users {
		syncUser(ctx, k8sClient, client, forgejoHost, user)
	}
}

func syncUser(ctx context.Context, k8sClient *kubernetes.Clientset, client *gitea.Client, forgejoHost string, user User) {
	log.Printf("Processing user %s with secret %s in %s", user.Name, user.SecretName, user.SecretNamespace)
	password, err := getOrCreatePassword(ctx, k8sClient, user.SecretNamespace, user.SecretName, user.Name)
	if err != nil {
		log.Printf("getOrCreatePassword for user %s: %v", user.Name, err)
	}
	mustChangePassword := false
	if _, _, err := client.GetUserInfo(user.Name); err != nil {
		if _, _, err := client.AdminCreateUser(gitea.CreateUserOption{
			Username:           user.Name,
			LoginName:          user.Name,
			FullName:           user.FullName,
			Password:           password,
			MustChangePassword: &mustChangePassword,
			Email:              user.Email,
		}); err != nil {
			log.Printf("Create %s: %v", user.Name, err)
			return
		}
	}
	if _, err := client.AdminEditUser(user.Name, gitea.EditUserOption{
		LoginName:          user.Name,
		Email:              &user.Email,
		FullName:           &user.FullName,
		Password:           password,
		Admin:              &user.Admin,
		MustChangePassword: &mustChangePassword,
	}); err != nil {
		log.Printf("Edit %s: %v", user.Name, err)
	} else {
		log.Printf("Successfully updated user %s", user.Name)
	}

	if len(user.AccessTokens) > 0 {
		syncUserAccessTokens(ctx, k8sClient, forgejoHost, user, password)
	}
}

func syncUserAccessTokens(ctx context.Context, k8sClient *kubernetes.Clientset, forgejoHost string, user User, password string) {
	userOptions := []gitea.ClientOption{gitea.SetBasicAuth(user.Name, password), gitea.SetContext(ctx)}
	userClient, err := gitea.NewClient(forgejoHost, userOptions...)
	if err != nil {
		log.Printf("Logging in as %s: %v", user.Name, err)
		return
	}
	currTokenList, _, err := userClient.ListAccessTokens(gitea.ListAccessTokensOptions{ListOptions: gitea.ListOptions{Page: -1}})
	if err != nil {
		log.Printf("Listing current access tokens for %s: %v", user.Name, err)
		return
	}
	currTokens := make(map[string]*gitea.AccessToken)
	for _, currToken := range currTokenList {
		currTokens[currToken.Name] = currToken
	}
	for _, token := range user.AccessTokens {
		syncAccessToken(ctx, k8sClient, userClient, user, token, currTokens)
	}
}

func syncAccessToken(ctx context.Context, k8sClient *kubernetes.Clientset, userClient *gitea.Client, user User, token AccessToken, currTokens map[string]*gitea.AccessToken) {
	var scopes []gitea.AccessTokenScope
	for _, s := range token.Scopes {
		scopes = append(scopes, gitea.AccessTokenScope(s))
	}
	slices.Sort(scopes)

	if currToken, ok := currTokens[token.Name]; ok {
		slices.Sort(currToken.Scopes)
		if slices.Equal(scopes, currToken.Scopes) {
			log.Printf("Existing access token %s matches expected scopes", currToken.Name)
			return
		}
		log.Printf("Existing access token %s differs from expected scopes. Deleting", currToken.Name)
		if _, err := userClient.DeleteAccessToken(currToken.ID); err != nil {
			log.Printf("Deleting %s: %v", currToken.Name, err)
			return
		}
	}

	log.Printf("Creating token %s for user %s", token.Name, user.Name)
	newToken, _, err := userClient.CreateAccessToken(gitea.CreateAccessTokenOption{
		Name:   token.Name,
		Scopes: scopes,
	})
	if err != nil {
		log.Printf("Creating %s: %v", token.Name, err)
		return
	}
	secretClient := k8sClient.CoreV1().Secrets(user.SecretNamespace)
	b64Token := base64.URLEncoding.EncodeToString([]byte(newToken.Token))
	patch := map[string]any{"data": map[string]string{"token": b64Token}}
	patchBytes, _ := json.Marshal(patch)

	if _, err := secretClient.Patch(ctx, user.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		log.Printf("Updating secret %s for token %s: %v", user.SecretName, token.Name, err)
		return
	}
	log.Printf("Successfully created token %s and updated secret %s", token.Name, user.SecretName)

	if token.RepoCredential != nil {
		if err := upsertRepoCredentialSecret(ctx, k8sClient, user.Name, newToken.Token, token.RepoCredential); err != nil {
			log.Printf("Upsert repo credential secret for token %s: %v", token.Name, err)
			return
		}
		log.Printf("Successfully updated repo credential secret %s", token.RepoCredential.SecretName)
	}
}

func syncRepositories(ctx context.Context, client *gitea.Client, k8sClient *kubernetes.Clientset, repos []Repository) {
	for _, repo := range repos {
		syncRepository(ctx, client, k8sClient, repo)
	}
}

func syncRepository(ctx context.Context, client *gitea.Client, k8sClient *kubernetes.Clientset, repo Repository) {
	if repo.Migrate.Source != "" {
		if _, _, err := client.MigrateRepo(gitea.MigrateRepoOption{
			RepoName:       repo.Name,
			RepoOwner:      repo.Owner,
			CloneAddr:      repo.Migrate.Source,
			Service:        gitea.GitServicePlain,
			Mirror:         repo.Migrate.Mirror,
			Private:        repo.Private,
			MirrorInterval: "10m",
		}); err != nil {
			log.Printf("Migrate %s/%s: %v", repo.Owner, repo.Name, err)
		}
	} else if _, _, err := client.AdminCreateRepo(repo.Owner, gitea.CreateRepoOption{
		Name:        repo.Name,
		Description: repo.Description,
		Private:     repo.Private,
	}); err != nil {
		log.Printf("Create %s/%s: %v", repo.Owner, repo.Name, err)
	}

	if repo.Webhook != nil {
		if err := upsertRepoWebhook(ctx, client, k8sClient, repo.Owner, repo.Name, repo.Webhook); err != nil {
			log.Printf("Upsert webhook for %s/%s: %v", repo.Owner, repo.Name, err)
		}
	}
}

func syncRunners(ctx context.Context, k8sClient *kubernetes.Clientset, forgejoHost, forgejoUser, forgejoPassword string, runners []Runner) {
	for _, runner := range runners {
		syncRunner(ctx, k8sClient, forgejoHost, forgejoUser, forgejoPassword, runner)
	}
}

func syncRunner(ctx context.Context, k8sClient *kubernetes.Clientset, forgejoHost, forgejoUser, forgejoPassword string, runner Runner) {
	secretClient := k8sClient.CoreV1().Secrets(runner.SecretNamespace)
	secret, err := secretClient.Get(ctx, runner.SecretName, metav1.GetOptions{})
	if err == nil {
		if token, ok := secret.Data["token"]; ok && len(token) > 0 {
			log.Printf("Runner token secret %s already populated, skipping", runner.SecretName)
			return
		}
	} else if !apierrors.IsNotFound(err) {
		log.Printf("Get runner secret %s in %s: %v", runner.SecretName, runner.SecretNamespace, err)
		return
	}

	token, err := getRunnerRegistrationToken(ctx, forgejoHost, forgejoUser, forgejoPassword)
	if err != nil {
		log.Printf("Get runner registration token for %s: %v", runner.Name, err)
		return
	}
	if err := upsertRunnerTokenSecret(ctx, k8sClient, runner.SecretNamespace, runner.SecretName, token); err != nil {
		log.Printf("Upsert runner token secret %s in %s: %v", runner.SecretName, runner.SecretNamespace, err)
		return
	}
	log.Printf("Updated runner token secret %s for %s", runner.SecretName, runner.Name)
}

func syncOAuth2Apps(ctx context.Context, client *gitea.Client, k8sClient *kubernetes.Clientset, apps []OAuth2App) {
	for _, app := range apps {
		syncOAuth2App(ctx, client, k8sClient, app)
	}
}

func syncOAuth2App(ctx context.Context, client *gitea.Client, k8sClient *kubernetes.Clientset, app OAuth2App) {
	log.Printf("Processing OAuth2 app %s", app.Name)

	if app.ClientIDKey == "" || app.ClientSecretKey == "" {
		log.Printf("OAuth2 app %s is missing clientIDKey/clientSecretKey, skipping", app.Name)
		return
	}

	// Check if secret already has credentials populated
	secretPopulated, existingClientID, err := oauth2SecretPopulated(ctx, k8sClient, app)
	if err != nil {
		log.Printf("Get OAuth2 app secret %s in %s: %v", app.SecretName, app.SecretNamespace, err)
		return
	}

	// Check if the OAuth2 app already exists in Forgejo
	existingApps, _, err := client.ListOauth2(gitea.ListOauth2Option{})
	if err != nil {
		log.Printf("List OAuth2 apps: %v", err)
		return
	}

	// If secret is populated, verify the app exists in Forgejo with a matching clientID
	if secretPopulated {
		if oauth2AppVerified(existingApps, app.Name, existingClientID) {
			log.Printf("OAuth2 app secret %s already populated and verified in Forgejo, skipping", app.SecretName)
			return
		}
		log.Printf("OAuth2 app secret %s is populated but app not found in Forgejo (clientID mismatch or missing), recreating", app.SecretName)
	}

	deleteExistingOAuth2App(client, existingApps, app.Name)

	newApp, _, err := client.CreateOauth2(gitea.CreateOauth2Option{
		Name:               app.Name,
		ConfidentialClient: true,
		RedirectURIs:       app.RedirectURIs,
	})
	if err != nil {
		log.Printf("Create OAuth2 app %s: %v", app.Name, err)
		return
	}

	if err := storeOAuth2Credentials(ctx, k8sClient, app, newApp); err != nil {
		log.Printf("Store OAuth2 credentials for %s: %v", app.Name, err)
		return
	}
	log.Printf("Created OAuth2 app %s and stored credentials in secret %s/%s", app.Name, app.SecretNamespace, app.SecretName)
}

// oauth2SecretPopulated reports whether app's K8s secret already holds a
// non-empty client ID and secret, returning the client ID for verification.
func oauth2SecretPopulated(ctx context.Context, k8sClient *kubernetes.Clientset, app OAuth2App) (bool, string, error) {
	secretClient := k8sClient.CoreV1().Secrets(app.SecretNamespace)
	secret, err := secretClient.Get(ctx, app.SecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("get secret %s in %s: %w", app.SecretName, app.SecretNamespace, err)
	}

	clientID, hasClientID := secret.Data[app.ClientIDKey]
	clientSecret, hasClientSecret := secret.Data[app.ClientSecretKey]
	if hasClientID && len(clientID) > 0 && hasClientSecret && len(clientSecret) > 0 {
		return true, string(clientID), nil
	}
	return false, "", nil
}

func oauth2AppVerified(existingApps []*gitea.Oauth2, name, clientID string) bool {
	for _, existing := range existingApps {
		if existing.Name == name && existing.ClientID == clientID {
			return true
		}
	}
	return false
}

func deleteExistingOAuth2App(client *gitea.Client, existingApps []*gitea.Oauth2, name string) {
	for _, existing := range existingApps {
		if existing.Name != name {
			continue
		}
		// name is operator-supplied config (chart values), not attacker input.
		log.Printf("OAuth2 app %s already exists (ID %d), recreating", name, existing.ID) //nolint:gosec // trusted config-derived name, not attacker-controlled
		if _, err := client.DeleteOauth2(existing.ID); err != nil {
			log.Printf("Delete existing OAuth2 app %s: %v", name, err)
		}
		break
	}
}

// storeOAuth2Credentials stores newly issued OAuth2 credentials in a K8s secret,
// creating it if absent or patching it in place if it already exists.
func storeOAuth2Credentials(ctx context.Context, k8sClient *kubernetes.Clientset, app OAuth2App, newApp *gitea.Oauth2) error {
	secretClient := k8sClient.CoreV1().Secrets(app.SecretNamespace)
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

	_, err := secretClient.Create(ctx, &oauthSecret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		b64ClientID := base64.StdEncoding.EncodeToString([]byte(newApp.ClientID))
		b64ClientSecret := base64.StdEncoding.EncodeToString([]byte(newApp.ClientSecret))
		patch := map[string]any{"data": map[string]string{
			app.ClientIDKey:     b64ClientID,
			app.ClientSecretKey: b64ClientSecret,
		}}
		patchBytes, marshalErr := json.Marshal(patch)
		if marshalErr != nil {
			return fmt.Errorf("marshal oauth2 credentials patch: %w", marshalErr)
		}
		_, err = secretClient.Patch(ctx, app.SecretName, k8sTypes.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	}
	if err != nil {
		return fmt.Errorf("store oauth2 credentials: %w", err)
	}
	return nil
}
