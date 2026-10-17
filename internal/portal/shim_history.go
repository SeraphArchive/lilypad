package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"lilypad/internal/gem"
)

// HistoryProvider resolves a platform XUID to its committed game transactions.
// A configured balance provider without history support must fail, not conceal it.
type HistoryProvider interface {
	GemHistory(ctx context.Context, xuid string, offset, limit int) ([]gem.Transaction, bool, error)
}

type paymentTransaction struct {
	Type         string  `json:"transaction_type"`
	Date         string  `json:"date"`
	Reason       string  `json:"reason"`
	Paid         string  `json:"balance_change_charge"`
	Free         string  `json:"balance_change_free"`
	ExpireCharge *string `json:"expire_charge,omitempty"`
	ExpireFree   *string `json:"expire_free,omitempty"`
}

var paymentHistoryJST = time.FixedZone("JST", 9*3600)

func historyParameter(query url.Values, key string, fallback int) (int, error) {
	values, present := query[key]
	if !present {
		return fallback, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, errors.New("invalid history parameter")
	}
	for _, c := range values[0] {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid history parameter")
		}
	}
	n, err := strconv.ParseUint(values[0], 10, 31)
	return int(n), err
}

func (sh *Shim) history(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
		return
	}
	offset, err := historyParameter(query, "offset", 0)
	limit, limitErr := historyParameter(query, "limit", 20)
	if err != nil || limitErr != nil || gem.ValidateHistoryPage(offset, limit) != nil {
		writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
		return
	}
	xuid, err := sh.xuidFromRequest(r)
	if err != nil || xuid == "" {
		writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
		return
	}
	entries := []gem.Transaction{}
	hasNext := false
	if sh.balances != nil {
		provider, ok := sh.balances.(HistoryProvider)
		if !ok {
			writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
			return
		}
		entries, hasNext, err = provider.GemHistory(r.Context(), xuid, offset, limit)
		if err != nil {
			sh.log.Warn("gem history read failed", "xuid", xuid, "err", err)
			writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
			return
		}
	}
	transactions := make([]paymentTransaction, 0, len(entries))
	for _, entry := range entries {
		tx, err := paymentHistoryTransaction(entry)
		if err != nil {
			sh.log.Warn("invalid gem history", "err", err)
			writeNG(w, clientInternalErrorCode, clientInternalErrorMessage)
			return
		}
		transactions = append(transactions, tx)
	}
	writeJSON(w, map[string]any{"result": "OK", "entry": map[string]any{
		"transactions": transactions, "offset": strconv.Itoa(offset), "limit": strconv.Itoa(limit), "has_next": hasNext,
	}})
}

func paymentHistoryTransaction(entry gem.Transaction) (paymentTransaction, error) {
	valid := !entry.Date.IsZero()
	switch entry.Type {
	case gem.TransactionConsume:
		valid = valid && entry.Free <= 0 && entry.Paid <= 0 && (entry.Free < 0 || entry.Paid < 0)
	case gem.TransactionIssue:
		valid = valid && entry.Free > 0 && entry.Paid == 0
	default:
		valid = false
	}
	if !valid {
		return paymentTransaction{}, errors.New("invalid gem transaction")
	}
	raw, err := json.Marshal(entry.Reason)
	if err != nil {
		return paymentTransaction{}, err
	}
	tx := paymentTransaction{
		Type: strconv.Itoa(entry.Type), Date: entry.Date.In(paymentHistoryJST).Format("2006-01-02T15:04:05-0700"),
		Reason: string(raw), Free: strconv.FormatInt(entry.Free, 10), Paid: strconv.FormatInt(entry.Paid, 10),
	}
	if entry.Type == gem.TransactionIssue {
		empty := ""
		tx.ExpireCharge, tx.ExpireFree = &empty, &empty
	}
	return tx, nil
}
