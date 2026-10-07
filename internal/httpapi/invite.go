package httpapi

import (
	"net/http"

	"lilypad/internal/deltacomm"
)

// inviteReceiveRequest is POST /api/invite/reward/receive.
type inviteReceiveRequest struct {
	Hdr            string `json:"hdr"`
	MasterInviteID int64  `json:"masterInviteId"`
}

// handleInviteRewardReceive answers the invite (友達招待) sender-reward
// refresh. The response echoes the invite's sender-condition rows
// (user_invite_sender_reward), creating zero-count rows for conditions never
// seen before. Nothing is granted here: the official balance does not move on
// this call — issued rewards were paid when the condition was first met, and
// on a private server there are no invitees progressing conditions.
func (s *Server) handleInviteRewardReceive(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req inviteReceiveRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad invite request")
		return
	}
	var condIDs []int64
	if s.master != nil {
		condIDs, _ = s.master.InviteSenderConditionIDs(req.MasterInviteID)
	}
	rows := []deltacomm.Row{}
	var tableHash string
	hashes, err := s.store.MutateUnderLock(r.Context(), userID,
		[]string{"user_invite_sender_reward"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, error) {
			existing := indexByInt(cur["user_invite_sender_reward"], "_masterInviteConditionId")
			var missing []deltacomm.Row
			for _, cid := range condIDs {
				if row, ok := existing[cid]; ok {
					rows = append(rows, row)
					continue
				}
				row := deltacomm.Row{"_masterInviteConditionId": cid, "_rewardIssueCount": 0}
				missing = append(missing, row)
				rows = append(rows, row)
			}
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			if len(missing) > 0 {
				d.PutItems["user_invite_sender_reward"] = missing
			}
			return d, nil
		})
	if err != nil {
		s.log.Error("invite/reward/receive", "err", err)
		s.fail(w, r, 1, "invite receive failed")
		return
	}
	if h, ok := hashes["user_invite_sender_reward"]; ok {
		tableHash = h
	} else {
		// Versions are database-issued tokens, including when no rows changed.
		current, err := s.store.AllHashes(r.Context(), userID)
		if err != nil {
			s.fail(w, r, 1, "invite receive failed")
			return
		}
		tableHash = current["user_invite_sender_reward"]
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables": map[string]any{
			"putItems": map[string]any{"user_invite_sender_reward": rows},
		},
		"hashes": map[string]any{"user_invite_sender_reward": tableHash},
	}))
}
