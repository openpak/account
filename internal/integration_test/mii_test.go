//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

const wiiUMii = "AwAAQOlVognnx0GC2/uogAOzuI0n2QAAAEBEAGUAZgBhAHUAbAB0AAAAAAAAAEBAAAAhAQJoRBgmNEYUgRIXaA0AACkAUkhQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAGm9"

// One Mii per account, written by one console network and read by all.
func TestMiiSharedAcrossConsoles(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	mc := accountv1.NewMiiClient(lastConn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	acct, _, _ := ident.Register(ctx, "mii@example.com", "correct horse battery", "Mi")
	ver3, _ := base64.StdEncoding.DecodeString(wiiUMii)

	if _, err := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_RFL}); status.Code(err) != codes.NotFound {
		t.Fatalf("no Mii yet: %v", err)
	}
	put, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_VER3, Data: ver3})
	if err != nil || !put.GetChanged() || put.GetVersion() == "" {
		t.Fatalf("put: %+v %v", put, err)
	}
	again, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_VER3, Data: ver3})
	if err != nil || again.GetChanged() || again.GetVersion() != put.GetVersion() {
		t.Fatalf("identical put must be a no-op: %+v %v", again, err)
	}
	sizes := map[accountv1.MiiFormat]int{accountv1.MiiFormat_MII_FORMAT_VER3: 96, accountv1.MiiFormat_MII_FORMAT_RFL: 76, accountv1.MiiFormat_MII_FORMAT_CHAR_INFO: 88}
	var rfl []byte
	for f, n := range sizes {
		got, err := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: f})
		if err != nil || len(got.GetData()) != n || got.GetName() != "Default" || got.GetVersion() != put.GetVersion() {
			t.Fatalf("get %v: %d bytes, name %q, %v", f, len(got.GetData()), got.GetName(), err)
		}
		if f == accountv1.MiiFormat_MII_FORMAT_RFL {
			rfl = got.GetData()
		}
	}
	nm, err := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_RFL, IfNoneMatch: put.GetVersion()})
	if err != nil || !nm.GetNotModified() || len(nm.GetData()) != 0 {
		t.Fatalf("conditional get: %+v %v", nm, err)
	}

	// A Wii writing the Mii back it just read changes nothing.
	same, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_RFL, Data: rfl})
	if err != nil || same.GetChanged() {
		t.Fatalf("rfl round trip changed the Mii: %+v %v", same, err)
	}
	// A Switch writing a changed Mii replaces it for every console.
	ci, _ := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_CHAR_INFO})
	edited := bytes.Clone(ci.GetData())
	edited[0x29]++ // height
	if p, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_CHAR_INFO, Data: edited}); err != nil || !p.GetChanged() {
		t.Fatalf("charinfo put: %+v %v", p, err)
	}
	w, _ := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_RFL})
	if w.GetData()[0x16] != rfl[0x16]+1 {
		t.Fatalf("Switch edit not visible on Wii: height %d, was %d", w.GetData()[0x16], rfl[0x16])
	}

	if _, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_VER3, Data: []byte("short")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad data: %v", err)
	}
	if _, err := mc.PutMii(ctx, &accountv1.PutMiiRequest{AccountId: "00000000-0000-0000-0000-000000000000", Format: accountv1.MiiFormat_MII_FORMAT_VER3, Data: ver3}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := mc.DeleteMii(ctx, &accountv1.DeleteMiiRequest{AccountId: acct.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.GetMii(ctx, &accountv1.GetMiiRequest{AccountId: acct.ID, Format: accountv1.MiiFormat_MII_FORMAT_VER3}); status.Code(err) != codes.NotFound {
		t.Fatalf("after delete: %v", err)
	}
}
