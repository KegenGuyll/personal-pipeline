package main

import (
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

func testAppKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testAppKeyPEMB64(t *testing.T) string {
	t.Helper()
	key := testAppKey(t)
	der := x509.MarshalPKCS1PrivateKey(key)
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return base64.StdEncoding.EncodeToString(block)
}

func TestNewGithubAppClient(t *testing.T) {
	if c, err := newGithubAppClient(&Config{}); err != nil || c != nil {
		t.Fatalf("unconfigured: client=%v err=%v, want nil,nil", c, err)
	}
	if _, err := newGithubAppClient(&Config{GithubAppID: 123}); err == nil {
		t.Fatal("expected error when App ID set but no key")
	}
	c, err := newGithubAppClient(&Config{
		GithubAppID:            123,
		GithubAppPrivateKeyB64: testAppKeyPEMB64(t),
		PipelineOwner:          "alice",
	})
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if c == nil || c.appID != 123 {
		t.Fatalf("bad client: %+v", c)
	}
	if _, err := newGithubAppClient(&Config{GithubAppID: 1, GithubAppPrivateKeyB64: "%%%not-base64%%%"}); err == nil {
		t.Fatal("expected error on bad base64")
	}
}

func TestAppJWT(t *testing.T) {
	key := testAppKey(t)
	c := &githubAppClient{appID: 456, key: key}
	jwt, err := c.appJWT(time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}

	// Verify the RS256 signature.
	signing := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(signing))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	// Check claims.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "456" {
		t.Fatalf("iss = %s", claims.Iss)
	}
	if claims.Iat != 940 || claims.Exp != 1600 {
		t.Fatalf("iat/exp = %d/%d", claims.Iat, claims.Exp)
	}
}

func TestSealBoxRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealBox(base64.StdEncoding.EncodeToString(pub[:]), []byte(`{"API_KEY":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	opened, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok {
		t.Fatal("could not open sealed box")
	}
	if string(opened) != `{"API_KEY":"secret"}` {
		t.Fatalf("round-trip = %s", opened)
	}

	if _, err := sealBox("not-base64", []byte("x")); err == nil {
		t.Fatal("expected error on bad key")
	}
}

// newTestGithubServer spins up a fake GitHub API that satisfies the happy path
// of openWorkflowPR + token minting, recording every requested path.
func newTestGithubServer(t *testing.T, installationID int64) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		auth := r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
			if !strings.HasPrefix(auth, "Bearer eyJ") {
				http.Error(w, "expected app jwt", http.StatusUnauthorized)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]string{
				"token": "inst-tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
			writeJSON(w, http.StatusOK, map[string]string{"default_branch": "main"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/git/ref/heads/main":
			writeJSON(w, http.StatusOK, map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/refs":
			writeJSON(w, http.StatusCreated, map[string]string{"ref": "refs/heads/pipeline/onboard-web"})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/contents/.github/workflows/deploy.yml":
			writeJSON(w, http.StatusCreated, map[string]string{"commit": "sha1"})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			writeJSON(w, http.StatusCreated, map[string]any{
				"number": 7, "html_url": "https://github.com/o/r/pull/7",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/contents/Dockerfile":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/secrets/public-key":
			writeJSON(w, http.StatusOK, map[string]string{"key_id": "kid1", "key": base64.StdEncoding.EncodeToString(make([]byte, 32))})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/actions/secrets/SERVICE_ENV":
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	_ = installationID
	return srv, &paths
}

func TestOpenWorkflowPRNeverMerges(t *testing.T) {
	srv, paths := newTestGithubServer(t, 7)
	defer srv.Close()

	c := &githubAppClient{
		appID: 1, key: testAppKey(t), installationID: 7,
		apiURL: srv.URL, http: srv.Client(),
	}
	res, err := c.openWorkflowPR(context.Background(), "o", "r", workflowPRParams{
		Service: "web", BaseBranch: "main", Content: "name: Deploy\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 7 || res.Branch != "pipeline/onboard-web" || res.URL == "" {
		t.Fatalf("result = %+v", res)
	}
	for _, p := range *paths {
		if strings.Contains(p, "merge") {
			t.Fatalf("a merge endpoint was hit: %s", p)
		}
	}
}

// Regression: a name collision (422 on POST /git/refs) must retry with a
// numeric suffix — the if-initializer scoping bug swallowed the err (and the
// retry) and surfaced "%!w(<nil>)".
func TestOpenWorkflowPRRetriesBranchCollision(t *testing.T) {
	refsCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeJSON(w, http.StatusCreated, map[string]string{
				"token": "inst-tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/repos/o/r/git/ref/heads/main":
			writeJSON(w, http.StatusOK, map[string]any{"object": map[string]string{"sha": "abc123"}})
		case "/repos/o/r/git/refs":
			refsCalls++
			if refsCalls == 1 {
				http.Error(w, `{"message":"Reference already exists"}`, http.StatusUnprocessableEntity)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]string{"ref": "refs/heads/pipeline/onboard-web-2"})
		case "/repos/o/r/contents/.github/workflows/deploy.yml":
			writeJSON(w, http.StatusCreated, map[string]string{"commit": "sha1"})
		case "/repos/o/r/pulls":
			writeJSON(w, http.StatusCreated, map[string]any{"number": 9, "html_url": "https://github.com/o/r/pull/9"})
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 7, apiURL: srv.URL, http: srv.Client()}
	res, err := c.openWorkflowPR(context.Background(), "o", "r", workflowPRParams{
		Service: "web", BaseBranch: "main", Content: "name: Deploy\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "pipeline/onboard-web-2" {
		t.Fatalf("branch = %q, want pipeline/onboard-web-2", res.Branch)
	}
	if refsCalls != 2 {
		t.Fatalf("refs calls = %d, want 2", refsCalls)
	}
}

// Regression: a hard failure (403) must surface the real GitHub error with its
// status — never "%!w(<nil>)".
func TestOpenWorkflowPRSurfacesRealError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			writeJSON(w, http.StatusCreated, map[string]string{
				"token": "inst-tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/repos/o/r/git/ref/heads/main":
			writeJSON(w, http.StatusOK, map[string]any{"object": map[string]string{"sha": "abc123"}})
		case "/repos/o/r/git/refs":
			http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 7, apiURL: srv.URL, http: srv.Client()}
	_, err := c.openWorkflowPR(context.Background(), "o", "r", workflowPRParams{
		Service: "web", BaseBranch: "main", Content: "name: Deploy\n",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "create branch") || !strings.Contains(err.Error(), "(status 403)") {
		t.Fatalf("error does not carry the real GitHub failure: %v", err)
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("error contains fmt artifact: %v", err)
	}
}

func TestHasFile(t *testing.T) {
	srv, _ := newTestGithubServer(t, 7)
	defer srv.Close()
	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 7, apiURL: srv.URL, http: srv.Client()}

	// The fake server 404s /contents/Dockerfile.
	ok, err := c.hasFile(context.Background(), "o", "r", "main", "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected missing")
	}
	// Any 404 is "missing" — the semantic the Dockerfile check relies on.
	ok2, err := c.hasFile(context.Background(), "o", "r", "main", "nowhere/at/all")
	if err != nil || ok2 {
		t.Fatalf("404 should map to missing: ok=%v err=%v", ok2, err)
	}
}

func TestSetSecretEncryptsForRepo(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		KeyID          string `json:"key_id"`
		EncryptedValue string `json:"encrypted_value"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/3/access_tokens":
			writeJSON(w, http.StatusCreated, map[string]string{
				"token": "inst-tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/repos/o/r/actions/secrets/public-key":
			writeJSON(w, http.StatusOK, map[string]string{
				"key_id": "kid1", "key": base64.StdEncoding.EncodeToString(pub[:]),
			})
		case "/repos/o/r/actions/secrets/SERVICE_ENV":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 3, apiURL: srv.URL, http: srv.Client()}
	value := `{"API_KEY":"x"}`
	if err := c.setSecret(context.Background(), "o", "r", "SERVICE_ENV", value); err != nil {
		t.Fatal(err)
	}
	if got.KeyID != "kid1" {
		t.Fatalf("key_id = %s", got.KeyID)
	}
	raw, err := base64.StdEncoding.DecodeString(got.EncryptedValue)
	if err != nil {
		t.Fatal(err)
	}
	opened, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok {
		t.Fatal("could not open secret")
	}
	if string(opened) != value {
		t.Fatalf("secret = %s, want %s", opened, value)
	}
}

func TestGithubAPIErrorNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 1, apiURL: srv.URL, http: srv.Client()}
	_, err := c.repoInfo(context.Background(), "o", "r")
	var ge *githubAPIError
	if !errors.As(err, &ge) || ge.Status != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestListReposPaginatesAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/5/access_tokens":
			writeJSON(w, http.StatusCreated, map[string]string{
				"token": "inst-tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/installation/repositories":
			if r.URL.Query().Get("page") == "2" {
				writeJSON(w, http.StatusOK, map[string]any{
					"repositories": []map[string]string{{"name": "api", "full_name": "alice/api"}},
				})
				return
			}
			w.Header().Set("Link", `</installation/repositories?per_page=100&page=2>; rel="next"`)
			writeJSON(w, http.StatusOK, map[string]any{
				"repositories": []map[string]string{{"name": "web", "full_name": "alice/web"}},
			})
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &githubAppClient{appID: 1, key: testAppKey(t), installationID: 5, apiURL: srv.URL, http: srv.Client()}
	repos, err := c.listRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want 2 (page 1 + next link)", repos)
	}
	if repos[0].FullName != "alice/api" || repos[1].FullName != "alice/web" {
		t.Fatalf("expected sorted by full name: %+v", repos)
	}
}

func TestNextLink(t *testing.T) {
	if got := nextLink(`</a>; rel="prev", </b>; rel="next"`); got != "/b" {
		t.Fatalf("next = %q", got)
	}
	if got := nextLink(`</a>; rel="last"`); got != "" {
		t.Fatalf("next = %q, want empty", got)
	}
}

func TestWorkflowWriteHint(t *testing.T) {
	if got := workflowWriteHint(&githubAPIError{Status: http.StatusForbidden, Message: "nope"}); !strings.Contains(got, "Actions") {
		t.Fatalf("expected Actions hint for 403, got %q", got)
	}
	if got := workflowWriteHint(&githubAPIError{Status: http.StatusNotFound, Message: "nope"}); got != "" {
		t.Fatalf("expected no hint for 404, got %q", got)
	}
	if got := workflowWriteHint(errors.New("transport")); got != "" {
		t.Fatalf("expected no hint for transport error, got %q", got)
	}
}
