// Save-data import/export for the Portal: download the account's full save as
// a lilypad-export/1 archive, or upload a compatible archive to overwrite the
// save. Identity is anchored
// on the Portal credential: email -> XUID -> game account.
package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"lilypad/internal/account"
	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/store"
)

// exportFormat identifies the supported save archive schema.
const exportFormat = "lilypad-export/1"

// maxArchiveBytes caps an import upload (a veteran full save is ~5 MB).
const maxArchiveBytes = 64 << 20

// TransferDeps wires the optional save-transfer feature. Game is the DeltaComm
// save store; Gems (optional) enables quartz-balance import/export; Sessions
// (optional) kicks a live client after an out-of-band write so it 401s and
// re-pulls the rewritten save.
type TransferDeps struct {
	Accounts account.Provider
	Game     store.Store
	Gems     gem.Store
	Sessions store.SessionStore
}

// EnableTransfer turns on the /transfer routes. Without it they 404 and the
// nav entry stays hidden (e.g. in-memory dev mode has no save store).
func (web *Web) EnableTransfer(d TransferDeps) {
	web.transfer = &d
}

// exportDoc is the on-disk archive. Extra members are ignored on import;
// table-version tokens are always generated server-side.
type exportDoc struct {
	Format     string                 `json:"format"`
	ExportedAt int64                  `json:"exportedAt"`
	Account    map[string]any         `json:"account"`
	Tables     map[string][]store.Row `json:"tables"`
	Hashes     map[string]string      `json:"hashes"`
	Extras     map[string]any         `json:"extras,omitempty"`
	Stats      map[string]any         `json:"stats"`
}

// saveSummary is the transfer-page view of the current save.
type saveSummary struct {
	HasSave       bool
	Tables        int
	Rows          int
	UserName      string
	Rank          any
	BundleVersion string
	GemFree       int64
	GemPaid       int64
}

// resolveUserID maps the logged-in Portal identity to the game account,
// creating it (seeded) on first contact.
func (web *Web) resolveUserID(ctx context.Context, email string) (int64, error) {
	cred, err := web.svc.Account(ctx, email)
	if err != nil {
		return 0, err
	}
	acc, err := web.transfer.Accounts.GetOrCreate(ctx, cred.XUID)
	if err != nil {
		return 0, err
	}
	return acc.UserID, nil
}

func (web *Web) summarizeSave(ctx context.Context, userID int64) (*saveSummary, error) {
	sum := &saveSummary{}
	snapshot, err := web.snapshot(ctx, userID)
	if err != nil {
		return sum, err
	}
	if len(snapshot.Tables) == 0 {
		return sum, nil
	}
	tables := snapshot.Tables
	sum.HasSave = true
	sum.Tables = len(tables)
	for _, rows := range tables {
		sum.Rows += len(rows)
	}
	if prof := firstRow(tables["user_profile"]); prof != nil {
		sum.UserName, _ = prof["_userName"].(string)
		sum.Rank = prof["_rank"]
	}
	if ver := firstRow(tables["user_version"]); ver != nil {
		sum.BundleVersion, _ = ver["_bundleVersion"].(string)
	}
	if web.transfer.Gems != nil {
		sum.GemFree, sum.GemPaid = snapshot.Balance.Free, snapshot.Balance.Paid
	}
	return sum, nil
}

func firstRow(rows []store.Row) store.Row {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func (web *Web) snapshot(ctx context.Context, uid int64) (store.Snapshot, error) {
	archives, ok := web.transfer.Game.(store.ArchiveStore)
	if !ok {
		return store.Snapshot{}, fmt.Errorf("store does not support atomic save transfers")
	}
	return archives.ExportSnapshot(ctx, uid)
}

func (web *Web) getTransfer(w http.ResponseWriter, r *http.Request) {
	sess := web.current(r)
	userID, err := web.resolveUserID(r.Context(), sess.email)
	if err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "could not resolve game account"})
		return
	}
	summary, err := web.summarizeSave(r.Context(), userID)
	if err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "could not read save; try again"})
		return
	}
	web.render(w, r, "transfer", viewData{Nav: "transfer", Save: summary})
}

func (web *Web) getExport(w http.ResponseWriter, r *http.Request) {
	sess := web.current(r)
	userID, err := web.resolveUserID(r.Context(), sess.email)
	if err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "could not resolve game account"})
		return
	}
	snapshot, err := web.snapshot(r.Context(), userID)
	if err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "could not read save"})
		return
	}
	tables, hashes := snapshot.Tables, snapshot.Hashes

	doc := exportDoc{
		Format:     exportFormat,
		ExportedAt: time.Now().Unix(),
		Account:    map[string]any{"userId": userID},
		Tables:     tables,
		Hashes:     hashes,
		Stats:      map[string]any{},
	}
	if prof := firstRow(tables["user_profile"]); prof != nil {
		doc.Account["userName"] = prof["_userName"]
		doc.Account["rank"] = prof["_rank"]
		doc.Account["registeredAt"] = prof["_registeredAt"]
		doc.Account["country"] = prof["_country"]
		doc.Account["language"] = prof["_language"]
	}
	if ver := firstRow(tables["user_version"]); ver != nil {
		doc.Account["bundleVersion"] = ver["_bundleVersion"]
	}
	rows, empty := 0, 0
	for _, t := range tables {
		rows += len(t)
		if len(t) == 0 {
			empty++
		}
	}
	doc.Stats["tableCount"] = len(tables)
	doc.Stats["rowCount"] = rows
	doc.Stats["emptyTables"] = empty
	if web.transfer.Gems != nil {
		b := snapshot.Balance
		doc.Extras = map[string]any{"paymentBalance": map[string]any{
			"balance_free_gem":   b.Free,
			"balance_charge_gem": b.Paid,
			"balance_total_gem":  b.Total(),
		}}
	}

	raw, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "could not encode archive"})
		return
	}
	name := fmt.Sprintf("lilypad-export-%d-%s.json", userID, time.Now().Format("20060102"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	_, _ = w.Write(raw)
}

func (web *Web) postImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxArchiveBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		web.render(w, r, "transfer", viewData{Nav: "transfer", Error: "invalid or oversized archive upload"})
		return
	}
	defer r.MultipartForm.RemoveAll()
	sess := web.current(r)
	redir := func(vd viewData) {
		vd.Nav = "transfer"
		if userID, err := web.resolveUserID(r.Context(), sess.email); err == nil {
			var readErr error
			vd.Save, readErr = web.summarizeSave(r.Context(), userID)
			if readErr != nil && vd.Error == "" {
				vd.Error = "Could not refresh the save summary; try again."
			}
		}
		web.render(w, r, "transfer", vd)
	}
	if !web.checkCSRF(r) {
		redir(viewData{Error: "invalid session, try again"})
		return
	}

	file, _, err := r.FormFile("archive")
	if err != nil {
		redir(viewData{Error: "choose an archive file to upload"})
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxArchiveBytes+1))
	if err != nil || len(raw) > maxArchiveBytes {
		redir(viewData{Error: "could not read upload"})
		return
	}
	tables, extras, err := parseArchive(raw)
	if err != nil {
		redir(viewData{Error: err.Error()})
		return
	}

	userID, err := web.resolveUserID(r.Context(), sess.email)
	if err != nil {
		redir(viewData{Error: "could not resolve game account"})
		return
	}
	var balance *gem.Balance
	if r.FormValue("with_gems") == "on" && web.transfer.Gems != nil {
		if _, present := extras["paymentBalance"]; present {
			free, paid, ok := paymentBalanceFrom(extras)
			if !ok {
				redir(viewData{Error: "invalid quartz balance in archive"})
				return
			}
			balance = &gem.Balance{Free: free, Paid: paid}
		}
	}
	archives, ok := web.transfer.Game.(store.ArchiveStore)
	if !ok {
		redir(viewData{Error: "atomic save transfer unavailable"})
		return
	}
	if err := archives.ImportSnapshot(r.Context(), userID, tables, balance); err != nil {
		web.log.Error("save import", "user", userID, "err", err)
		redir(viewData{Error: "import failed while writing the save"})
		return
	}

	rows := 0
	for _, t := range tables {
		rows += len(t)
	}
	msg := fmt.Sprintf("Imported %d tables (%d rows).", len(tables), rows)

	// Optional quartz balance (Gree wallet): official archives carry it as
	// decimal strings under extras.paymentBalance.
	if balance != nil {
		msg += fmt.Sprintf(" Quartz set to %d free + %d paid.", balance.Free, balance.Paid)
	}
	web.log.Info("save imported", "user", userID, "tables", len(tables), "rows", rows)
	redir(viewData{Flash: msg})
}

// parseArchive validates a lilypad-export/1 payload. Numbers decode as
// json.Number so 64-bit ids and floats survive the round trip bit-exactly
// (same treatment as the new-player seed).
func parseArchive(raw []byte) (map[string][]store.Row, map[string]any, error) {
	var doc struct {
		Format string                 `json:"format"`
		Tables map[string][]store.Row `json:"tables"`
		Extras map[string]any         `json:"extras"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("not a valid JSON archive: %v", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("archive must contain exactly one JSON document")
	}
	if doc.Format != exportFormat {
		return nil, nil, fmt.Errorf("unsupported archive format %q (want %s)", doc.Format, exportFormat)
	}
	if len(doc.Tables) == 0 {
		return nil, nil, fmt.Errorf("archive contains no tables")
	}
	for name, rows := range doc.Tables {
		if err := deltacomm.ValidateArchiveRows(name, rows); err != nil {
			return nil, nil, err
		}
		for i, row := range rows {
			if row == nil {
				return nil, nil, fmt.Errorf("table %s row %d is not an object", name, i)
			}
		}
	}
	return doc.Tables, doc.Extras, nil
}

// paymentBalanceFrom extracts free/paid quartz from extras.paymentBalance,
// tolerating the official string form ("15200") and LilyPad's numeric form.
func paymentBalanceFrom(extras map[string]any) (free, paid int64, ok bool) {
	pb, _ := extras["paymentBalance"].(map[string]any)
	if pb == nil {
		return 0, 0, false
	}
	free, okF := balanceField(pb["balance_free_gem"])
	paid, okP := balanceField(pb["balance_charge_gem"])
	b := gem.Balance{Free: free, Paid: paid}
	return free, paid, okF && okP && b.Validate() == nil
}

func balanceField(v any) (int64, bool) {
	switch t := v.(type) {
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t >= float64(math.MaxInt64) || math.Trunc(t) != t {
			return 0, false
		}
		return int64(t), true
	}
	return 0, false
}
