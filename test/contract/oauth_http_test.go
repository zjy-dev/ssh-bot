package contract_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/oauth"
)

func newKeyB64() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(b)
}

type fakeNotifier struct{ calls int }

func (f *fakeNotifier) NotifyUser(_ context.Context, _, _ string) error { f.calls++; return nil }

func newOAuthServer(t *testing.T) (*oauth.Server, *oauth.Store, *oauth.StateSigner, *httptest.Server) {
	t.Helper()
	signer, err := oauth.NewStateSigner(newKeyB64())
	require.NoError(t, err)
	enc, err := oauth.NewEncryptor(newKeyB64())
	require.NoError(t, err)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := oauth.NewStore(rdb, enc)

	var feishuTokenSrv *httptest.Server
	_ = feishuTokenSrv

	cfg := oauth.Config{
		ListenAddr:    "127.0.0.1:0",
		PublicBaseURL: "https://bot.example.com",
		AppID:         "cli_xxx",
		AppSecret:     "secret",
		Scopes:        []string{"docx:document:readonly", "offline_access"},
		Notifier:      &fakeNotifier{},
	}
	srv := oauth.NewServer(cfg, signer, store, nil)

	// We don't call srv.ListenAndServe; we use httptest.NewServer backed by a
	// mux that matches what our server installs. For contract tests of
	// /oauth/start and /oauth/callback we actually want to exercise the real
	// handlers: we do that by using the internal http.Server indirectly —
	// since NewServer doesn't expose its mux, construct a request/recorder
	// pair against the real handler via startOnce.
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/start", srv.HandleStartForTest)
	mux.HandleFunc("/oauth/callback", srv.HandleCallbackForTest)
	mux.HandleFunc("/healthz", srv.HandleHealthForTest)

	return srv, store, signer, httptest.NewServer(mux)
}

func TestOAuth_Health(t *testing.T) {
	_, _, _, ts := newOAuthServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
}

func TestOAuth_StartRedirectsWithValidState(t *testing.T) {
	_, _, signer, ts := newOAuthServer(t)
	defer ts.Close()

	state, err := signer.Sign("ou_abc")
	require.NoError(t, err)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/oauth/start?state=" + state)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.Contains(t, loc, "accounts.feishu.cn")
	require.Contains(t, loc, "state="+state)
	require.Contains(t, loc, "redirect_uri=")
	require.Contains(t, loc, "scope=")
}

func TestOAuth_StartRejectsBadState(t *testing.T) {
	_, _, _, ts := newOAuthServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/oauth/start?state=garbage")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuth_CallbackRejectsBadState(t *testing.T) {
	_, _, _, ts := newOAuthServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/oauth/callback?code=abc&state=nope")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestOAuth_CallbackSuccessPath requires a mock of Feishu's token endpoint.
// We override a package-level hook for the test.
func TestOAuth_CallbackSuccessPath(t *testing.T) {
	srv, store, signer, ts := newOAuthServer(t)
	defer ts.Close()

	// Spin up a stand-in for Feishu's token endpoint.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"access_token":             "access_xyz",
			"expires_in":               7200,
			"refresh_token":            "refresh_abc",
			"refresh_token_expires_in": 604800,
			"scope":                    "docx:document:readonly offline_access",
		}
		raw, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer tokenSrv.Close()

	// Override the token endpoint used by the server under test.
	srv.OverrideTokenEndpointForTest(tokenSrv.URL)

	state, err := signer.Sign("ou_alice")
	require.NoError(t, err)

	resp, err := http.Get(ts.URL + "/oauth/callback?code=THECODE&state=" + state)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got, err := store.Get(context.Background(), "ou_alice")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "access_xyz", got.AccessToken)
	require.Equal(t, "refresh_abc", got.RefreshToken)
	require.Contains(t, got.Scopes, "docx:document:readonly")
}

func TestStateSigner_Roundtrip(t *testing.T) {
	signer, err := oauth.NewStateSigner(newKeyB64())
	require.NoError(t, err)
	state, err := signer.Sign("ou_xyz")
	require.NoError(t, err)
	got, err := signer.Verify(state)
	require.NoError(t, err)
	require.Equal(t, "ou_xyz", got)
}

func TestStateSigner_TamperDetected(t *testing.T) {
	signer, err := oauth.NewStateSigner(newKeyB64())
	require.NoError(t, err)
	state, err := signer.Sign("ou_xyz")
	require.NoError(t, err)
	// Flip a byte in the middle.
	raw := []byte(state)
	raw[len(raw)/2] ^= 0xff
	_, err = signer.Verify(string(raw))
	require.ErrorIs(t, err, oauth.ErrInvalidState)
}

func TestEncryptor_Roundtrip(t *testing.T) {
	enc, err := oauth.NewEncryptor(newKeyB64())
	require.NoError(t, err)
	ct, err := enc.Seal([]byte("hello"))
	require.NoError(t, err)
	pt, err := enc.Open(ct)
	require.NoError(t, err)
	require.Equal(t, "hello", string(pt))
}

func TestEncryptor_TamperFails(t *testing.T) {
	enc, err := oauth.NewEncryptor(newKeyB64())
	require.NoError(t, err)
	ct, err := enc.Seal([]byte("hello"))
	require.NoError(t, err)
	ct[len(ct)-1] ^= 0x01
	_, err = enc.Open(ct)
	require.Error(t, err)
}
