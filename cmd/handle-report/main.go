// handle-report produces a read-only migration report. Account choices are
// supplied privately; production names and identifiers never enter the repo.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openpak/account/internal/identity"
	"openpak/account/internal/storage"
)

type choice struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
}
type link struct {
	ID        string `json:"link_id"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
}
type snapshot struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Links       []link `json:"links"`
}
type xboxReview struct {
	AccountID    string `json:"account_id"`
	ConsoleBound bool   `json:"console_bound"`
}

type report struct {
	snapshot
	XboxConsoleBound bool     `json:"xbox_console_bound"`
	Handle           string   `json:"handle"`
	NewDisplayName   string   `json:"new_display_name"`
	Changes          []string `json:"changes"`
	Issues           []string `json:"issues"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	choicesPath := flag.String("choices", "", "reviewed account choices JSON (required)")
	snapshotPath := flag.String("snapshot", "", "optional read-only JSON snapshot, instead of DATABASE_URL")
	xboxPath := flag.String("xbox-report", "", "optional read-only Xbox console-binding report JSON")
	flag.Parse()
	data, err := os.ReadFile(*choicesPath)
	if err != nil {
		return err
	}
	var choices map[string]choice
	if err = json.Unmarshal(data, &choices); err != nil {
		return err
	}
	xboxBound := map[string]bool{}
	if *xboxPath != "" {
		data, err = os.ReadFile(*xboxPath)
		if err != nil {
			return err
		}
		var xs []xboxReview
		if err = json.Unmarshal(data, &xs); err != nil {
			return err
		}
		for _, x := range xs {
			xboxBound[x.AccountID] = x.ConsoleBound
		}
	}
	var accounts []snapshot
	if *snapshotPath != "" {
		data, err = os.ReadFile(*snapshotPath)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &accounts); err != nil {
			return err
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pool, err := storage.Connect(ctx, os.Getenv("ACCOUNT_DATABASE_URL"))
		if err != nil {
			return fmt.Errorf("account database unavailable")
		}
		defer pool.Close()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.display_name,COALESCE((SELECT json_agg(json_build_object('link_id',l.id,'namespace',l.namespace,'name',l.public_code,'subject',l.subject_id) ORDER BY l.namespace) FROM adapter_links l WHERE l.account_id=a.id AND l.state='active'),'[]'::json) FROM accounts a WHERE a.status='active' ORDER BY a.created_at`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a snapshot
			var links []byte
			if err = rows.Scan(&a.ID, &a.DisplayName, &links); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(links, &a.Links); err != nil {
				rows.Close()
				return err
			}
			accounts = append(accounts, a)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
	}
	seen := map[string]string{}
	found := map[string]bool{}
	out := make([]report, 0, len(accounts))
	invalid := false
	for _, a := range accounts {
		c, ok := choices[a.ID]
		found[a.ID] = true
		r := report{snapshot: a, XboxConsoleBound: xboxBound[a.ID], Handle: c.Handle, NewDisplayName: c.DisplayName, Changes: []string{}, Issues: []string{}}
		if !ok {
			r.Issues = append(r.Issues, "owner choice missing")
		}
		if err := identity.ValidateHandle(c.Handle); err != nil {
			r.Issues = append(r.Issues, err.Error())
		}
		key := strings.ToLower(c.Handle)
		if owner, ok := seen[key]; ok && owner != a.ID {
			r.Issues = append(r.Issues, "duplicate chosen handle")
		}
		seen[key] = a.ID
		for _, l := range a.Links {
			switch l.Namespace {
			case "wiiu", "xbox", "x360", "np":
				if l.Name != c.Handle {
					r.Changes = append(r.Changes, l.Namespace)
				}
			case "x360-console":
				r.Issues = append(r.Issues, "Xbox 360 console is bound; re-enrolment review required")
			}
		}
		if xboxBound[a.ID] {
			r.Issues = append(r.Issues, "Xbox console is bound; re-enrolment review required")
		}
		for _, other := range accounts {
			if other.ID == a.ID {
				continue
			}
			for _, link := range other.Links {
				if link.Namespace != "wiiu" && link.Namespace != "xbox" && link.Namespace != "x360" && link.Namespace != "np" {
					continue
				}
				normalized := strings.Map(func(r rune) rune {
					if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
						return r
					}
					return -1
				}, strings.ToLower(link.Name))
				if normalized == key {
					r.Issues = append(r.Issues, "chosen handle conflicts with another account's current network name")
				}
			}
		}
		if a.DisplayName != c.DisplayName {
			r.Changes = append(r.Changes, "display_name")
		}
		if len(r.Issues) > 0 {
			invalid = true
		}
		out = append(out, r)
	}
	for id := range choices {
		if !found[id] {
			return fmt.Errorf("choice refers to an account absent from the active snapshot: %s", id)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(out); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("report has unresolved issues; no data was changed")
	}
	return nil
}
