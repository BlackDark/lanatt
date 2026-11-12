// Package claimlist is the read-only view of claims behind `lanatt claims`. It judges liveness
// through functions passed in, so it never reads /proc or the state file itself.
package claimlist

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"text/tabwriter"

	"lanatt/internal/claims"
)

// Row is a claim plus its liveness. JSON is the claim fields with alive, reason and owner_comm.
type Row struct {
	claims.Claim
	Alive     bool   `json:"alive"`
	Reason    string `json:"reason"`
	OwnerComm string `json:"owner_comm"`
}

// Rows judges each claim: reason is "" for a live claim, and comm names the owner pid.
func Rows(s claims.State, reason func(claims.Claim) string, comm func(pid int) string) []Row {
	out := make([]Row, 0, len(s.Claims))
	for _, c := range s.Claims {
		r := reason(c)
		row := Row{Claim: c, Alive: r == "", Reason: r}
		if row.Alive {
			row.OwnerComm = comm(c.OwnerPid)
		}
		out = append(out, row)
	}
	return out
}

// WriteTable prints rows as a table. boot is the current boot id, to label claims from others.
func WriteTable(w io.Writer, rows []Row, boot string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUID\tNETWORK\tADDRESS\tMAC\tIFACE\tNETNS\tBOOT\tOWNER\tREASON")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			claims.ShortID(r.ID), r.UID, r.Network, claims.Addresses(r.Claim), r.MAC, r.Interface, r.Netns,
			bootLabel(r.BootID, boot), owner(r), reasonOrDash(r.Reason))
	}
	return tw.Flush()
}

// WriteJSON prints rows as an array.
func WriteJSON(w io.Writer, rows []Row) error {
	if rows == nil {
		rows = []Row{}
	}
	b, err := json.Marshal(rows, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func bootLabel(claimBoot, boot string) string {
	switch {
	case claimBoot == "":
		return "unknown"
	case claimBoot == boot:
		return "this"
	default:
		return "previous"
	}
}

func owner(r Row) string {
	if !r.Alive {
		return "none"
	}
	if r.OwnerComm == "" {
		return fmt.Sprint(r.OwnerPid)
	}
	return fmt.Sprintf("%d/%s", r.OwnerPid, r.OwnerComm)
}

func reasonOrDash(reason string) string {
	if reason == "" {
		return "-"
	}
	return reason
}
