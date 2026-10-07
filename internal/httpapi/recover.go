package httpapi

import (
	"errors"
	"net/http"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/store"
)

type recoverRequest struct {
	Hdr                    string `json:"hdr"`
	RecoveryNum            int64  `json:"recoveryNum"`
	ConsumedHC             int64  `json:"consumedHc"`
	RecoverConsumeType     int    `json:"recoverConsumeType"`
	MasterRecoverItemLabel string `json:"masterRecoverItemLabel"`
	ConsumeItemCount       int64  `json:"consumeItemCount"`
}

func (s *Server) handleStaminaRecover(w http.ResponseWriter, r *http.Request) {
	s.handleResourceRecover(w, r, "user_life", defaultLifeRow)
}

func (s *Server) handleSpiritRecover(w http.ResponseWriter, r *http.Request) {
	s.handleResourceRecover(w, r, "user_spirit", defaultSpiritRow)
}

func (s *Server) handleLifeRecover(w http.ResponseWriter, r *http.Request) {
	s.handleResourceRecover(w, r, "user_life", defaultLifeRow)
}

func defaultLifeRow() deltacomm.Row {
	return deltacomm.Row{"_num": int64(0), "_step": 0, "_completeRecoveredAt": int64(0), "_extraLifeElapsedTime": int64(0)}
}

func defaultSpiritRow() deltacomm.Row {
	return deltacomm.Row{"_num": int64(0), "_step": 0, "_completeRecoveredAt": int64(0)}
}

func (s *Server) handleResourceRecover(w http.ResponseWriter, r *http.Request, table string, blank func() deltacomm.Row) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req recoverRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad recover request")
		return
	}
	if req.RecoveryNum <= 0 {
		s.fail(w, r, 1, "invalid recoveryNum")
		return
	}
	if req.RecoveryNum > 10000 || req.ConsumedHC < 0 || req.ConsumeItemCount < 0 {
		s.fail(w, r, 1, "invalid recovery amounts")
		return
	}
	if req.MasterRecoverItemLabel != "" && (s.master == nil || req.ConsumeItemCount <= 0) {
		s.fail(w, r, 1, "item recovery unavailable")
		return
	}
	put := map[string]any{}
	read := []string{table}
	if req.MasterRecoverItemLabel != "" && req.ConsumeItemCount > 0 {
		read = append(read, "user_item")
	}
	hashes, err := s.store.MutateWithGems(r.Context(), userID, read,
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			var gch store.GemChange
			if req.ConsumedHC > 0 {
				gch.Consume = req.ConsumedHC
			}
			if req.MasterRecoverItemLabel != "" && req.ConsumeItemCount > 0 && s.master != nil {
				itemID, ok := s.master.ItemID(req.MasterRecoverItemLabel)
				if !ok {
					return d, store.GemChange{}, errInsufficientItems
				}
				if itemCount(cur["user_item"], itemID) < req.ConsumeItemCount {
					return d, store.GemChange{}, errInsufficientItems
				}
				rows := applyItemConsume(cur["user_item"], itemID, req.ConsumeItemCount)
				if len(rows) > 0 {
					d.PutItems["user_item"] = rows
					put["user_item"] = rows
				}
			}
			row := blank()
			if len(cur[table]) > 0 {
				row = cur[table][0]
			}
			if err := addRowReward(row, "_num", req.RecoveryNum); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			d.PutItems[table] = []deltacomm.Row{row}
			put[table] = d.PutItems[table]
			return d, gch, nil
		})
	if err != nil {
		if errors.Is(err, gem.ErrInsufficient) {
			s.fail(w, r, 1, "insufficient gems")
			return
		}
		if errors.Is(err, errInsufficientItems) {
			s.fail(w, r, 1, "insufficient items")
			return
		}
		s.log.Error("recover", "table", table, "err", err)
		s.fail(w, r, 1, "recover failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(put, nil, hashes, nil))
}
