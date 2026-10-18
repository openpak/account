// handle-migrate applies the privately reviewed alpha choices. It never runs
// on service startup. Stop enrollment and backfills; migrate local adapter
// names under the same maintenance window. See docs/handles.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openpak/account/internal/events"
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	planPath := flag.String("choices", "", "reviewed private choices JSON")
	snapshotPath := flag.String("snapshot", "", "reviewed private pre-migration snapshot JSON")
	apply := flag.Bool("apply", false, "write the reviewed choices; default is read-only validation")
	flag.Parse()
	var choices map[string]choice
	if err := readJSON(*planPath, &choices); err != nil {
		return err
	}
	var snapshots []snapshot
	if err := readJSON(*snapshotPath, &snapshots); err != nil {
		return err
	}
	previous := map[string]snapshot{}
	for _, a := range snapshots {
		if _, ok := previous[a.ID]; ok {
			return fmt.Errorf("duplicate snapshot account")
		}
		previous[a.ID] = a
	}
	if len(choices) == 0 || len(choices) != len(previous) {
		return fmt.Errorf("choices and snapshot must cover the same active accounts")
	}
	ids := make([]string, 0, len(choices))
	handles := map[string]bool{}
	for id, c := range choices {
		if _, ok := previous[id]; !ok {
			return fmt.Errorf("choice absent from snapshot")
		}
		if err := identity.ValidateHandle(c.Handle); err != nil {
			return fmt.Errorf("invalid reviewed handle: %w", err)
		}
		if strings.TrimSpace(c.DisplayName) != c.DisplayName || len(c.DisplayName) == 0 || len(c.DisplayName) > 32 {
			return fmt.Errorf("invalid reviewed display name")
		}
		key := strings.ToLower(c.Handle)
		if handles[key] {
			return fmt.Errorf("duplicate reviewed handle")
		}
		handles[key] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := storage.Connect(ctx, os.Getenv("ACCOUNT_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer pool.Close()
	opts := pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}
	if *apply {
		opts = pgx.TxOptions{}
	}
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if *apply {
		if _, err = tx.Exec(ctx, `LOCK TABLE accounts,adapter_links IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE status='active'`).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("active account set changed; refresh and review the report")
	}
	ev := events.New(pool)
	for _, id := range ids {
		c := choices[id]
		old := previous[id]
		query := `SELECT COALESCE(handle,''),display_name FROM accounts WHERE id=$1 AND status='active'`
		if *apply {
			query += ` FOR UPDATE`
		}
		var current, name string
		if err = tx.QueryRow(ctx, query, id).Scan(&current, &name); err != nil {
			return fmt.Errorf("reviewed account is no longer active")
		}
		if current != "" && current != c.Handle {
			return fmt.Errorf("an account already owns another immutable handle")
		}
		if name != old.DisplayName && name != c.DisplayName {
			return fmt.Errorf("account name changed since review")
		}
		for _, l := range old.Links {
			if l.Namespace != "wiiu" && l.Namespace != "xbox" && l.Namespace != "x360" && l.Namespace != "np" {
				continue
			}
			if l.ID == "" {
				return fmt.Errorf("snapshot lacks link IDs; export a fresh read-only snapshot")
			}
			var subject, code string
			if err = tx.QueryRow(ctx, `SELECT subject_id,COALESCE(public_code,'') FROM adapter_links WHERE id=$1 AND account_id=$2 AND namespace=$3 AND state='active'`, l.ID, id, l.Namespace).Scan(&subject, &code); err != nil {
				return fmt.Errorf("network link changed since review")
			}
			if subject != l.Subject && !(l.Namespace == "np" && subject == c.Handle) {
				return fmt.Errorf("network subject changed since review")
			}
			if code != l.Name && code != c.Handle {
				return fmt.Errorf("network name changed since review")
			}
		}
		if !*apply {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE accounts SET handle=$2,display_name=$3,updated_at=now() WHERE id=$1`, id, c.Handle, c.DisplayName); err != nil {
			return fmt.Errorf("handle claim failed: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE adapter_links SET public_code=$2,public_code_key=lower($2),subject_id=CASE WHEN namespace='np' THEN $2 ELSE subject_id END,updated_at=now()
   WHERE account_id=$1 AND state='active' AND namespace IN ('wiiu','xbox','x360','np')`, id, c.Handle); err != nil {
			return fmt.Errorf("network identity migration failed: %w", err)
		}
		if current == "" {
			if err = ev.EmitTx(ctx, tx, "handle_assigned", id, "", ""); err != nil {
				return err
			}
		}
	}
	if !*apply {
		fmt.Printf("Validated %d reviewed accounts; no writes.\n", len(ids))
		return nil
	}
	var enforced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='accounts'::regclass AND conname='accounts_active_handle')`).Scan(&enforced); err != nil {
		return err
	}
	if !enforced {
		if _, err = tx.Exec(ctx, `ALTER TABLE accounts ADD CONSTRAINT accounts_active_handle CHECK(status <> 'active' OR handle IS NOT NULL)`); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("Applied %d reviewed accounts; existing link IDs and numeric subjects preserved.\n", len(ids))
	return nil
}
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
