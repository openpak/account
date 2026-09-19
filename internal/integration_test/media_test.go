//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountv1 "openpak/account/proto/openpak/account/v1"
)

func testPNG(t *testing.T, side int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	img.Set(0, 0, color.RGBA{R: 1, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// CR-2: the website stores both processed sizes; adapters fetch by account id.
func TestAvatars(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	mc := accountv1.NewMediaClient(lastConn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", grpcInternalKey)
	acct, _, _ := ident.Register(ctx, "avatar@example.com", "correct horse battery", "Av")

	if _, err := mc.GetAvatar(ctx, &accountv1.GetAvatarRequest{AccountId: acct.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("no avatar yet: %v", err)
	}
	small, large := testPNG(t, 256, 10), testPNG(t, 512, 10)
	put, err := mc.PutAvatar(ctx, &accountv1.PutAvatarRequest{AccountId: acct.ID, Png_256: small, Png_512: large})
	if err != nil || put.GetVersion() == "" {
		t.Fatalf("put: %+v %v", put, err)
	}
	for size, want := range map[uint32][]byte{0: large, 512: large, 256: small} {
		got, err := mc.GetAvatar(ctx, &accountv1.GetAvatarRequest{AccountId: acct.ID, Size: size})
		if err != nil || !bytes.Equal(got.GetPng(), want) || got.GetVersion() != put.GetVersion() || got.GetNotModified() {
			t.Fatalf("get %d: %v (version %q)", size, err, got.GetVersion())
		}
	}
	nm, err := mc.GetAvatar(ctx, &accountv1.GetAvatarRequest{AccountId: acct.ID, IfNoneMatch: put.GetVersion()})
	if err != nil || !nm.GetNotModified() || len(nm.GetPng()) != 0 {
		t.Fatalf("conditional get: %+v %v", nm, err)
	}
	// A new picture is a new version.
	put2, err := mc.PutAvatar(ctx, &accountv1.PutAvatarRequest{AccountId: acct.ID, Png_256: testPNG(t, 256, 200), Png_512: testPNG(t, 512, 200)})
	if err != nil || put2.GetVersion() == put.GetVersion() {
		t.Fatalf("replace: %+v %v", put2, err)
	}

	for name, req := range map[string]*accountv1.PutAvatarRequest{
		"swapped sizes": {AccountId: acct.ID, Png_256: large, Png_512: small},
		"not a png":     {AccountId: acct.ID, Png_256: []byte("GIF89a"), Png_512: large},
		"missing size":  {AccountId: acct.ID, Png_512: large},
		"too big":       {AccountId: acct.ID, Png_256: append(append([]byte{}, small...), make([]byte, 256<<10)...), Png_512: large},
	} {
		if _, err := mc.PutAvatar(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := mc.GetAvatar(ctx, &accountv1.GetAvatarRequest{AccountId: acct.ID, Size: 128}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad size: %v", err)
	}
	if _, err := mc.PutAvatar(ctx, &accountv1.PutAvatarRequest{AccountId: "00000000-0000-4000-8000-000000000000", Png_256: small, Png_512: large}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown account: %v", err)
	}

	// Region nodes read but never write.
	fed := accountv1.NewFederationClient(lastConn)
	node, err := fed.CreateNode(ctx, &accountv1.CreateNodeRequest{NodeId: "media-node", Name: "M", Country: "FR"})
	if err != nil {
		t.Fatal(err)
	}
	region := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", node.GetKey())
	if _, err := mc.GetAvatar(region, &accountv1.GetAvatarRequest{AccountId: acct.ID}); err != nil {
		t.Fatalf("region read: %v", err)
	}
	if _, err := mc.DeleteAvatar(region, &accountv1.DeleteAvatarRequest{AccountId: acct.ID}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("region delete: %v", err)
	}

	// Deletion hides the avatar.
	if err := ident.RequestDeletion(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.GetAvatar(ctx, &accountv1.GetAvatarRequest{AccountId: acct.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("avatar of deletion-pending account: %v", err)
	}
	if _, err := mc.DeleteAvatar(ctx, &accountv1.DeleteAvatarRequest{AccountId: acct.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.DeleteAvatar(ctx, &accountv1.DeleteAvatarRequest{AccountId: acct.ID}); err != nil {
		t.Fatalf("delete is idempotent: %v", err)
	}
}
