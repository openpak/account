package mii

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"testing"
)

// The Ver3 data the Wii U adapter hands out as its default Mii.
const sampleVer3 = "AwAAQOlVognnx0GC2/uogAOzuI0n2QAAAEBEAGUAZgBhAHUAbAB0AAAAAAAAAEBAAAAhAQJoRBgmNEYUgRIXaA0AACkAUkhQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAGm9"

var seed = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func sample(t *testing.T) (*Mii, []byte) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(sampleVer3)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseVer3(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m, raw
}

func TestVer3RoundTripAndCRC(t *testing.T) {
	m, raw := sample(t)
	out := m.EncodeVer3()
	if !bytes.Equal(out[:94], raw[:94]) {
		t.Fatalf("ver3 re-encode differs:\n%x\n%x", raw, out)
	}
	if got, want := crc16(raw[:94]), uint16(raw[94])<<8|uint16(raw[95]); got != want {
		t.Fatalf("console CRC %04x, computed %04x", want, got)
	}
}

// random builds a Mii inside the range the Wii and the Switch can both hold, so
// every conversion must be lossless.
func random(r *rand.Rand) *Mii {
	n := func(max int) int { return r.Intn(max + 1) }
	m := &Mii{
		Gender: n(1), BirthMonth: n(12), BirthDay: n(31), FavoriteColor: n(11), Favorite: r.Intn(2) == 1,
		Height: n(127), Build: n(127),
		FaceType: n(7), FaceColor: n(5),
		HairType: n(71), HairColor: n(7), HairFlip: r.Intn(2) == 1,
		EyeType: n(47), EyeColor: n(5), EyeScale: n(7), EyeAspect: 3, EyeRotate: n(7), EyeX: n(12), EyeY: n(18),
		EyebrowType: n(23), EyebrowColor: n(7), EyebrowScale: n(8), EyebrowAspect: 3, EyebrowRotate: n(11), EyebrowX: n(12), EyebrowY: 3 + n(15),
		NoseType: n(11), NoseScale: n(8), NoseY: n(18),
		MouthType: n(23), MouthColor: n(3), MouthScale: n(8), MouthAspect: 3, MouthY: n(18),
		MustacheType: n(3), BeardType: n(3), BeardColor: n(7), MustacheScale: n(8), MustacheY: n(16),
		GlassType: n(8), GlassColor: n(5), GlassScale: n(7), GlassY: n(20),
		MoleType: n(1), MoleScale: n(8), MoleX: n(16), MoleY: n(30),
	}
	li := r.Intn(len(rflFaceLineMake))
	m.Wrinkles, m.Makeup = rflFaceLineMake[li][0], rflFaceLineMake[li][1]
	for i := range m.Name {
		m.Name[i] = uint16('A' + r.Intn(26))
		m.Creator[i] = uint16('a' + r.Intn(26))
	}
	m.Header[0] = 3
	return m
}

func same(t *testing.T, a, b *Mii, what string) {
	t.Helper()
	x, y := *a, *b
	x.Header, y.Header = [24]byte{}, [24]byte{}
	if x != y {
		t.Fatalf("%s: %+v != %+v", what, x, y)
	}
}

func TestRoundTripsAreLossless(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		m := random(r)
		if err := m.validate(); err != nil {
			t.Fatal(err)
		}
		v3, err := ParseVer3(m.EncodeVer3())
		if err != nil {
			t.Fatal(err)
		}
		same(t, m, v3, "ver3")
		rfl, err := ParseRFL(m.EncodeRFL(seed))
		if err != nil {
			t.Fatal(err)
		}
		same(t, m, rfl, "rfl")
		ci := m.EncodeCharInfo(seed)
		sw, err := ParseCharInfo(ci)
		if err != nil {
			t.Fatal(err)
		}
		// Switch common colours are many-to-one only for colours Ver3 lacks; Ver3's own
		// colours must survive (hair colour 0 maps via 8 and back to 0).
		// CharInfo has no birthday, creator name, favourite or sharing flag.
		want := *m
		want.BirthMonth, want.BirthDay, want.Creator, want.Favorite, want.DisableSharing = 0, 0, [10]uint16{}, false, false
		same(t, &want, sw, "charinfo")
		// A Mii that passes through the Switch keeps its look on the Wii.
		rfl2, err := ParseRFL(sw.EncodeRFL(seed))
		if err != nil {
			t.Fatal(err)
		}
		same(t, &want, rfl2, "wii->switch->wii")
	}
}

func TestWiiU3DSMiiOnWiiAndSwitchStaysValid(t *testing.T) {
	m, _ := sample(t)
	rfl := m.EncodeRFL(seed)
	if len(rfl) != RFLSize || crc16(rfl[:0x4A]) != uint16(rfl[0x4A])<<8|uint16(rfl[0x4B]) {
		t.Fatal("rfl size or crc")
	}
	if _, err := ParseRFL(rfl); err != nil {
		t.Fatal(err)
	}
	ci := m.EncodeCharInfo(seed)
	if len(ci) != CharInfoSize || !bytes.Equal(ci[:16], seed[:]) {
		t.Fatal("charinfo size or create id")
	}
	if got := string([]byte{ci[0x10], ci[0x12], ci[0x14]}); got != "Def" {
		t.Fatalf("charinfo name %q", got)
	}
	// The name is UTF-16BE on the Wii and UTF-16LE on the Switch.
	if rfl[2] != 0 || rfl[3] != 'D' || ci[0x10] != 'D' || ci[0x11] != 0 {
		t.Fatal("name byte order")
	}
}

func TestPartsTheWiiLacksFoldToDefaults(t *testing.T) {
	m, _ := sample(t)
	m.FaceType, m.HairType, m.MouthColor, m.Wrinkles, m.Makeup = 11, 100, 4, 3, 2
	r, err := ParseRFL(m.EncodeRFL(seed))
	if err != nil {
		t.Fatal(err)
	}
	if r.FaceType != 0 || r.HairType != 0 || r.MouthColor != 0 || r.Wrinkles != 3 || r.Makeup != 0 {
		t.Fatalf("folded: %+v", r)
	}
}

func TestRejectsBadData(t *testing.T) {
	_, raw := sample(t)
	for _, f := range []Format{FormatVer3, FormatRFL, FormatCharInfo} {
		if _, err := Parse(f, raw[:10]); err == nil {
			t.Fatal(f, "short data accepted")
		}
	}
	bad := append([]byte(nil), raw...)
	bad[0] = 9
	if _, err := ParseVer3(bad); err == nil {
		t.Fatal("bad version accepted")
	}
	m, _ := sample(t)
	rfl := m.EncodeRFL(seed)
	rfl[5] ^= 1
	if _, err := ParseRFL(rfl); err == nil {
		t.Fatal("rfl checksum not checked")
	}
	// Garbage in the CharInfo fields is rejected, not stored.
	ci := m.EncodeCharInfo(seed)
	ci[0x34] = 200 // eye type
	if _, err := ParseCharInfo(ci); err == nil {
		t.Fatal("out-of-range charinfo accepted")
	}
}

func TestRFLIDStablePerAccount(t *testing.T) {
	c1, s1 := RFLID(seed)
	c2, s2 := RFLID(seed)
	if c1 != c2 || s1 != s2 || c1[0]>>5 != 1 {
		t.Fatalf("%x %x", c1, s1)
	}
	other := seed
	other[0]++
	if c3, _ := RFLID(other); c3 == c1 {
		t.Fatal("different accounts share a create id")
	}
}
