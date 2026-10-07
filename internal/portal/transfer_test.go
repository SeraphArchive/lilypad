package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lilypad/internal/account"
	"lilypad/internal/gem"
	"lilypad/internal/store"
)

// --- fakes ---

// fakeGameStore is an in-memory store.Store subset for transfer tests; unused
// methods panic via the embedded nil interface.
type fakeGameStore struct {
	store.Store
	tables   map[int64]map[string][]store.Row
	gems     *fakeGemStore
	sessions *fakeSessionStore
}

func newFakeGameStore() *fakeGameStore {
	return &fakeGameStore{tables: map[int64]map[string][]store.Row{}}
}

func (f *fakeGameStore) ExportSnapshot(ctx context.Context, uid int64) (store.Snapshot, error) {
	hashes, err := f.AllHashes(ctx, uid)
	if err != nil {
		return store.Snapshot{}, err
	}
	names := []string{}
	for n := range hashes {
		names = append(names, n)
	}
	tables, err := f.GetTables(ctx, uid, names)
	snapshot := store.Snapshot{Tables: tables, Hashes: hashes}
	if f.gems != nil {
		snapshot.Balance = f.gems.bal[uid]
	}
	return snapshot, err
}
func (f *fakeGameStore) ImportSnapshot(ctx context.Context, uid int64, tables map[string][]store.Row, balance *gem.Balance) error {
	if err := f.ImportTables(ctx, uid, tables); err != nil {
		return err
	}
	if balance != nil {
		if _, err := f.gems.Set(ctx, uid, *balance); err != nil {
			return err
		}
	}
	return f.sessions.InvalidateSessions(ctx, uid)
}

func (f *fakeGameStore) AllHashes(_ context.Context, userID int64) (map[string]string, error) {
	out := map[string]string{}
	for name := range f.tables[userID] {
		out[name] = "h-" + name
	}
	return out, nil
}

func (f *fakeGameStore) GetTables(_ context.Context, userID int64, names []string) (map[string][]store.Row, error) {
	out := map[string][]store.Row{}
	for _, n := range names {
		if rows, ok := f.tables[userID][n]; ok {
			out[n] = rows
		} else {
			out[n] = []store.Row{}
		}
	}
	return out, nil
}

func (f *fakeGameStore) ImportTables(_ context.Context, userID int64, tables map[string][]store.Row) error {
	// Mirrors postgres.Store: the archive replaces the save, it does not merge.
	f.tables[userID] = maps.Clone(tables)
	return nil
}

type fakeGemStore struct {
	gem.Store
	bal map[int64]gem.Balance
}

func (f *fakeGemStore) Get(_ context.Context, userID int64) (gem.Balance, error) {
	return f.bal[userID], nil
}

func (f *fakeGemStore) Set(_ context.Context, userID int64, b gem.Balance) (gem.Balance, error) {
	f.bal[userID] = b
	return b, nil
}

type fakeSessionStore struct {
	invalidated []int64
}

func (f *fakeSessionStore) SessionBase(context.Context, int64) (int64, error) { return 0, nil }
func (f *fakeSessionStore) SetSessionBase(context.Context, int64, int64) error {
	return nil
}
func (f *fakeSessionStore) InvalidateSessions(_ context.Context, userID int64) error {
	f.invalidated = append(f.invalidated, userID)
	return nil
}

func newTransferWeb(t *testing.T) (*Web, http.Handler, *fakeGameStore, *fakeGemStore, *fakeSessionStore) {
	t.Helper()
	svc := NewService(NewMemoryStore(), LogMailer{}, "http://localhost", false)
	web, err := NewWeb(svc, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	game := newFakeGameStore()
	gems := &fakeGemStore{bal: map[int64]gem.Balance{}}
	sess := &fakeSessionStore{}
	game.gems, game.sessions = gems, sess
	web.EnableTransfer(TransferDeps{
		Accounts: account.NewMemory(0),
		Game:     game,
		Gems:     gems,
		Sessions: sess,
	})
	return web, web.Routes(), game, gems, sess
}

func loginAs(t *testing.T, web *Web, h http.Handler, email string) *http.Cookie {
	t.Helper()
	if _, err := web.svc.Register(context.Background(), email, "password123"); err != nil {
		t.Fatal(err)
	}
	return loginCookie(t, h, email, "password123")
}

// --- tests ---

func TestTransferRoutesDisabledWithoutDeps(t *testing.T) {
	web, h := newTestWeb(t)
	c := loginAs(t, web, h, "nope@b.com")
	for _, path := range []string{"/transfer", "/transfer/export"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s without deps: status=%d, want 404", path, rec.Code)
		}
	}
}

func TestTransferPageShowsSaveSummary(t *testing.T) {
	web, h, game, gems, _ := newTransferWeb(t)
	c := loginAs(t, web, h, "p@b.com")
	// The export/import identity chain: email -> XUID -> userID. Resolve it the
	// same way the handlers do.
	userID := resolveTestUserID(t, web)
	game.tables[userID] = map[string][]store.Row{
		"user_profile": {{"_userName": "TestPlayer", "_rank": json.Number("42")}},
		"user_version": {{"_bundleVersion": "6.9.0"}},
		"user_item":    {{"_id": 1}, {"_id": 2}},
	}
	gems.bal[userID] = gem.Balance{Free: 1200, Paid: 30}

	req := httptest.NewRequest(http.MethodGet, "/transfer", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"TestPlayer", "6.9.0", "1200 free + 30 paid"} {
		if !strings.Contains(body, want) {
			t.Fatalf("transfer page missing %q", want)
		}
	}
}

func resolveTestUserID(t *testing.T, web *Web) int64 {
	t.Helper()
	cred, err := web.svc.Account(context.Background(), "p@b.com")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := web.transfer.Accounts.GetOrCreate(context.Background(), cred.XUID)
	if err != nil {
		t.Fatal(err)
	}
	return acc.UserID
}

func TestExportDownloadRoundTrips(t *testing.T) {
	web, h, game, gems, _ := newTransferWeb(t)
	c := loginAs(t, web, h, "p@b.com")
	userID := resolveTestUserID(t, web)
	game.tables[userID] = map[string][]store.Row{
		"user_profile": {{"_userName": "Exporter", "_rank": json.Number("7")}},
		"user_card":    {{"_masterCardId": json.Number("1001101")}},
	}
	gems.bal[userID] = gem.Balance{Free: 500, Paid: 10}

	req := httptest.NewRequest(http.MethodGet, "/transfer/export", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status=%d", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("export not an attachment: %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("save download is cacheable")
	}
	var doc exportDoc
	dec := json.NewDecoder(rec.Body)
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("export not decodable: %v", err)
	}
	if doc.Format != exportFormat {
		t.Fatalf("format=%q", doc.Format)
	}
	if len(doc.Tables["user_card"]) != 1 {
		t.Fatalf("user_card rows=%d", len(doc.Tables["user_card"]))
	}
	pb, _ := doc.Extras["paymentBalance"].(map[string]any)
	if pb == nil {
		t.Fatal("export missing quartz balance")
	}
	if n, _ := pb["balance_free_gem"].(json.Number).Int64(); n != 500 {
		t.Fatalf("free gems=%v", pb["balance_free_gem"])
	}
	if doc.Account["userName"] != "Exporter" {
		t.Fatalf("account.userName=%v", doc.Account["userName"])
	}
}

func importRequest(t *testing.T, cookie *http.Cookie, csrf, payload string, withGems bool) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("archive", "save.json")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(payload))
	mw.WriteField("csrf", csrf)
	if withGems {
		mw.WriteField("with_gems", "on")
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/transfer/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	return req
}

func TestImportOverwritesSaveAndGems(t *testing.T) {
	web, h, game, gems, sess := newTransferWeb(t)
	c := loginAs(t, web, h, "p@b.com")
	userID := resolveTestUserID(t, web)
	// A table the archive does not carry must not survive the import.
	game.tables[userID] = map[string][]store.Row{
		"user_item":    {{"_id": json.Number("1")}},
		"user_mission": {{"_masterMissionId": json.Number("9"), "_state": json.Number("3")}},
	}
	if len(sess.invalidated) != 0 {
		t.Fatal("session kicked before import")
	}

	// grab the CSRF token off the transfer page
	req := httptest.NewRequest(http.MethodGet, "/transfer", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	csrf := extractCSRF(rec.Body.String())
	if csrf == "" {
		t.Fatal("no csrf on transfer page")
	}

	// official-style archive: balances as decimal strings, extra members present
	payload := `{
	 "format": "lilypad-export/1",
	 "account": {"userId": 1000001, "userName": "TestPlayer"},
	 "tables": {
	  "user_item": [{"_id": 7000001, "_num": 5}],
	  "user_card": [{"_masterCardId": 1001101, "_limitBreakLevel": 2}]
	 },
	 "officialHashes": {"user_item": "deadbeef"},
	 "extras": {"paymentBalance": {"balance_free_gem": "1200", "balance_charge_gem": "30"}}
	}`
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, importRequest(t, c, csrf, payload, true))
	body := rec2.Body.String()
	if !strings.Contains(body, "Imported 2 tables") {
		t.Fatalf("missing import flash: %s", body)
	}
	if !strings.Contains(body, "1200 free + 30 paid") {
		t.Fatalf("missing gem flash: %s", body)
	}
	got := game.tables[userID]
	if len(got) != 2 || len(got["user_card"]) != 1 {
		t.Fatalf("store tables=%v", got)
	}
	if _, kept := got["user_mission"]; kept {
		t.Fatalf("table absent from the archive survived the import: %v", got)
	}
	// int64 id must survive as a number, not float-mangled
	row := got["user_item"][0]
	if n, ok := row["_id"].(json.Number); !ok || n.String() != "7000001" {
		t.Fatalf("_id=%v (%T)", row["_id"], row["_id"])
	}
	if b := gems.bal[userID]; b.Free != 1200 || b.Paid != 30 {
		t.Fatalf("gems=%+v", b)
	}
	if len(sess.invalidated) != 1 || sess.invalidated[0] != userID {
		t.Fatalf("import must kick live sessions, got %v", sess.invalidated)
	}
}

func TestImportRejectsBadArchive(t *testing.T) {
	web, h, game, _, _ := newTransferWeb(t)
	c := loginAs(t, web, h, "p@b.com")
	userID := resolveTestUserID(t, web)

	req := httptest.NewRequest(http.MethodGet, "/transfer", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	csrf := extractCSRF(rec.Body.String())

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, importRequest(t, c, csrf, `{"format":"other/9","tables":{}}`, false))
	if !strings.Contains(rec2.Body.String(), "unsupported archive format") {
		t.Fatalf("expected format error, got %s", rec2.Body.String())
	}
	if len(game.tables[userID]) != 0 {
		t.Fatal("rejected archive must not touch the store")
	}
}

func TestImportRequiresCSRF(t *testing.T) {
	web, h, game, _, _ := newTransferWeb(t)
	c := loginAs(t, web, h, "p@b.com")
	userID := resolveTestUserID(t, web)
	payload := `{"format":"lilypad-export/1","tables":{"user_item":[{"_id":1}]}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importRequest(t, c, "wrong-token", payload, false))
	if !strings.Contains(rec.Body.String(), "invalid session") {
		t.Fatalf("expected csrf error, got %s", rec.Body.String())
	}
	if len(game.tables[userID]) != 0 {
		t.Fatal("csrf-failed import must not touch the store")
	}
}
