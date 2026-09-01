package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// githubClient is the small GitHub surface the onboarding endpoint needs. It is
// an interface so tests can inject a fake and exercise the handler without any
// network access.
type githubClient interface {
	// repoInfo returns the repo's default branch. Any error means the app
	// cannot see the repo (not installed, renamed, or insufficient access).
	repoInfo(ctx context.Context, owner, repo string) (defaultBranch string, err error)

	// hasFile reports whether a file exists at path on the given ref (branch).
	hasFile(ctx context.Context, owner, repo, ref, path string) (bool, error)

	// setSecret creates or updates a repo-level Actions secret. Secrets are
	// encrypted client-side with the repo's public key (libsodium sealed box).
	setSecret(ctx context.Context, owner, repo, name, value string) error

	// listRepos returns every repository the app's installation can see
	// (full_name + name), sorted by full name. Powers the dashboard's
	// repository dropdown.
	listRepos(ctx context.Context) ([]githubRepo, error)

	// diagnostics returns what GitHub actually granted this installation:
	// install ID, account, repo selection, and the live permissions map.
	// Used to debug "Resource not accessible by integration" 403s.
	diagnostics(ctx context.Context) (map[string]any, error)

	// openWorkflowPR creates a branch from the base branch, commits the given
	// deploy.yml content to it, and opens a PR. It NEVER merges — the PR is
	// left open for human review.
	openWorkflowPR(ctx context.Context, owner, repo string, p workflowPRParams) (workflowPRResult, error)
}

// githubRepo is the minimal repository info the dashboard needs.
type githubRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

// workflowPRParams is everything needed to open the onboarding PR.
type workflowPRParams struct {
	Service    string // used to name the branch: pipeline/onboard-<service>
	BaseBranch string
	Content    string // rendered deploy.yml
	Title      string
	Body       string
}

// workflowPRResult describes the opened PR.
type workflowPRResult struct {
	Number int
	URL    string
	Branch string
}

// githubAppClient is the real implementation backed by the GitHub REST API
// using a GitHub App's credentials (app ID + private key) to mint installation
// tokens. The agent only ever acts on the app's behalf, never with a user PAT.
type githubAppClient struct {
	appID          int64
	key            *rsa.PrivateKey
	installationID int64 // 0 = resolve automatically via pipelineOwner
	pipelineOwner  string
	apiURL         string
	http           *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// newGithubAppClient builds the client from config. It returns (nil, nil) when
// onboarding is not configured (endpoint stays disabled), and an error when it
// is partially configured (e.g. App ID but no key).
func newGithubAppClient(cfg *Config) (*githubAppClient, error) {
	if cfg.GithubAppID == 0 && cfg.GithubAppPrivateKeyB64 == "" {
		return nil, nil
	}
	if cfg.GithubAppID == 0 || cfg.GithubAppPrivateKeyB64 == "" {
		return nil, errors.New("GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_B64 must both be set to enable onboarding")
	}
	pemBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.GithubAppPrivateKeyB64))
	if err != nil {
		return nil, fmt.Errorf("decode GITHUB_APP_PRIVATE_KEY_B64: %w", err)
	}
	key, err := parseRSAPrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	return &githubAppClient{
		appID:          cfg.GithubAppID,
		key:            key,
		installationID: cfg.GithubAppInstallationID,
		pipelineOwner:  cfg.PipelineOwner,
		apiURL:         strings.TrimRight(envOr("GITHUB_API_URL", "https://api.github.com"), "/"),
		http:           &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := k.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("PKCS8 key is not RSA (GitHub App keys must be RSA)")
	}
	return nil, errors.New("unsupported private key format (expected PKCS1 or PKCS8 RSA)")
}

// ---- auth ----

// appJWT builds a short-lived RS256 JWT for the app itself, used only to mint
// installation tokens.
func (c *githubAppClient) appJWT(now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-60*time.Second).Unix(), now.Add(10*time.Minute).Unix(), c.appID,
	)))
	signing := header + "." + payload
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// tokenForInstallation returns a cached (or freshly minted) installation access
// token. The token is the credential used for every repo-scoped API call.
func (c *githubAppClient) tokenForInstallation(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiresAt.Add(-60*time.Second)) {
		return c.token, nil
	}

	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return "", fmt.Errorf("mint app jwt: %w", err)
	}
	instID := c.installationID
	resolvedVia := "configured"
	if instID == 0 {
		resolvedVia = "auto"
		instID, err = c.resolveInstallationID(ctx, jwt)
		if err != nil {
			return "", err
		}
	}

	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", instID), jwt, nil, &out); err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	logEvent("onboard.install", map[string]any{"install_id": instID, "resolved": resolvedVia})
	c.token = out.Token
	if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		c.expiresAt = t
	}
	return c.token, nil
}

// appInstallation is the minimal shape of an app installation from
// GET /app/installations (authenticated with the app JWT).
type appInstallation struct {
	ID                  int64  `json:"id"`
	RepositorySelection string `json:"repository_selection"`
	Account             struct {
		Login string `json:"login"`
	} `json:"account"`
}

// listInstallations lists the app's installations using the app JWT (works
// even when an installation token is stale/orphaned).
func (c *githubAppClient) listInstallations(ctx context.Context, jwt string) ([]appInstallation, error) {
	var insts []appInstallation
	if _, err := c.doJSON(ctx, http.MethodGet, "/app/installations", jwt, nil, &insts); err != nil {
		return nil, err
	}
	return insts, nil
}

func (c *githubAppClient) resolveInstallationID(ctx context.Context, jwt string) (int64, error) {
	insts, err := c.listInstallations(ctx, jwt)
	if err != nil {
		return 0, err
	}
	if len(insts) == 0 {
		return 0, errors.New("GitHub App has no installations — install it on your account first")
	}
	if c.pipelineOwner != "" {
		for _, i := range insts {
			if strings.EqualFold(i.Account.Login, c.pipelineOwner) {
				return i.ID, nil
			}
		}
	}
	if len(insts) == 1 {
		return insts[0].ID, nil
	}
	return 0, fmt.Errorf(
		"GitHub App has %d installations; set GITHUB_APP_INSTALLATION_ID (none matched PIPELINE_OWNER=%q)",
		len(insts), c.pipelineOwner,
	)
}

// ---- GitHub API operations ----

func (c *githubAppClient) repoInfo(ctx context.Context, owner, repo string) (string, error) {
	tok, err := c.tokenForInstallation(ctx)
	if err != nil {
		return "", err
	}
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/repos/"+owner+"/"+repo, tok, nil, &out); err != nil {
		return "", fmt.Errorf("fetch repo %s/%s: %w", owner, repo, err)
	}
	if out.DefaultBranch == "" {
		return "", errors.New("repo has no default branch")
	}
	return out.DefaultBranch, nil
}

func (c *githubAppClient) hasFile(ctx context.Context, owner, repo, ref, path string) (bool, error) {
	tok, err := c.tokenForInstallation(ctx)
	if err != nil {
		return false, err
	}
	u := "/repos/" + owner + "/" + repo + "/contents/" + escapePath(path)
	if ref != "" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	if _, err := c.doJSON(ctx, http.MethodGet, u, tok, nil, nil); err != nil {
		var ge *githubAPIError
		if errors.As(err, &ge) && ge.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *githubAppClient) listRepos(ctx context.Context) ([]githubRepo, error) {
	tok, err := c.tokenForInstallation(ctx)
	if err != nil {
		return nil, err
	}
	var repos []githubRepo
	next := "/installation/repositories?per_page=100"
	for page := 0; page < 10 && next != ""; page++ {
		var out struct {
			Repositories []githubRepo `json:"repositories"`
		}
		resp, err := c.doJSON(ctx, http.MethodGet, next, tok, nil, &out)
		if err != nil {
			return nil, fmt.Errorf("list installation repositories: %w", err)
		}
		repos = append(repos, out.Repositories...)
		next = nextLink(resp.Header.Get("Link"))
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	return repos, nil
}

// diagnostics assembles a complete picture of the app's install state without
// failing on the first error: the app's installations (via JWT) plus the
// installation the token resolves to (via GET /installation, which 404s when
// the token's install was removed). Errors are embedded in the map, never
// swallowed silently.
func (c *githubAppClient) diagnostics(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}

	jwt, err := c.appJWT(time.Now())
	if err != nil {
		out["jwt_error"] = err.Error()
	} else if insts, err := c.listInstallations(ctx, jwt); err != nil {
		out["installations_error"] = err.Error()
	} else {
		out["app_installations"] = insts
	}

	if tok, err := c.tokenForInstallation(ctx); err != nil {
		out["token_error"] = err.Error()
	} else {
		var info map[string]any
		if _, err := c.doJSON(ctx, http.MethodGet, "/installation", tok, nil, &info); err != nil {
			out["installation_error"] = err.Error()
		} else {
			out["installation"] = info
		}
	}
	return out, nil
}

// nextLink extracts the URL of the next page from a GitHub Link header.
func nextLink(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		seg := strings.SplitN(strings.TrimSpace(part), ";", 2)
		if len(seg) != 2 {
			continue
		}
		if strings.Contains(seg[1], `rel="next"`) {
			return strings.Trim(strings.TrimSpace(seg[0]), "<>")
		}
	}
	return ""
}

func (c *githubAppClient) setSecret(ctx context.Context, owner, repo, name, value string) error {
	tok, err := c.tokenForInstallation(ctx)
	if err != nil {
		return err
	}
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/repos/"+owner+"/"+repo+"/actions/secrets/public-key", tok, nil, &pk); err != nil {
		return fmt.Errorf("fetch repo public key: %w", err)
	}
	encrypted, err := sealBox(pk.Key, []byte(value))
	if err != nil {
		return err
	}
	body := map[string]string{"encrypted_value": encrypted, "key_id": pk.KeyID}
	if _, err := c.doJSON(ctx, http.MethodPut, "/repos/"+owner+"/"+repo+"/actions/secrets/"+name, tok, body, nil); err != nil {
		return fmt.Errorf("set secret %s: %w", name, err)
	}
	return nil
}

func (c *githubAppClient) openWorkflowPR(ctx context.Context, owner, repo string, p workflowPRParams) (workflowPRResult, error) {
	tok, err := c.tokenForInstallation(ctx)
	if err != nil {
		return workflowPRResult{}, err
	}

	// 1. HEAD sha of the base branch (proves the repo has commits).
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/repos/"+owner+"/"+repo+"/git/ref/heads/"+p.BaseBranch, tok, nil, &ref); err != nil {
		return workflowPRResult{}, fmt.Errorf("resolve base branch %q: %w", p.BaseBranch, err)
	}
	if ref.Object.SHA == "" {
		return workflowPRResult{}, errors.New("repo has no commits on " + p.BaseBranch + " — make an initial commit before onboarding")
	}

	// 2. Create the onboarding branch; on name collision retry with a suffix.
	branch := "pipeline/onboard-" + p.Service
	created := false
	for i := 0; i < 10; i++ {
		body := map[string]string{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}
		// Declare err with := so the same value is visible to the checks below;
		// an if-initializer scoped err would be nil here and swallow the real
		// GitHub error (and break the 422 collision retry).
		_, err := c.doJSON(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/git/refs", tok, body, nil)
		if err == nil {
			created = true
			break
		}
		var ge *githubAPIError
		if errors.As(err, &ge) && ge.Status == http.StatusUnprocessableEntity {
			branch = fmt.Sprintf("pipeline/onboard-%s-%d", p.Service, i+2)
			continue
		}
		return workflowPRResult{}, fmt.Errorf("create branch %q: %w", branch, err)
	}
	if !created {
		return workflowPRResult{}, errors.New("create branch: too many name collisions")
	}
	logEvent("onboard.branch_created", map[string]any{"repo": owner + "/" + repo, "branch": branch})

	// 3. Commit the workflow file to the branch (create or update). If the
	// file already exists on the branch — e.g. inherited from a main that
	// gained it after a prior onboarding PR was merged — GitHub requires the
	// existing file's sha to allow the update, so fetch it first.
	existingSHA := ""
	var existing struct {
		SHA string `json:"sha"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/repos/"+owner+"/"+repo+"/contents/.github/workflows/deploy.yml?ref="+url.QueryEscape(branch), tok, nil, &existing); err == nil {
		existingSHA = existing.SHA
	}
	commit := map[string]any{
		"message": "Add pipeline deploy workflow",
		"content": base64.StdEncoding.EncodeToString([]byte(p.Content)),
		"branch":  branch,
	}
	if existingSHA != "" {
		commit["sha"] = existingSHA
	}
	if _, err := c.doJSON(ctx, http.MethodPut, "/repos/"+owner+"/"+repo+"/contents/.github/workflows/deploy.yml", tok, commit, nil); err != nil {
		return workflowPRResult{}, fmt.Errorf("commit workflow file: %w%s", err, workflowWriteHint(err))
	}
	logEvent("onboard.file_committed", map[string]any{"repo": owner + "/" + repo, "branch": branch})

	// 4. Open the PR. Deliberately no merge — a human reviews it.
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	pr := map[string]string{"title": p.Title, "head": branch, "base": p.BaseBranch, "body": p.Body}
	if _, err := c.doJSON(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/pulls", tok, pr, &out); err != nil {
		return workflowPRResult{}, fmt.Errorf("open pull request: %w", err)
	}
	logEvent("onboard.pr_opened", map[string]any{"repo": owner + "/" + repo, "branch": branch, "pr": out.Number, "url": out.HTMLURL})
	return workflowPRResult{Number: out.Number, URL: out.HTMLURL, Branch: branch}, nil
}

// ---- helpers ----

// workflowWriteHint appends a targeted fix hint when a GitHub App is denied
// writing a workflow file — a well-known gotcha: Contents: write alone is not
// enough for .github/workflows/**, the app also needs the Workflows permission
// set to Read and write (not Actions), and the install must be re-approved
// after permission changes.
func workflowWriteHint(err error) string {
	var ge *githubAPIError
	if errors.As(err, &ge) && ge.Status == http.StatusForbidden {
		return " (hint: writing .github/workflows/ files requires the GitHub App's Workflows permission set to Read and write — then re-install the app to apply it)"
	}
	return ""
}

// sealBox encrypts msg for the repo's Actions-secrets public key using a
// libsodium-style sealed box (X25519-XSalsa20-Poly1305), as required by
// PUT /repos/{owner}/{repo}/actions/secrets/{name}. Returns base64.
func sealBox(publicKeyB64 string, msg []byte) (string, error) {
	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(pub) != 32 {
		return "", errors.New("invalid repo public key")
	}
	var pk [32]byte
	copy(pk[:], pub)
	sealed, err := box.SealAnonymous(nil, msg, &pk, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// escapePath percent-encodes a file path for the Contents API while keeping
// the "/" separators intact.
func escapePath(path string) string {
	return (&url.URL{Path: path}).EscapedPath()
}

// githubAPIError carries the HTTP status of a failed GitHub API call.
type githubAPIError struct {
	Status  int
	Message string
	Path    string
}

func (e *githubAPIError) Error() string {
	return fmt.Sprintf("github api %s: %s (status %d)", e.Path, e.Message, e.Status)
}

func (c *githubAppClient) doJSON(ctx context.Context, method, path, token string, body, out any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "personal-pipeline-onboarder")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		logEvent("github.api_error", map[string]any{
			"method": method, "path": path, "status": 0, "error": err.Error(),
		})
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := readLimited(resp.Body, 512)
		// Surface every GitHub API failure in the agent log, not just in the
		// response body (which Cloudflare's 502 page hides).
		logEvent("github.api_error", map[string]any{
			"method": method, "path": path, "status": resp.StatusCode, "message": msg,
		})
		return resp, &githubAPIError{Status: resp.StatusCode, Message: msg, Path: path}
	}
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp, nil
}

func readLimited(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	msg := strings.TrimSpace(string(b))
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &parsed) == nil && parsed.Message != "" {
		return parsed.Message
	}
	return msg
}
