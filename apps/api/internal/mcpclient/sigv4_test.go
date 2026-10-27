package mcpclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// Test-only credentials: AWS's documented example key pair, not a secret.
const (
	testAccessKey = "AKIDEXAMPLE"
	testSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testToken     = "session-token-example"
)

// countingCreds is a static credentials provider that counts retrievals.
type countingCreds struct{ n atomic.Int32 }

func (c *countingCreds) Retrieve(context.Context) (aws.Credentials, error) {
	c.n.Add(1)
	return aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey, SessionToken: testToken, Source: "test"}, nil
}

// recordingV4 records what the SigV4 signer hands to the SDK.
type recordingV4 struct {
	creds                        aws.Credentials
	payloadHash, service, region string
	connection                   string
}

func (r *recordingV4) SignHTTP(_ context.Context, creds aws.Credentials, req *http.Request, payloadHash, service, region string, _ time.Time, _ ...func(*v4.SignerOptions)) error {
	r.creds, r.payloadHash, r.service, r.region = creds, payloadHash, service, region
	r.connection = req.Header.Get("Connection")
	req.Header.Set("Authorization", "recorded")
	return nil
}

func TestSigV4SignerHandsTheBodyHashToTheSDK(t *testing.T) {
	rec := &recordingV4{}
	s := newSigV4Signer(&countingCreds{}, rec, AWSMCPService, "eu-central-1")
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"AWS_REGION":"eu-west-1"}}}`)
	req, _ := http.NewRequest(http.MethodPost, "https://aws-mcp.eu-central-1.api.aws/mcp", bytes.NewReader(body))
	req.Header.Set("Connection", "keep-alive")
	if err := s.Sign(t.Context(), req, body); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if rec.payloadHash != hex.EncodeToString(sum[:]) {
		t.Errorf("payload hash = %s, want sha256 of the body", rec.payloadHash)
	}
	if rec.service != "aws-mcp" || rec.region != "eu-central-1" || rec.creds.AccessKeyID != testAccessKey {
		t.Errorf("signed with service=%q region=%q creds=%q", rec.service, rec.region, rec.creds.AccessKeyID)
	}
	if rec.connection != "" {
		t.Error("Connection must be dropped before signing")
	}

	// A nil body hashes as the empty payload.
	if err := s.Sign(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if rec.payloadHash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty payload hash = %s", rec.payloadHash)
	}
}

func TestSigV4SignerCachesCredentials(t *testing.T) {
	creds := &countingCreds{}
	s := NewSigV4Signer(creds, AWSMCPService, "us-east-1")
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://aws-mcp.us-east-1.api.aws/mcp", nil)
		if err := s.Sign(t.Context(), req, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if n := creds.n.Load(); n != 1 {
		t.Errorf("credentials retrieved %d times for 5 requests, want 1", n)
	}
}

func TestSigV4SignerErrors(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x/mcp", nil)
	if err := NewSigV4Signer(nil, AWSMCPService, "us-east-1").Sign(t.Context(), req, nil); err == nil {
		t.Error("nil credentials accepted")
	}
	failing := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("no identity")
	})
	if err := NewSigV4Signer(failing, AWSMCPService, "us-east-1").Sign(t.Context(), req, nil); err == nil || !strings.Contains(err.Error(), "no identity") {
		t.Errorf("err = %v", err)
	}
}

// verifySigV4 recomputes a SigV4 signature from the request as the server
// received it — independently of the SDK — and compares it to the one in
// the Authorization header. It proves the signature covers the exact body
// and headers that arrived.
func verifySigV4(r *http.Request, body []byte, secret, service, region string) error {
	auth := r.Header.Get("Authorization")
	const alg = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, alg) {
		return fmt.Errorf("authorization %q is not SigV4", auth)
	}
	fields := map[string]string{}
	for _, p := range strings.Split(strings.TrimPrefix(auth, alg), ", ") {
		k, v, _ := strings.Cut(p, "=")
		fields[k] = v
	}
	scope := strings.SplitN(fields["Credential"], "/", 2)
	if len(scope) != 2 || scope[0] != testAccessKey {
		return fmt.Errorf("credential %q", fields["Credential"])
	}
	date := strings.Split(scope[1], "/")[0]
	if scope[1] != date+"/"+region+"/"+service+"/aws4_request" {
		return fmt.Errorf("scope %q, want service %s region %s", scope[1], service, region)
	}
	var canonHeaders strings.Builder
	for _, h := range strings.Split(fields["SignedHeaders"], ";") {
		var v string
		switch h {
		case "host":
			v = r.Host
		case "content-length":
			v = strconv.FormatInt(r.ContentLength, 10)
		default:
			vals := r.Header.Values(h)
			for i := range vals {
				vals[i] = strings.Join(strings.Fields(vals[i]), " ")
			}
			v = strings.Join(vals, ",")
		}
		canonHeaders.WriteString(h + ":" + v + "\n")
	}
	bodySum := sha256.Sum256(body)
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, canonHeaders.String(), fields["SignedHeaders"], hex.EncodeToString(bodySum[:])}, "\n")
	canonSum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", r.Header.Get("X-Amz-Date"), scope[1], hex.EncodeToString(canonSum[:])}, "\n")
	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	k := mac([]byte("AWS4"+secret), date)
	k = mac(k, region)
	k = mac(k, service)
	k = mac(k, "aws4_request")
	if want := hex.EncodeToString(mac(k, toSign)); want != fields["Signature"] {
		return fmt.Errorf("signature mismatch")
	}
	must := []string{"host", "x-amz-date", "x-amz-security-token"}
	if r.Method == http.MethodPost {
		must = append(must, "content-type", "content-length")
	}
	for _, must := range must {
		if !strings.Contains(";"+fields["SignedHeaders"]+";", ";"+must+";") {
			return fmt.Errorf("%s is not signed: %s", must, fields["SignedHeaders"])
		}
	}
	return nil
}

// TestSigV4EndToEnd runs the real SDK signer through the client against a
// server that verifies every request's signature from scratch, including the
// re-signed retry after a lost session and the session DELETE.
func TestSigV4EndToEnd(t *testing.T) {
	f := newFake(t)
	var verified atomic.Int32
	f.hook = func(_ http.ResponseWriter, r *http.Request, req wire.Request, body []byte) bool {
		if err := verifySigV4(r, body, testSecretKey, AWSMCPService, "us-east-1"); err != nil {
			t.Errorf("%s %s: %v", r.Method, req.Method, err)
		} else {
			verified.Add(1)
		}
		if r.Header.Get("X-Amz-Security-Token") != testToken {
			t.Error("temporary credentials must send their session token")
		}
		if req.Method == "tools/call" && r.Header.Get("Mcp-Session-Id") == "sess-1" {
			f.forgetSessions()
		}
		return false
	}
	srv := f.start()
	c := newTestClient(t, srv,
		WithSigner(NewSigV4Signer(&countingCreds{}, AWSMCPService, "us-east-1")),
		WithMeta(func() map[string]any { return map[string]any{"AWS_REGION": "eu-west-1"} }))
	if _, err := c.CallTool(t.Context(), "echo", []byte(`{"service":"rds","operation":"DescribeDBInstances"}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// initialize, initialized, call(lost), initialize, initialized, call, DELETE
	if n := verified.Load(); n != 7 {
		t.Errorf("verified %d signatures, want 7", n)
	}
}

func TestAssumeRoleCredentials(t *testing.T) {
	var form atomicForm
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form.set(r.PostForm.Encode())
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAASSUMED</AccessKeyId>
      <SecretAccessKey>assumed-secret</SecretAccessKey>
      <SessionToken>assumed-token</SessionToken>
      <Expiration>%s</Expiration>
    </Credentials>
    <AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/kb/kubebolt-kobi</Arn><AssumedRoleId>AROA:kubebolt-kobi</AssumedRoleId></AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata>
</AssumeRoleResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer sts.Close()

	base := aws.Config{
		Region:       "us-east-1",
		Credentials:  &countingCreds{},
		BaseEndpoint: aws.String(sts.URL),
		HTTPClient:   sts.Client(),
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"rds:Describe*","Resource":"*"}]}`
	creds, err := AssumeRoleCredentials(base, "arn:aws:iam::123456789012:role/kubebolt-mcp", "ext-123", policy, "kubebolt-kobi")
	if err != nil {
		t.Fatal(err)
	}
	got, err := creds.Retrieve(t.Context())
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.AccessKeyID != "ASIAASSUMED" || got.SessionToken != "assumed-token" || !got.CanExpire {
		t.Errorf("assumed credentials = %+v", got.AccessKeyID)
	}
	sent := form.get()
	for _, want := range []string{"Action=AssumeRole", "ExternalId=ext-123", "RoleSessionName=kubebolt-kobi", "RoleArn=arn%3Aaws%3Aiam%3A%3A123456789012%3Arole%2Fkubebolt-mcp", "Policy="} {
		if !strings.Contains(sent, want) {
			t.Errorf("AssumeRole request lacks %q: %s", want, sent)
		}
	}
	if _, err := creds.Retrieve(t.Context()); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"bad arn": func() error {
			_, err := AssumeRoleCredentials(base, "arn:aws:iam::123:user/x", "", "", "")
			return err
		},
		"policy over limit": func() error {
			_, err := AssumeRoleCredentials(base, "arn:aws:iam::123456789012:role/r", "", strings.Repeat("x", MaxSessionPolicyChars+1), "")
			return err
		},
		"bad session name": func() error {
			_, err := AssumeRoleCredentials(base, "arn:aws:iam::123456789012:role/r", "", "", "has space")
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// An external ID STS would reject is refused before the call, without
	// its value in the error: STS's own validation message echoes it.
	for _, bad := range []string{"x", "has space " + testSecretKey, strings.Repeat("a", 1225), "ext\nid"} {
		_, err := AssumeRoleCredentials(base, "arn:aws:iam::123456789012:role/r", bad, "", "")
		if err == nil {
			t.Errorf("external ID %q accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), testSecretKey) || strings.Contains(err.Error(), "ext\nid") {
			t.Errorf("error leaks the external ID: %v", err)
		}
	}
	if _, err := AssumeRoleCredentials(base, "arn:aws:iam::123456789012:role/r", "Kb:org/1234-abcd_EF+=,.@", "", ""); err != nil {
		t.Errorf("a valid external ID was refused: %v", err)
	}
}

type atomicForm struct{ v atomic.Value }

func (a *atomicForm) set(s string) { a.v.Store(s) }
func (a *atomicForm) get() string {
	s, _ := a.v.Load().(string)
	return s
}

func TestLoadAWSConfigProfile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config")
	credFile := filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfgFile, []byte("[profile readonly]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credFile, []byte("[readonly]\naws_access_key_id = "+testAccessKey+"\naws_secret_access_key = "+testSecretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credFile)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_PROFILE", "")

	cfg, err := LoadAWSConfig(t.Context(), "us-east-1", "readonly")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("region = %q, the explicit region must win", cfg.Region)
	}
	creds, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != testAccessKey {
		t.Errorf("profile credentials: %v", err)
	}
	if _, err := LoadAWSConfig(t.Context(), "us-east-1", "../../etc/passwd"); err == nil {
		t.Error("a path as profile name was accepted")
	}
	if _, err := LoadAWSConfig(t.Context(), "us-east-1", "missing"); err == nil {
		t.Error("a missing profile was accepted")
	}
}
