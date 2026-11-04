// Package mii is the account core's Mii store and the format converters
// behind it.
//
// An account owns one primary Mii. It is stored once, in the Ver3 format
// (96-byte store data used by the Wii U and 3DS), because Ver3 carries every
// field the other two formats have. Per-format encodings are computed when the
// Mii is written and cached beside it, so a console adapter asks for the form
// its console speaks and never converts anything itself:
//
//	FormatVer3      96 bytes   Wii U / 3DS FFLStoreData, CRC16 at 0x5E (big endian)
//	FormatRFL       76 bytes   Wii RFLiStoreData: 0x4A bytes of RFLiCharData + CRC16 (big endian)
//	FormatCharInfo  88 bytes   Switch nn::mii::CharInfo
//
// Sources for the layouts: ogws RVLFaceLib (RFLiCharData), the FFL decompilation
// (FFLiMiiDataCoreRFL2CharInfo is the official Wii-to-Ver3 conversion, followed
// here), and Eden's service/mii (Ver3StoreData, CharInfo, and the colour tables
// that map Ver3 colour indexes to the Switch's 100-entry common colour table).
package mii

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Format names one wire form of a Mii.
type Format int

const (
	FormatVer3 Format = iota + 1
	FormatRFL
	FormatCharInfo
)

func (f Format) String() string {
	switch f {
	case FormatVer3:
		return "ver3"
	case FormatRFL:
		return "rfl"
	case FormatCharInfo:
		return "charinfo"
	}
	return fmt.Sprintf("format(%d)", int(f))
}

// Encoded sizes.
const (
	Ver3Size     = 0x60
	RFLSize      = 0x4C // 0x4A of RFLiCharData plus the CRC16
	CharInfoSize = 0x58
)

var ErrInvalid = errors.New("mii: invalid Mii data")

// Mii is the field set shared by all three formats, in Ver3 value ranges.
type Mii struct {
	Name, Creator                                    [10]uint16
	Gender, BirthMonth, BirthDay, FavoriteColor      int
	Favorite                                         bool
	Height, Build                                    int
	FontRegion, RegionLock                           int
	AllowCopying, ProfanityFlag, DisableSharing      bool
	FaceType, FaceColor, Wrinkles, Makeup            int
	HairType, HairColor                              int
	HairFlip                                         bool
	EyeType, EyeColor, EyeScale, EyeAspect           int
	EyeRotate, EyeX, EyeY                            int
	EyebrowType, EyebrowColor, EyebrowScale          int
	EyebrowAspect, EyebrowRotate, EyebrowX, EyebrowY int
	NoseType, NoseScale, NoseY                       int
	MouthType, MouthColor, MouthScale, MouthAspect   int
	MouthY                                           int
	MustacheType, BeardType, BeardColor              int
	MustacheScale, MustacheY                         int
	GlassType, GlassColor, GlassScale, GlassY        int
	MoleType, MoleScale, MoleX, MoleY                int
	// Header is Ver3 bytes 0..23 (version, region bits, Mii id, system id,
	// creation date, creator MAC). It is only kept to round-trip a Ver3 Mii
	// exactly; the other formats never see it.
	Header [24]byte
}

// crc16 is CRC-16/XMODEM (poly 0x1021, init 0), the checksum all Mii formats use.
func crc16(b []byte) uint16 {
	var crc uint16
	for _, v := range b {
		crc ^= uint16(v) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

type limit struct {
	name   string
	v, max int
}

// validate range-checks the Ver3 values (same limits the website's Mii Studio
// package applies to real console data).
func (m *Mii) validate() error {
	for _, c := range []limit{
		{"gender", m.Gender, 1}, {"birth month", m.BirthMonth, 12}, {"birth day", m.BirthDay, 31},
		{"favorite color", m.FavoriteColor, 11}, {"height", m.Height, 127}, {"build", m.Build, 127},
		{"face type", m.FaceType, 11}, {"face color", m.FaceColor, 6}, {"wrinkles", m.Wrinkles, 11},
		{"makeup", m.Makeup, 11}, {"hair type", m.HairType, 131}, {"hair color", m.HairColor, 7},
		{"eye type", m.EyeType, 59}, {"eye color", m.EyeColor, 5}, {"eye scale", m.EyeScale, 7},
		{"eye aspect", m.EyeAspect, 6}, {"eye rotate", m.EyeRotate, 7}, {"eye x", m.EyeX, 12},
		{"eye y", m.EyeY, 18}, {"eyebrow type", m.EyebrowType, 24}, {"eyebrow color", m.EyebrowColor, 7},
		{"eyebrow scale", m.EyebrowScale, 8}, {"eyebrow aspect", m.EyebrowAspect, 6},
		{"eyebrow rotate", m.EyebrowRotate, 11}, {"eyebrow x", m.EyebrowX, 12}, {"eyebrow y", m.EyebrowY, 18},
		{"nose type", m.NoseType, 17}, {"nose scale", m.NoseScale, 8}, {"nose y", m.NoseY, 18},
		{"mouth type", m.MouthType, 35}, {"mouth color", m.MouthColor, 4}, {"mouth scale", m.MouthScale, 8},
		{"mouth aspect", m.MouthAspect, 6}, {"mouth y", m.MouthY, 18}, {"mustache type", m.MustacheType, 5},
		{"beard type", m.BeardType, 5}, {"beard color", m.BeardColor, 7}, {"mustache scale", m.MustacheScale, 8},
		{"mustache y", m.MustacheY, 16}, {"glass type", m.GlassType, 8}, {"glass color", m.GlassColor, 5},
		{"glass scale", m.GlassScale, 7}, {"glass y", m.GlassY, 20}, {"mole scale", m.MoleScale, 8},
		{"mole x", m.MoleX, 16}, {"mole y", m.MoleY, 30},
	} {
		if c.v < 0 || c.v > c.max {
			return fmt.Errorf("%w: %s %d", ErrInvalid, c.name, c.v)
		}
	}
	if m.EyebrowY < 3 {
		return fmt.Errorf("%w: eyebrow y %d", ErrInvalid, m.EyebrowY)
	}
	if m.Name[0] == 0 {
		return fmt.Errorf("%w: empty name", ErrInvalid)
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func clamp(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// ---- Ver3 ------------------------------------------------------------------

// ParseVer3 decodes 96-byte store data. The CRC is not enforced (the console's
// copy is authoritative), the value ranges are.
func ParseVer3(b []byte) (*Mii, error) {
	if len(b) != Ver3Size {
		return nil, fmt.Errorf("%w: ver3 is %d bytes, got %d", ErrInvalid, Ver3Size, len(b))
	}
	if b[0] != 0 && b[0] != 3 {
		return nil, fmt.Errorf("%w: ver3 version %d", ErrInvalid, b[0])
	}
	m := &Mii{}
	copy(m.Header[:], b[:24])
	m.RegionLock = int(b[1] >> 2 & 3)
	m.FontRegion = int(b[1] >> 4 & 3)
	m.AllowCopying = b[1]&1 != 0
	m.ProfanityFlag = b[1]>>1&1 != 0
	info := binary.LittleEndian.Uint16(b[24:])
	m.Gender = int(info & 1)
	m.BirthMonth = int(info >> 1 & 15)
	m.BirthDay = int(info >> 5 & 31)
	m.FavoriteColor = int(info >> 10 & 15)
	m.Favorite = info>>14&1 != 0
	for i := range m.Name {
		m.Name[i] = binary.LittleEndian.Uint16(b[26+2*i:])
		m.Creator[i] = binary.LittleEndian.Uint16(b[72+2*i:])
	}
	m.Height, m.Build = int(b[46]), int(b[47])
	m.DisableSharing = b[48]&1 != 0
	m.FaceType = int(b[48] >> 1 & 15)
	m.FaceColor = int(b[48] >> 5 & 7)
	m.Wrinkles = int(b[49] & 15)
	m.Makeup = int(b[49] >> 4)
	m.HairType = int(b[50])
	m.HairColor = int(b[51] & 7)
	m.HairFlip = b[51]>>3&1 != 0
	e := binary.LittleEndian.Uint32(b[52:])
	m.EyeType, m.EyeColor, m.EyeScale = int(e&63), int(e>>6&7), int(e>>9&15)
	m.EyeAspect, m.EyeRotate = int(e>>13&7), int(e>>16&31)
	m.EyeX, m.EyeY = int(e>>21&15), int(e>>25&31)
	e = binary.LittleEndian.Uint32(b[56:])
	m.EyebrowType, m.EyebrowColor, m.EyebrowScale = int(e&31), int(e>>5&7), int(e>>8&15)
	m.EyebrowAspect, m.EyebrowRotate = int(e>>12&7), int(e>>16&15)
	m.EyebrowX, m.EyebrowY = int(e>>21&15), int(e>>25&31)
	n := binary.LittleEndian.Uint16(b[60:])
	m.NoseType, m.NoseScale, m.NoseY = int(n&31), int(n>>5&15), int(n>>9&31)
	o := binary.LittleEndian.Uint16(b[62:])
	m.MouthType, m.MouthColor, m.MouthScale, m.MouthAspect = int(o&63), int(o>>6&7), int(o>>9&15), int(o>>13&7)
	m.MouthY, m.MustacheType = int(b[64]&31), int(b[64]>>5)
	d := binary.LittleEndian.Uint16(b[66:])
	m.BeardType, m.BeardColor, m.MustacheScale, m.MustacheY = int(d&7), int(d>>3&7), int(d>>6&15), int(d>>10&31)
	g := binary.LittleEndian.Uint16(b[68:])
	m.GlassType, m.GlassColor, m.GlassScale, m.GlassY = int(g&15), int(g>>4&7), int(g>>7&15), int(g>>11&31)
	l := binary.LittleEndian.Uint16(b[70:])
	m.MoleType, m.MoleScale, m.MoleX, m.MoleY = int(l&1), int(l>>1&15), int(l>>5&31), int(l>>10&31)
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// EncodeVer3 writes 96-byte store data with a fresh CRC.
func (m *Mii) EncodeVer3() []byte {
	b := make([]byte, Ver3Size)
	copy(b, m.Header[:])
	b[0] = 3
	b[1] = byte(b2i(m.AllowCopying) | b2i(m.ProfanityFlag)<<1 | m.RegionLock&3<<2 | m.FontRegion&3<<4)
	binary.LittleEndian.PutUint16(b[24:], uint16(m.Gender&1|m.BirthMonth&15<<1|m.BirthDay&31<<5|m.FavoriteColor&15<<10|b2i(m.Favorite)<<14))
	for i := range m.Name {
		binary.LittleEndian.PutUint16(b[26+2*i:], m.Name[i])
		binary.LittleEndian.PutUint16(b[72+2*i:], m.Creator[i])
	}
	b[46], b[47] = byte(m.Height), byte(m.Build)
	b[48] = byte(b2i(m.DisableSharing) | m.FaceType&15<<1 | m.FaceColor&7<<5)
	b[49] = byte(m.Wrinkles&15 | m.Makeup&15<<4)
	b[50] = byte(m.HairType)
	b[51] = byte(m.HairColor&7 | b2i(m.HairFlip)<<3)
	binary.LittleEndian.PutUint32(b[52:], uint32(m.EyeType&63|m.EyeColor&7<<6|m.EyeScale&15<<9|m.EyeAspect&7<<13|m.EyeRotate&31<<16|m.EyeX&15<<21|m.EyeY&31<<25))
	binary.LittleEndian.PutUint32(b[56:], uint32(m.EyebrowType&31|m.EyebrowColor&7<<5|m.EyebrowScale&15<<8|m.EyebrowAspect&7<<12|m.EyebrowRotate&15<<16|m.EyebrowX&15<<21|m.EyebrowY&31<<25))
	binary.LittleEndian.PutUint16(b[60:], uint16(m.NoseType&31|m.NoseScale&15<<5|m.NoseY&31<<9))
	binary.LittleEndian.PutUint16(b[62:], uint16(m.MouthType&63|m.MouthColor&7<<6|m.MouthScale&15<<9|m.MouthAspect&7<<13))
	b[64] = byte(m.MouthY&31 | m.MustacheType&7<<5)
	binary.LittleEndian.PutUint16(b[66:], uint16(m.BeardType&7|m.BeardColor&7<<3|m.MustacheScale&15<<6|m.MustacheY&31<<10))
	binary.LittleEndian.PutUint16(b[68:], uint16(m.GlassType&15|m.GlassColor&7<<4|m.GlassScale&15<<7|m.GlassY&31<<11))
	binary.LittleEndian.PutUint16(b[70:], uint16(m.MoleType&1|m.MoleScale&15<<1|m.MoleX&31<<5|m.MoleY&31<<10))
	binary.BigEndian.PutUint16(b[94:], crc16(b[:94]))
	return b
}

// ---- Wii RFL ---------------------------------------------------------------

// RFL value limits (the Wii Mii Channel's part counts; Ver3 has more parts).
const (
	rflFaceTypes     = 8
	rflHairTypes     = 72
	rflEyebrowTypes  = 24
	rflEyeTypes      = 48
	rflNoseTypes     = 12
	rflMouthTypes    = 24
	rflGlassTypes    = 9
	rflMustacheTypes = 4
	rflBeardTypes    = 4
)

// rflFaceLineMake is the official table (FFL RFL_FACE_LINE_AND_MAKE): the
// Wii's single "facial feature" index to Ver3 {wrinkles, makeup}.
var rflFaceLineMake = [12][2]int{
	{0, 0}, {0, 1}, {0, 6}, {0, 9}, {5, 0}, {2, 0}, {3, 0}, {7, 0}, {8, 0}, {0, 10}, {9, 0}, {11, 0},
}

// RFLID derives the Wii create id and system id for a Mii from the owner's id.
// The Wii format carries both; Ver3 keeps its own in the header, so these are
// synthetic but stable per account. The top three bits of the create id are
// 001 (a normal Mii, never the "special" 100 pattern of the built-in Miis).
func RFLID(seed [16]byte) (createID, systemID [4]byte) {
	h := sha256.Sum256(append([]byte("openpak-mii-rfl-id\x00"), seed[:]...))
	copy(createID[:], h[:4])
	copy(systemID[:], h[4:8])
	createID[0] = createID[0]&0x1F | 0x20
	return
}

// ParseRFL decodes 76 bytes (RFLiStoreData) or 74 bytes (RFLiCharData alone).
// With the CRC present it must match. Following the official Wii-to-Ver3
// conversion (FFLiMiiDataCoreRFL2CharInfo), the three stretch fields the Wii
// has no equivalent for take the neutral value 3.
func ParseRFL(b []byte) (*Mii, error) {
	switch len(b) {
	case RFLSize:
		if crc16(b[:0x4A]) != binary.BigEndian.Uint16(b[0x4A:]) {
			return nil, fmt.Errorf("%w: rfl checksum", ErrInvalid)
		}
	case 0x4A:
	default:
		return nil, fmt.Errorf("%w: rfl is %d or %d bytes, got %d", ErrInvalid, RFLSize, 0x4A, len(b))
	}
	w := func(off int) int { return int(binary.BigEndian.Uint16(b[off:])) }
	m := &Mii{EyeAspect: 3, EyebrowAspect: 3, MouthAspect: 3}
	f := w(0)
	m.Gender, m.BirthMonth, m.BirthDay = f>>14&1, f>>10&15, f>>5&31
	m.FavoriteColor, m.Favorite = f>>1&15, f&1 != 0
	for i := range m.Name {
		m.Name[i] = binary.BigEndian.Uint16(b[2+2*i:])
		m.Creator[i] = binary.BigEndian.Uint16(b[0x36+2*i:])
	}
	m.Height, m.Build = int(b[0x16]), int(b[0x17])
	f = w(0x20)
	m.FaceType, m.FaceColor = f>>13, f>>10&7
	if lm := f >> 6 & 15; lm < len(rflFaceLineMake) {
		m.Wrinkles, m.Makeup = rflFaceLineMake[lm][0], rflFaceLineMake[lm][1]
	}
	m.DisableSharing = f>>2&1 != 0
	f = w(0x22)
	m.HairType, m.HairColor, m.HairFlip = f>>9, f>>6&7, f>>5&1 != 0
	f = w(0x24)
	m.EyebrowType, m.EyebrowRotate = f>>11, f>>6&31
	f = w(0x26)
	m.EyebrowColor, m.EyebrowScale, m.EyebrowY, m.EyebrowX = f>>13, f>>9&15, f>>4&31, f&15
	f = w(0x28)
	m.EyeType, m.EyeRotate, m.EyeY = f>>10, f>>5&31, f&31
	f = w(0x2A)
	m.EyeColor, m.EyeScale, m.EyeX = f>>13, f>>9&15, f>>5&15
	f = w(0x2C)
	m.NoseType, m.NoseScale, m.NoseY = f>>12, f>>8&15, f>>3&31
	f = w(0x2E)
	m.MouthType, m.MouthColor, m.MouthScale, m.MouthY = f>>11, f>>9&3, f>>5&15, f&31
	f = w(0x30)
	m.GlassType, m.GlassColor, m.GlassScale, m.GlassY = f>>12, f>>9&7, f>>5&15, f&31
	f = w(0x32)
	m.MustacheType, m.BeardType, m.BeardColor, m.MustacheScale, m.MustacheY = f>>14, f>>12&3, f>>9&7, f>>5&15, f&31
	f = w(0x34)
	m.MoleType, m.MoleScale, m.MoleY, m.MoleX = f>>15, f>>11&15, f>>6&31, f>>1&31
	m.Header[0] = 3
	// The Ver3 eyebrow y range starts at 3; a lower Wii value is the lowest valid one.
	m.EyebrowY = max(m.EyebrowY, 3)
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func rflFaceLineMakeIndex(wrinkles, makeup int) int {
	best, score := 0, -1
	for i, e := range rflFaceLineMake {
		s := 0
		if e[0] == wrinkles && e[1] == makeup {
			return i
		}
		if e[0] == wrinkles && wrinkles != 0 {
			s = 3
		} else if e[1] == makeup && makeup != 0 && wrinkles == 0 {
			s = 2
		} else if e[0] == 0 && e[1] == 0 {
			s = 1
		}
		if s > score {
			best, score = i, s
		}
	}
	return best
}

// outOf folds a part index the Wii does not have to its first part (the
// default), so a Mii built with 3DS-only parts still loads on a Wii.
func outOf(v, count int) int {
	if v >= count {
		return 0
	}
	return v
}

// EncodeRFL writes 76 bytes. Ver3 values with no Wii counterpart map to the
// nearest valid one: part indexes beyond the Wii's catalogue (face shapes 8-11,
// hair 72+, and so on) become part 0; a Ver3 face with both wrinkles and
// makeup keeps the wrinkles (the Wii has one facial-feature slot); mouth colour
// 4 becomes 0 (the Wii has four lip colours); a face colour above 5 becomes 5.
// The create and system ids come from RFLID(seed).
func (m *Mii) EncodeRFL(seed [16]byte) []byte {
	b := make([]byte, RFLSize)
	put := func(off, v int) { binary.BigEndian.PutUint16(b[off:], uint16(v)) }
	put(0, m.Gender&1<<14|m.BirthMonth&15<<10|m.BirthDay&31<<5|m.FavoriteColor&15<<1|b2i(m.Favorite))
	for i := range m.Name {
		put(2+2*i, int(m.Name[i]))
		put(0x36+2*i, int(m.Creator[i]))
	}
	b[0x16], b[0x17] = byte(m.Height), byte(m.Build)
	cid, sid := RFLID(seed)
	copy(b[0x18:], cid[:])
	copy(b[0x1C:], sid[:])
	put(0x20, outOf(m.FaceType, rflFaceTypes)<<13|clamp(m.FaceColor, 5)<<10|rflFaceLineMakeIndex(m.Wrinkles, m.Makeup)<<6|b2i(m.DisableSharing)<<2)
	put(0x22, outOf(m.HairType, rflHairTypes)<<9|m.HairColor&7<<6|b2i(m.HairFlip)<<5)
	put(0x24, outOf(m.EyebrowType, rflEyebrowTypes)<<11|m.EyebrowRotate&31<<6)
	put(0x26, m.EyebrowColor&7<<13|m.EyebrowScale&15<<9|m.EyebrowY&31<<4|m.EyebrowX&15)
	put(0x28, outOf(m.EyeType, rflEyeTypes)<<10|m.EyeRotate&31<<5|m.EyeY&31)
	put(0x2A, m.EyeColor&7<<13|m.EyeScale&15<<9|m.EyeX&15<<5)
	put(0x2C, outOf(m.NoseType, rflNoseTypes)<<12|m.NoseScale&15<<8|m.NoseY&31<<3)
	mc := m.MouthColor
	if mc > 3 {
		mc = 0
	}
	put(0x2E, outOf(m.MouthType, rflMouthTypes)<<11|mc<<9|m.MouthScale&15<<5|m.MouthY&31)
	gt := outOf(m.GlassType, rflGlassTypes)
	put(0x30, gt<<12|m.GlassColor&7<<9|m.GlassScale&15<<5|m.GlassY&31)
	put(0x32, outOf(m.MustacheType, rflMustacheTypes)<<14|outOf(m.BeardType, rflBeardTypes)<<12|m.BeardColor&7<<9|m.MustacheScale&15<<5|m.MustacheY&31)
	put(0x34, m.MoleType&1<<15|m.MoleScale&15<<11|m.MoleY&31<<6|m.MoleX&31<<1)
	binary.BigEndian.PutUint16(b[0x4A:], crc16(b[:0x4A]))
	return b
}

// ---- Switch CharInfo -------------------------------------------------------

// Colour index tables, Ver3 field value to Switch common colour (Eden
// service/mii raw_data.cpp, from Ryujinx).
var (
	ver3FacelineColor = [8]byte{0, 1, 2, 3, 4, 5, 5, 5}
	ver3HairColor     = [8]byte{8, 1, 2, 3, 4, 5, 6, 7}
	ver3EyeColor      = [6]byte{8, 9, 10, 11, 12, 13}
	ver3MouthColor    = [5]byte{0x13, 0x14, 0x15, 0x16, 0x17}
	ver3GlassColor    = [7]byte{8, 0xe, 0xf, 0x10, 0x11, 0x12, 0}

	// Switch common colour (0..99) back to the nearest Ver3 value.
	fromFaceline = [10]byte{0, 1, 2, 3, 4, 5, 0, 1, 5, 5}
	fromHair     = [100]byte{
		0, 1, 2, 3, 4, 5, 6, 7, 0, 4, 3, 5, 4, 4, 6, 2, 0,
		6, 4, 3, 2, 2, 7, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 2, 2, 2, 4, 4, 4, 4, 4, 4, 4, 0, 0, 4,
		4, 4, 4, 4, 4, 0, 0, 0, 4, 4, 4, 4, 4, 4, 5, 5, 5,
		4, 4, 4, 4, 4, 4, 4, 5, 7, 5, 7, 7, 7, 7, 7, 6, 7,
		7, 7, 7, 7, 3, 7, 7, 7, 7, 7, 0, 4, 4, 4, 4}
	fromEye = [100]byte{
		0, 2, 2, 2, 1, 3, 2, 3, 0, 1, 2, 3, 4, 5, 2, 2, 4,
		2, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 0, 0, 4, 4, 4, 4, 4, 4, 4, 1, 0, 4, 4,
		4, 4, 4, 4, 4, 0, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
		5, 5, 5, 5, 5, 5, 5, 3, 3, 3, 3, 3, 3, 3, 3, 2, 2,
		3, 3, 3, 3, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1}
	fromMouth = [100]byte{
		4, 4, 4, 4, 4, 4, 4, 3, 4, 4, 4, 4, 4, 4, 4, 1, 4,
		4, 4, 0, 1, 2, 3, 4, 4, 2, 3, 3, 4, 4, 4, 4, 1, 4,
		4, 2, 3, 3, 4, 4, 4, 4, 4, 4, 4, 3, 3, 3, 4, 4, 4,
		3, 3, 3, 3, 3, 4, 4, 4, 4, 4, 3, 3, 3, 3, 4, 4, 4,
		4, 3, 3, 3, 3, 3, 3, 4, 4, 3, 3, 3, 3, 3, 3, 4, 3,
		3, 3, 3, 3, 4, 0, 3, 3, 3, 3, 4, 3, 3, 3, 3}
	fromGlass = [100]byte{
		0, 1, 1, 1, 5, 1, 1, 4, 0, 5, 1, 1, 3, 5, 1, 2, 3,
		4, 5, 4, 2, 2, 4, 4, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
		3, 3, 3, 3, 3, 0, 0, 0, 5, 5, 5, 5, 5, 5, 0, 5, 5,
		5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 1, 4,
		4, 4, 4, 4, 4, 4, 4, 4, 4, 5, 5, 5, 5, 5, 5}
	fromGlassType = [20]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 1, 2, 1, 3, 7, 7, 6, 7, 8, 7, 7}
)

// CharInfo byte offsets (nn::mii::CharInfo, 0x58 bytes; every field after the
// name is one byte).
const (
	ciCreateID   = 0x00
	ciName       = 0x10 // 10 UTF-16LE units plus a null terminator
	ciFontRegion = 0x26
)

// EncodeCharInfo writes the 88-byte CharInfo. Ver3 colour indexes become the
// Switch's common colour numbers through the Ver3 tables; the eyebrow y
// position loses Ver3's +3 offset; the CharInfo type and region_move are 0 for
// a Mii that was not made special. create_id is the owner's id, so one Mii
// keeps one identity on the Switch.
func (m *Mii) EncodeCharInfo(seed [16]byte) []byte {
	b := make([]byte, CharInfoSize)
	copy(b[ciCreateID:], seed[:])
	for i, u := range m.Name {
		binary.LittleEndian.PutUint16(b[ciName+2*i:], u)
	}
	v := []int{
		m.FontRegion, m.FavoriteColor, m.Gender, m.Height, m.Build, 0, m.RegionLock, // 0x26..0x2c, ending in type and region_move
		m.FaceType, int(ver3FacelineColor[m.FaceColor&7]), m.Wrinkles, m.Makeup,
		m.HairType, int(ver3HairColor[m.HairColor&7]), b2i(m.HairFlip),
		m.EyeType, int(ver3EyeColor[clamp(m.EyeColor, 5)]), m.EyeScale, m.EyeAspect, m.EyeRotate, m.EyeX, m.EyeY,
		m.EyebrowType, int(ver3HairColor[m.EyebrowColor&7]), m.EyebrowScale, m.EyebrowAspect, m.EyebrowRotate, m.EyebrowX, clamp(m.EyebrowY-3, 15),
		m.NoseType, m.NoseScale, m.NoseY,
		m.MouthType, int(ver3MouthColor[clamp(m.MouthColor, 4)]), m.MouthScale, m.MouthAspect, m.MouthY,
		int(ver3HairColor[m.BeardColor&7]), m.BeardType, m.MustacheType, m.MustacheScale, m.MustacheY,
		m.GlassType, int(ver3GlassColor[clamp(m.GlassColor, 6)]), m.GlassScale, m.GlassY,
		m.MoleType, m.MoleScale, m.MoleX, m.MoleY,
	}
	for i, x := range v {
		b[ciFontRegion+i] = byte(x)
	}
	return b
}

// ParseCharInfo decodes an 88-byte CharInfo into Ver3 values. Switch parts with
// no Ver3 counterpart map to the nearest valid one: common colours through the
// reverse tables, glasses through the glass-type table, eyebrow y gains Ver3's
// +3 offset. The Ver3 header (Mii id, system id, MAC) is synthesised from the
// create id.
func ParseCharInfo(b []byte) (*Mii, error) {
	if len(b) != CharInfoSize {
		return nil, fmt.Errorf("%w: charinfo is %d bytes, got %d", ErrInvalid, CharInfoSize, len(b))
	}
	f := func(i int) int { return int(b[ciFontRegion+i]) }
	col := func(t *[100]byte, v int) int { return int(t[clamp(v, 99)]) }
	m := &Mii{}
	for i := range m.Name {
		m.Name[i] = binary.LittleEndian.Uint16(b[ciName+2*i:])
	}
	m.FontRegion, m.FavoriteColor, m.Gender, m.Height, m.Build = clamp(f(0), 3), f(1), f(2), f(3), f(4)
	m.RegionLock = clamp(f(6), 3)
	m.FaceType = f(7)
	m.FaceColor = int(fromFaceline[clamp(f(8), 9)])
	m.Wrinkles, m.Makeup = f(9), f(10)
	m.HairType, m.HairColor, m.HairFlip = f(11), col(&fromHair, f(12)), f(13) != 0
	m.EyeType, m.EyeColor, m.EyeScale, m.EyeAspect, m.EyeRotate, m.EyeX, m.EyeY = f(14), col(&fromEye, f(15)), f(16), f(17), f(18), f(19), f(20)
	m.EyebrowType, m.EyebrowColor, m.EyebrowScale, m.EyebrowAspect, m.EyebrowRotate, m.EyebrowX = f(21), col(&fromHair, f(22)), f(23), f(24), f(25), f(26)
	m.EyebrowY = f(27) + 3
	m.NoseType, m.NoseScale, m.NoseY = f(28), f(29), f(30)
	m.MouthType, m.MouthColor, m.MouthScale, m.MouthAspect, m.MouthY = f(31), col(&fromMouth, f(32)), f(33), f(34), f(35)
	m.BeardColor, m.BeardType, m.MustacheType, m.MustacheScale, m.MustacheY = col(&fromHair, f(36)), f(37), f(38), f(39), f(40)
	m.GlassType, m.GlassColor, m.GlassScale, m.GlassY = int(fromGlassType[clamp(f(41), 19)]), col(&fromGlass, f(42)), f(43), f(44)
	m.MoleType, m.MoleScale, m.MoleX, m.MoleY = f(45), f(46), f(47), f(48)
	var seed [16]byte
	copy(seed[:], b[ciCreateID:])
	h := sha256.Sum256(append([]byte("openpak-mii-ver3-header\x00"), seed[:]...))
	m.Header[0] = 3
	copy(m.Header[2:12], h[:10]) // Mii id (2) and system id (8)
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Parse decodes any supported format.
func Parse(f Format, b []byte) (*Mii, error) {
	switch f {
	case FormatVer3:
		return ParseVer3(b)
	case FormatRFL:
		return ParseRFL(b)
	case FormatCharInfo:
		return ParseCharInfo(b)
	}
	return nil, fmt.Errorf("%w: unknown format %d", ErrInvalid, int(f))
}

// Encode produces one cached encoding.
func (m *Mii) Encode(f Format, seed [16]byte) []byte {
	switch f {
	case FormatRFL:
		return m.EncodeRFL(seed)
	case FormatCharInfo:
		return m.EncodeCharInfo(seed)
	}
	return m.EncodeVer3()
}
