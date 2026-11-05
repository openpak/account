// totp-rekey moves the sealed two-factor secrets to a new ACCOUNT_TOTP_KEY. Stop the account
// service first; run it without -apply to see the counts, with -apply to write them (one
// transaction, all or nothing), then start the service on the new key.
//
//	ACCOUNT_DATABASE_URL   the account database
//	ACCOUNT_TOTP_KEY       the new key (at least 32 bytes)
//	ACCOUNT_TOTP_KEY_OLD   the keys rows may be under now, comma separated: the previous
//	                       ACCOUNT_TOTP_KEY, and the session pepper (ACCOUNT_SESSION_SECRET)
//	                       for rows sealed before ACCOUNT_TOTP_KEY existed
//
// It prints counts only, never a key or a secret.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"openpak/account/internal/identity"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "totp-rekey:", err)
		os.Exit(1)
	}
}

func run() error {
	apply := flag.Bool("apply", false, "write the re-sealed secrets; default only reports")
	flag.Parse()
	url, newKey := os.Getenv("ACCOUNT_DATABASE_URL"), os.Getenv("ACCOUNT_TOTP_KEY")
	if url == "" || newKey == "" {
		return fmt.Errorf("ACCOUNT_DATABASE_URL and ACCOUNT_TOTP_KEY are required")
	}
	var olds []string
	for _, k := range strings.Split(os.Getenv("ACCOUNT_TOTP_KEY_OLD"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			olds = append(olds, k)
		}
	}
	if len(olds) == 0 {
		return fmt.Errorf("ACCOUNT_TOTP_KEY_OLD is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := identity.RekeyTOTP(ctx, pool, newKey, olds, *apply)
	fmt.Printf("totp-rekey: apply=%v rows=%d already-on-new-key=%d from-old-keys=%v err=%v\n", *apply, rep.Total, rep.AlreadyNew, rep.FromOld, err)
	return err
}
