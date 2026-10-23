//go:build integration

package integration_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

// CR-5: search by every identifier the core holds, and a ban that kills the
// live session, emits the existing event and leaves an audit row.
func TestModerationSearchAndBan(t *testing.T) {
	idc, lc, _, _, _, evc, ident := startGRPC(t)
	mod := accountv1.NewModerationClient(lastConn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)

	target, _, err := ident.Register(ctx, "Target.Person@example.com", "correct horse battery", "Targeted")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ident.Register(ctx, "bystander@example.com", "correct horse battery", "Bystander"); err != nil {
		t.Fatal(err)
	}
	res, err := lc.ReserveLink(ctx, &accountv1.ReserveLinkRequest{AccountId: target.ID, Namespace: "switch", SubjectId: "sw-subject-77", IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lc.ActivateLink(ctx, &accountv1.ActivateLinkRequest{LinkId: res.LinkId, IdempotencyKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	sess, _, err := ident.Login(ctx, "target.person@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"target.person", "TARGET", "targeted", target.ID, "sw-subject-77"} {
		got, err := mod.SearchAccounts(ctx, &accountv1.SearchAccountsRequest{Query: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(got.GetAccounts()) != 1 || got.GetAccounts()[0].GetAccountId() != target.ID {
			t.Fatalf("search %q: %+v", q, got.GetAccounts())
		}
		if a := got.GetAccounts()[0]; a.GetEmail() != "Target.Person@example.com" || a.GetStatus() != accountv1.AccountStatus_ACCOUNT_STATUS_ACTIVE {
			t.Fatalf("projection: %+v", a)
		}
	}
	if got, _ := mod.SearchAccounts(ctx, &accountv1.SearchAccountsRequest{Query: "sw-subject-77"}); len(got.GetAccounts()[0].GetMatchedSubjects()) != 1 ||
		got.GetAccounts()[0].GetMatchedSubjects()[0] != "switch:sw-subject-77" {
		t.Fatalf("matched subjects: %+v", got.GetAccounts()[0])
	}
	// LIKE metacharacters are literal.
	if got, _ := mod.SearchAccounts(ctx, &accountv1.SearchAccountsRequest{Query: "%"}); len(got.GetAccounts()) != 0 {
		t.Fatalf("%% matched everything: %+v", got.GetAccounts())
	}
	if _, err := mod.SearchAccounts(ctx, &accountv1.SearchAccountsRequest{Query: "  "}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty query: %v", err)
	}

	if _, err := mod.SetAccountBanned(ctx, &accountv1.SetAccountBannedRequest{AccountId: target.ID, Banned: true}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ban without reason/actor: %v", err)
	}
	before, _ := evc.PollEvents(ctx, &accountv1.PollEventsRequest{})
	got, err := mod.SetAccountBanned(ctx, &accountv1.SetAccountBannedRequest{AccountId: target.ID, Banned: true, Reason: "cheating in ranked", Actor: "admin-1"})
	if err != nil || got.GetStatus() != accountv1.AccountStatus_ACCOUNT_STATUS_BANNED {
		t.Fatalf("ban: %+v %v", got, err)
	}
	if _, err := ident.SessionAccount(ctx, sess); err == nil {
		t.Fatal("ban must revoke the live web session")
	}
	after, _ := evc.PollEvents(ctx, &accountv1.PollEventsRequest{SinceVersion: before.GetMaxVersion()})
	if len(after.GetEvents()) != 1 || after.GetEvents()[0].GetType() != "account_banned" || after.GetEvents()[0].GetAccountId() != target.ID {
		t.Fatalf("events after ban: %+v", after.GetEvents())
	}
	if acct, err := idc.GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: target.ID}); err != nil || acct.GetStatus() != accountv1.AccountStatus_ACCOUNT_STATUS_BANNED {
		t.Fatalf("status after ban: %+v %v", acct, err)
	}
	if found, _ := mod.SearchAccounts(ctx, &accountv1.SearchAccountsRequest{Query: target.ID}); found.GetAccounts()[0].GetBanReason() != "cheating in ranked" {
		t.Fatalf("ban reason: %+v", found.GetAccounts()[0])
	}

	if _, err := mod.SetAccountBanned(ctx, &accountv1.SetAccountBannedRequest{AccountId: target.ID, Banned: false, Reason: "appeal upheld", Actor: "admin-2"}); err != nil {
		t.Fatal(err)
	}
	after2, _ := evc.PollEvents(ctx, &accountv1.PollEventsRequest{SinceVersion: after.GetMaxVersion()})
	if len(after2.GetEvents()) != 1 || after2.GetEvents()[0].GetType() != "account_unbanned" {
		t.Fatalf("events after unban: %+v", after2.GetEvents())
	}
	rows, err := ident.Pool().Query(ctx, `SELECT action, reason, actor FROM moderation_actions WHERE account_id=$1 ORDER BY created_at`, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var audit []string
	for rows.Next() {
		var a, r, who string
		_ = rows.Scan(&a, &r, &who)
		audit = append(audit, a+"|"+r+"|"+who)
	}
	rows.Close()
	if len(audit) != 2 || audit[0] != "ban|cheating in ranked|admin-1" || audit[1] != "unban|appeal upheld|admin-2" {
		t.Fatalf("audit: %v", audit)
	}

	// Unbanning must never revive an account being deleted.
	if err := ident.RequestDeletion(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.SetAccountBanned(ctx, &accountv1.SetAccountBannedRequest{AccountId: target.ID, Banned: false, Reason: "r", Actor: "a"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unban of deletion-pending: %v", err)
	}
	if _, err := mod.SetAccountBanned(ctx, &accountv1.SetAccountBannedRequest{AccountId: "nope", Banned: true, Reason: "r", Actor: "a"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown id: %v", err)
	}
}

// A region node's key may file reports but not moderate.
func TestModerationHomeOnly(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	mod := accountv1.NewModerationClient(lastConn)
	fed := accountv1.NewFederationClient(lastConn)
	home := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	node, err := fed.CreateNode(home, &accountv1.CreateNodeRequest{NodeId: "eu-pending", Name: "EU pending", Country: "DE"})
	if err != nil {
		t.Fatal(err)
	}
	pending := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", node.GetKey())
	if _, err := mod.ListReports(pending, &accountv1.ListReportsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unapproved node: %v", err)
	}
	region := admittedNode(t, "eu-test")
	if _, err := mod.SearchAccounts(region, &accountv1.SearchAccountsRequest{Query: "x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("region search: %v", err)
	}
	if _, err := mod.ListReports(region, &accountv1.ListReportsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("region list: %v", err)
	}
	a, _, _ := ident.Register(home, "r1@example.com", "correct horse battery", "R1")
	b, _, _ := ident.Register(home, "r2@example.com", "correct horse battery", "R2")
	if _, err := mod.FileReport(region, &accountv1.FileReportRequest{ReporterId: a.ID, SubjectAccountId: b.ID, Kind: "other"}); err != nil {
		t.Fatalf("region file: %v", err)
	}
	if _, err := mod.SearchAccounts(context.Background(), &accountv1.SearchAccountsRequest{Query: "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no key: %v", err)
	}
}

// CR-6: file, dedupe, list, resolve once.
func TestModerationReports(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	mod := accountv1.NewModerationClient(lastConn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	reporter, _, _ := ident.Register(ctx, "reporter@example.com", "correct horse battery", "Reporter")
	subject, _, _ := ident.Register(ctx, "subject@example.com", "correct horse battery", "Subject")

	first, err := mod.FileReport(ctx, &accountv1.FileReportRequest{ReporterId: reporter.ID, SubjectAccountId: subject.ID, Kind: "harassment", Detail: "  said rude things  "})
	if err != nil {
		t.Fatal(err)
	}
	r := first.GetReport()
	if r.GetStatus() != accountv1.ReportStatus_REPORT_STATUS_OPEN || r.GetDetail() != "said rude things" ||
		r.GetReporterDisplayName() != "Reporter" || r.GetSubjectDisplayName() != "Subject" || r.GetCreatedAtUnix() == 0 {
		t.Fatalf("filed: %+v", r)
	}
	again, err := mod.FileReport(ctx, &accountv1.FileReportRequest{ReporterId: reporter.ID, SubjectAccountId: subject.ID, Kind: "harassment", Detail: "again"})
	if err != nil || again.GetReport().GetReportId() != r.GetReportId() {
		t.Fatalf("duplicate open report must return the first: %+v %v", again, err)
	}
	if _, err := mod.FileReport(ctx, &accountv1.FileReportRequest{ReporterId: reporter.ID, SubjectAccountId: subject.ID, Kind: "cheating"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*accountv1.FileReportRequest{
		{ReporterId: reporter.ID, SubjectAccountId: reporter.ID, Kind: "other"},
		{ReporterId: reporter.ID, SubjectAccountId: subject.ID, Kind: "Not A Slug"},
		{ReporterId: "x", SubjectAccountId: subject.ID, Kind: "other"},
	} {
		if _, err := mod.FileReport(ctx, bad); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bad report %+v: %v", bad, err)
		}
	}
	if _, err := mod.FileReport(ctx, &accountv1.FileReportRequest{ReporterId: reporter.ID, SubjectAccountId: "00000000-0000-4000-8000-000000000000", Kind: "other"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown subject: %v", err)
	}

	open, err := mod.ListReports(ctx, &accountv1.ListReportsRequest{Status: accountv1.ReportStatus_REPORT_STATUS_OPEN})
	if err != nil || len(open.GetReports()) != 2 || open.GetReports()[0].GetReportId() != r.GetReportId() {
		t.Fatalf("open queue (oldest first): %+v %v", open.GetReports(), err)
	}
	if _, err := mod.ResolveReport(ctx, &accountv1.ResolveReportRequest{ReportId: r.GetReportId(), Actor: "admin"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("resolve without resolution: %v", err)
	}
	res, err := mod.ResolveReport(ctx, &accountv1.ResolveReportRequest{ReportId: r.GetReportId(), Resolution: "warned", Actor: "admin-9"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.GetReport(); got.GetStatus() != accountv1.ReportStatus_REPORT_STATUS_RESOLVED || got.GetResolution() != "warned" ||
		got.GetResolvedBy() != "admin-9" || got.GetResolvedAtUnix() == 0 {
		t.Fatalf("resolved: %+v", got)
	}
	if _, err := mod.ResolveReport(ctx, &accountv1.ResolveReportRequest{ReportId: r.GetReportId(), Resolution: "again", Actor: "admin"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second resolve: %v", err)
	}
	if _, err := mod.ResolveReport(ctx, &accountv1.ResolveReportRequest{ReportId: "00000000-0000-4000-8000-000000000000", Resolution: "x", Actor: "a"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown report: %v", err)
	}
	resolved, _ := mod.ListReports(ctx, &accountv1.ListReportsRequest{Status: accountv1.ReportStatus_REPORT_STATUS_RESOLVED})
	all, _ := mod.ListReports(ctx, &accountv1.ListReportsRequest{})
	if len(resolved.GetReports()) != 1 || len(all.GetReports()) != 2 {
		t.Fatalf("resolved %d all %d", len(resolved.GetReports()), len(all.GetReports()))
	}
	// Once resolved, the same complaint can be filed afresh.
	fresh, err := mod.FileReport(ctx, &accountv1.FileReportRequest{ReporterId: reporter.ID, SubjectAccountId: subject.ID, Kind: "harassment"})
	if err != nil || fresh.GetReport().GetReportId() == r.GetReportId() {
		t.Fatalf("refile after resolve: %+v %v", fresh, err)
	}
}
