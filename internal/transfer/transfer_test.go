package transfer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/WellWells/agentswap/internal/antigravity"
	"github.com/WellWells/agentswap/internal/claude"
	"github.com/WellWells/agentswap/internal/codex"
	"github.com/WellWells/agentswap/internal/store"
	"github.com/WellWells/agentswap/internal/swap"
)

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

type memKeyring struct {
	b []byte
}

func (m *memKeyring) Read() ([]byte, error) {
	if m.b == nil {
		return nil, os.ErrNotExist
	}
	return m.b, nil
}

func (m *memKeyring) Write(b []byte) error {
	m.b = append([]byte(nil), b...)
	return nil
}

func newTestManagers(t *testing.T) map[string]*swap.Manager {
	t.Helper()
	dir := t.TempDir()

	codexHome := filepath.Join(dir, "codex_home")
	claudeHome := filepath.Join(dir, "claude_home")
	_ = os.MkdirAll(codexHome, 0o700)
	_ = os.MkdirAll(claudeHome, 0o700)

	cxMgr := &swap.Manager{
		P: codex.Provider{Home: codexHome},
		S: store.Store{Dir: filepath.Join(dir, "store_codex")},
	}
	ccMgr := &swap.Manager{
		P: claude.New(func(string) string { return "" }, claudeHome, "linux", nil),
		S: store.Store{Dir: filepath.Join(dir, "store_claude")},
	}
	agMgr := &swap.Manager{
		P: antigravity.Provider{Keyring: &memKeyring{}},
		S: store.Store{Dir: filepath.Join(dir, "store_agy")},
	}

	return map[string]*swap.Manager{
		"codex":       cxMgr,
		"claude":      ccMgr,
		"antigravity": agMgr,
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var v1, v2 any
	if err := json.Unmarshal(a, &v1); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &v2); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	norm1, _ := json.Marshal(v1)
	norm2, _ := json.Marshal(v2)
	return bytes.Equal(norm1, norm2)
}

func TestNativeSnapshotExportAndImport(t *testing.T) {
	mgrsSrc := newTestManagers(t)

	// 1. Codex OAuth Snapshot with rich metadata (zero field loss test)
	idTokenCX := fakeJWT(map[string]any{
		"email": "user@openai.com",
		"sub":   "auth0|user123",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "org-xyz",
			"chatgpt_user_id":    "u-123",
			"chatgpt_plan_type":  "plus",
		},
	})
	snapCX, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idTokenCX,
			"refresh_token": "rt-openai",
			"account_id":    "org-xyz",
			"access_token":  "at-openai",
		},
		"last_refresh": "2026-10-08T12:34:56Z",
	})
	if _, err := mgrsSrc["codex"].Import(snapCX, "cx-plus"); err != nil {
		t.Fatalf("seed codex: %v", err)
	}

	// 2. Claude Native Snapshot
	credsClaude, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-claude",
			"refreshToken":     "rt-claude",
			"expiresAt":        1735689600.0,
			"subscriptionType": "team",
		},
	})
	oauthClaude, _ := json.Marshal(map[string]any{
		"accountUuid":      "uuid-claude-1",
		"organizationUuid": "org-claude-1",
		"emailAddress":     "claude@example.com",
	})
	snapCL := claude.Pack(credsClaude, oauthClaude)
	if _, err := mgrsSrc["claude"].Import(snapCL, "cl-team"); err != nil {
		t.Fatalf("seed claude: %v", err)
	}

	// 3. Antigravity Native Snapshot
	idTokenAG := fakeJWT(map[string]any{
		"sub":   "google-sub-789",
		"email": "agy@google.com",
	})
	snapAG, _ := json.Marshal(map[string]any{
		"token": map[string]any{
			"access_token":  "at-agy",
			"refresh_token": "rt-agy",
		},
		"id_token":    idTokenAG,
		"auth_method": "consumer",
	})
	if _, err := mgrsSrc["antigravity"].Import(snapAG, "ag-main"); err != nil {
		t.Fatalf("seed antigravity: %v", err)
	}

	// Export native snapshots directly
	bundle, err := Export(mgrsSrc)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Verify 100% byte/semantic fidelity: exported token matches native snapshot directly
	if len(bundle["codex"]) != 1 || !bytes.Equal(bundle["codex"][0].Token, snapCX) {
		t.Fatalf("codex export not byte-for-byte identical to snapshot!\nExpected: %s\nGot: %s", snapCX, bundle["codex"][0].Token)
	}
	if len(bundle["claude"]) != 1 || !bytes.Equal(bundle["claude"][0].Token, snapCL) {
		t.Fatalf("claude export not byte-for-byte identical to snapshot!\nExpected: %s\nGot: %s", snapCL, bundle["claude"][0].Token)
	}
	if len(bundle["antigravity"]) != 1 || !bytes.Equal(bundle["antigravity"][0].Token, snapAG) {
		t.Fatalf("antigravity export not byte-for-byte identical to snapshot!\nExpected: %s\nGot: %s", snapAG, bundle["antigravity"][0].Token)
	}

	// Import into new managers
	mgrsDst := newTestManagers(t)
	bundleBytes, _ := json.Marshal(bundle)
	rep, err := Import(bundleBytes, mgrsDst)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(rep.Failures()) > 0 {
		t.Fatalf("import failures: %+v", rep.Failures())
	}
	if len(rep.Successes()) != 3 {
		t.Fatalf("expected 3 successes, got %d", len(rep.Successes()))
	}

	var resCX, resCL, resAG *Result
	for _, res := range rep.Successes() {
		resCopy := res
		switch res.Provider {
		case "codex":
			resCX = &resCopy
		case "claude":
			resCL = &resCopy
		case "antigravity":
			resAG = &resCopy
		}
	}
	if resCX == nil || resCL == nil || resAG == nil {
		t.Fatalf("missing expected provider in successes: %+v", rep.Successes())
	}

	// Verify imported snapshots in target store have zero metadata loss
	dstCXSnap, err := mgrsDst["codex"].S.ReadSnapshot(resCX.Account.Key)
	if err != nil {
		t.Fatalf("read dst codex snapshot: %v", err)
	}
	if !bytes.Equal(dstCXSnap, snapCX) {
		t.Fatalf("target codex snapshot corrupted: got %s, want %s", dstCXSnap, snapCX)
	}

	dstCLSnap, err := mgrsDst["claude"].S.ReadSnapshot(resCL.Account.Key)
	if err != nil {
		t.Fatalf("read dst claude snapshot: %v", err)
	}
	if !bytes.Equal(dstCLSnap, snapCL) {
		t.Fatalf("target claude snapshot corrupted: got %s, want %s", dstCLSnap, snapCL)
	}

	dstAGSnap, err := mgrsDst["antigravity"].S.ReadSnapshot(resAG.Account.Key)
	if err != nil {
		t.Fatalf("read dst antigravity snapshot: %v", err)
	}
	if !bytes.Equal(dstAGSnap, snapAG) {
		t.Fatalf("target antigravity snapshot corrupted: got %s, want %s", dstAGSnap, snapAG)
	}
}

func TestBareAPIKeyCompatibility(t *testing.T) {
	mgrs := newTestManagers(t)

	// 1. JSON string bare API Key
	bundleJSON, _ := json.Marshal(Bundle{
		"codex": []AccountItem{
			{Alias: "work-key-1", Token: json.RawMessage(`"sk-proj-1234567890abcdef"`)},
		},
	})
	rep, err := Import(bundleJSON, mgrs)
	if err != nil {
		t.Fatalf("import bare api key: %v", err)
	}
	if len(rep.Failures()) > 0 {
		t.Fatalf("import failures: %+v", rep.Failures())
	}
	if len(rep.Successes()) != 1 {
		t.Fatalf("expected 1 success, got %d", len(rep.Successes()))
	}
	succ := rep.Successes()[0]
	if succ.Account.Alias != "work-key-1" || succ.Account.Email != "sk-...cdef" {
		t.Fatalf("unexpected account: %+v", succ.Account)
	}

	// 2. Export and verify full fidelity
	bundle, err := Export(mgrs)
	if err != nil {
		t.Fatalf("export after bare key import: %v", err)
	}
	if len(bundle["codex"]) != 1 {
		t.Fatalf("expected 1 exported codex account")
	}
	var exportedKey struct {
		AuthMode string `json:"auth_mode"`
		APIKey   string `json:"OPENAI_API_KEY"`
	}
	if err := json.Unmarshal(bundle["codex"][0].Token, &exportedKey); err != nil {
		t.Fatalf("unmarshal exported key: %v", err)
	}
	if exportedKey.AuthMode != "apikey" || exportedKey.APIKey != "sk-proj-1234567890abcdef" {
		t.Fatalf("unexpected exported key: %+v", exportedKey)
	}
}

func TestLegacyTokenCompatibility(t *testing.T) {
	mgrs := newTestManagers(t)

	// 1. Legacy Codex OAuth (flat fields without nested "tokens")
	idTokenCX := fakeJWT(map[string]any{
		"email": "legacy@openai.com",
		"sub":   "sub-legacy",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "org-legacy",
			"chatgpt_user_id":    "u-legacy",
			"chatgpt_plan_type":  "pro",
		},
	})
	legacyCX := []byte(`{"id_token":"` + idTokenCX + `","refresh_token":"rt-legacy","account_id":"org-legacy","access_token":"at-legacy"}`)

	// 2. Legacy Claude (flat structure)
	legacyCL := []byte(`{
		"refreshToken": "rt-legacy-claude",
		"accessToken": "at-legacy-claude",
		"expiresAt": 1735689600.0,
		"subscriptionType": "pro",
		"accountUuid": "uuid-legacy-cl",
		"organizationUuid": "org-legacy-cl",
		"emailAddress": "legacy-cl@example.com"
	}`)

	// 3. Legacy Antigravity (flat structure)
	idTokenAG := fakeJWT(map[string]any{
		"sub":   "sub-legacy-ag",
		"email": "legacy-ag@google.com",
	})
	legacyAG := []byte(`{
		"id_token": "` + idTokenAG + `",
		"refresh_token": "rt-legacy-ag",
		"access_token": "at-legacy-ag",
		"auth_method": "consumer"
	}`)

	bundle, _ := json.Marshal(Bundle{
		"codex":       []AccountItem{{Alias: "cx-leg", Token: legacyCX}},
		"claude":      []AccountItem{{Alias: "cl-leg", Token: legacyCL}},
		"antigravity": []AccountItem{{Alias: "ag-leg", Token: legacyAG}},
	})

	rep, err := Import(bundle, mgrs)
	if err != nil {
		t.Fatalf("import legacy: %v", err)
	}
	if len(rep.Failures()) > 0 {
		t.Fatalf("import legacy failures: %+v", rep.Failures())
	}
	if len(rep.Successes()) != 3 {
		t.Fatalf("expected 3 successes, got %d", len(rep.Successes()))
	}
}

func TestOverwriteImport(t *testing.T) {
	mgrs := newTestManagers(t)

	// Step 1: Initial import with alias "original"
	snap1 := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-proj-same-key"}`)
	bundle1, _ := json.Marshal(Bundle{
		"codex": []AccountItem{{Alias: "original", Token: snap1}},
	})
	rep1, err := Import(bundle1, mgrs)
	if err != nil || len(rep1.Failures()) > 0 {
		t.Fatalf("initial import failed: %v %+v", err, rep1.Failures())
	}
	if rep1.Successes()[0].Account.Alias != "original" {
		t.Fatalf("expected 'original', got %s", rep1.Successes()[0].Account.Alias)
	}

	reg1, _ := mgrs["codex"].S.Load()
	if len(reg1.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(reg1.Accounts))
	}

	// Step 2: Overwrite import with new alias "updated"
	bundle2, _ := json.Marshal(Bundle{
		"codex": []AccountItem{{Alias: "updated", Token: snap1}},
	})
	rep2, err := Import(bundle2, mgrs)
	if err != nil || len(rep2.Failures()) > 0 {
		t.Fatalf("overwrite import failed: %v %+v", err, rep2.Failures())
	}

	reg2, _ := mgrs["codex"].S.Load()
	if len(reg2.Accounts) != 1 {
		t.Fatalf("expected 1 account after overwrite, got %d", len(reg2.Accounts))
	}
	if reg2.Accounts[0].Alias != "updated" {
		t.Fatalf("expected alias to be updated to 'updated', got %s", reg2.Accounts[0].Alias)
	}
}

func TestRoundTripExportImport(t *testing.T) {
	mgrsSrc := newTestManagers(t)

	// Seed src with accounts
	// 1) Codex API Key
	snapCXKey := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-proj-roundtrip-key"}`)
	if _, err := mgrsSrc["codex"].Import(snapCXKey, "cx-key"); err != nil {
		t.Fatalf("seed cx key: %v", err)
	}

	// 2) Codex OAuth
	idTokenCX := fakeJWT(map[string]any{
		"email": "cx-user@roundtrip.com",
		"sub":   "sub-cx",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-cx",
			"chatgpt_user_id":    "user-cx",
			"chatgpt_plan_type":  "pro",
		},
	})
	snapCXOAuth, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idTokenCX,
			"access_token":  "at-cx-roundtrip",
			"refresh_token": "rt-cx-roundtrip",
			"account_id":    "acct-cx",
		},
		"last_refresh": "2026-10-08T00:00:00Z",
	})
	if _, err := mgrsSrc["codex"].Import(snapCXOAuth, "cx-oauth"); err != nil {
		t.Fatalf("seed cx oauth: %v", err)
	}

	// 3) Claude
	credsClaude, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-cl-roundtrip",
			"refreshToken":     "rt-cl-roundtrip",
			"expiresAt":        1735689600.0,
			"subscriptionType": "pro",
		},
	})
	oauthClaude, _ := json.Marshal(map[string]any{
		"accountUuid":  "uuid-cl-roundtrip",
		"emailAddress": "cl-user@roundtrip.com",
	})
	if _, err := mgrsSrc["claude"].Import(claude.Pack(credsClaude, oauthClaude), "cl-alias"); err != nil {
		t.Fatalf("seed claude: %v", err)
	}

	// 4) Antigravity
	idTokenAG := fakeJWT(map[string]any{
		"sub":   "sub-ag-roundtrip",
		"email": "ag-user@roundtrip.com",
	})
	snapAG, _ := json.Marshal(map[string]any{
		"token": map[string]any{
			"refresh_token": "rt-ag-roundtrip",
			"access_token":  "at-ag-roundtrip",
		},
		"id_token":    idTokenAG,
		"auth_method": "consumer",
	})
	if _, err := mgrsSrc["antigravity"].Import(snapAG, "ag-alias"); err != nil {
		t.Fatalf("seed antigravity: %v", err)
	}

	// Export
	bundle, err := Export(mgrsSrc)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(bundle["codex"]) != 2 || len(bundle["claude"]) != 1 || len(bundle["antigravity"]) != 1 {
		t.Fatalf("unexpected export counts: %+v", bundle)
	}

	// Serialize
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	// Target managers
	mgrsDst := newTestManagers(t)

	// Import into target managers
	rep, err := Import(data, mgrsDst)
	if err != nil {
		t.Fatalf("import roundtrip: %v", err)
	}
	if len(rep.Failures()) > 0 {
		t.Fatalf("import failures: %+v", rep.Failures())
	}
	if len(rep.Successes()) != 4 {
		t.Fatalf("expected 4 successes, got %d", len(rep.Successes()))
	}

	// Verify target registries
	regCX, _ := mgrsDst["codex"].S.Load()
	if len(regCX.Accounts) != 2 {
		t.Fatalf("expected 2 codex accounts, got %d", len(regCX.Accounts))
	}
	for _, acct := range regCX.Accounts {
		if acct.Alias == "cx-oauth" {
			snapBytes, err := mgrsDst["codex"].S.ReadSnapshot(acct.Key)
			if err != nil {
				t.Fatalf("read codex snapshot: %v", err)
			}
			if !jsonEqual(t, snapBytes, snapCXOAuth) {
				t.Fatalf("codex oauth snapshot mismatch after roundtrip:\nGot: %s\nWant: %s", snapBytes, snapCXOAuth)
			}
		}
	}

	regCL, _ := mgrsDst["claude"].S.Load()
	if len(regCL.Accounts) != 1 || regCL.Accounts[0].Alias != "cl-alias" {
		t.Fatalf("unexpected claude accounts: %+v", regCL.Accounts)
	}

	regAG, _ := mgrsDst["antigravity"].S.Load()
	if len(regAG.Accounts) != 1 || regAG.Accounts[0].Alias != "ag-alias" {
		t.Fatalf("unexpected antigravity accounts: %+v", regAG.Accounts)
	}
}

func TestReconstructDirect(t *testing.T) {
	// 1. Native snapshot returns as-is
	nativeCX := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-proj-native"}`)
	recon, err := Reconstruct("codex", nativeCX)
	if err != nil {
		t.Fatalf("reconstruct native: %v", err)
	}
	if !bytes.Equal(recon, nativeCX) {
		t.Fatalf("expected identical bytes for native snapshot")
	}

	// 2. Bare string adapts
	reconKey, err := Reconstruct("codex", []byte(`"sk-proj-reconstructed"`))
	if err != nil {
		t.Fatalf("reconstruct bare string: %v", err)
	}
	var parsed struct {
		AuthMode string `json:"auth_mode"`
		APIKey   string `json:"OPENAI_API_KEY"`
	}
	if err := json.Unmarshal(reconKey, &parsed); err != nil || parsed.AuthMode != "apikey" {
		t.Fatalf("unexpected parsed key: %+v", parsed)
	}

	// 3. Empty token fails
	if _, err := Reconstruct("codex", []byte("")); err == nil {
		t.Fatalf("expected error for empty token")
	}
}

func TestErrorCases(t *testing.T) {
	mgrs := newTestManagers(t)

	// 1. Unsupported provider
	bundle, _ := json.Marshal(Bundle{
		"unknown-provider": []AccountItem{{Alias: "foo", Token: []byte(`{}`)}},
	})
	rep, err := Import(bundle, mgrs)
	if err != nil {
		t.Fatalf("unexpected import error: %v", err)
	}
	if len(rep.Failures()) != 1 {
		t.Fatalf("expected 1 failure for unknown provider")
	}

	// 2. Invalid JSON token for valid provider
	bundleInvalid, _ := json.Marshal(Bundle{
		"codex": []AccountItem{{Alias: "bad", Token: []byte(`{"invalid": true}`)}},
	})
	repInvalid, err := Import(bundleInvalid, mgrs)
	if err != nil {
		t.Fatalf("unexpected import error: %v", err)
	}
	if len(repInvalid.Failures()) != 1 {
		t.Fatalf("expected 1 failure for invalid token")
	}

	// 3. Malformed import JSON
	if _, err := Import([]byte(`not json`), mgrs); err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}
