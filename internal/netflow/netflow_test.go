package netflow

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func be16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }
func be32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }

func TestV5(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	p := be16(nil, 5)
	p = be16(p, 1)
	p = be32(p, 100_000) // uptime ms
	p = be32(p, uint32(now.Unix()))
	p = be32(p, 0)
	p = be32(p, 1)
	p = append(p, 0, 0, 0, 0) // engine, sampling
	r := []byte{192, 168, 1, 10, 8, 8, 8, 8, 0, 0, 0, 0}
	r = be16(r, 1)
	r = be16(r, 2)
	r = be32(r, 10)     // pkts
	r = be32(r, 15000)  // bytes
	r = be32(r, 40_000) // first
	r = be32(r, 99_000) // last
	r = be16(r, 51000)
	r = be16(r, 443)
	r = append(r, 0, 0x18, 6, 0, 0, 0, 0, 0, 24, 0, 0, 0)
	p = append(p, r...)

	fl, err := NewDecoder().Decode("x", p)
	if err != nil || len(fl) != 1 {
		t.Fatalf("got %v %v", fl, err)
	}
	f := fl[0]
	if f.Src != netip.MustParseAddr("192.168.1.10") || f.Dst != netip.MustParseAddr("8.8.8.8") || f.Bytes != 15000 || f.Proto != 6 || f.DstPort != 443 {
		t.Fatalf("bad flow %+v", f)
	}
	if got := now.Sub(f.End); got != time.Second {
		t.Fatalf("end offset %v", got)
	}
	if got := f.End.Sub(f.Start); got != 59*time.Second {
		t.Fatalf("duration %v", got)
	}
}

func v9Packet(now time.Time, withTemplate bool) []byte {
	var sets []byte
	if withTemplate {
		tpl := be16(nil, 256)
		tpl = be16(tpl, 7)
		for _, f := range [][2]uint16{{fSrcAddr4, 4}, {fDstAddr4, 4}, {fInBytes, 8}, {fProtocol, 1}, {fSrcPort, 2}, {fDstPort, 2}, {fLastSwitched, 4}} {
			tpl = be16(tpl, f[0])
			tpl = be16(tpl, f[1])
		}
		sets = be16(sets, 0)
		sets = be16(sets, uint16(4+len(tpl)))
		sets = append(sets, tpl...)
	}
	rec := []byte{8, 8, 4, 4, 10, 0, 20, 5}
	rec = binary.BigEndian.AppendUint64(rec, 5_000_000_000) // > 32 bits
	rec = append(rec, 17)
	rec = be16(rec, 53)
	rec = be16(rec, 40000)
	rec = be32(rec, 1000)
	body := append(rec, 0, 0, 0) // padding
	sets = be16(sets, 256)
	sets = be16(sets, uint16(4+len(body)))
	sets = append(sets, body...)

	p := be16(nil, 9)
	p = be16(p, 2)
	p = be32(p, 1000)
	p = be32(p, uint32(now.Unix()))
	p = be32(p, 1)
	p = be32(p, 7) // source id
	return append(p, sets...)
}

func TestV9Templates(t *testing.T) {
	d := NewDecoder()
	now := time.Now()
	// data before template is dropped
	if fl, _ := d.Decode("a", v9Packet(now, false)); len(fl) != 0 || d.Missing != 1 {
		t.Fatalf("expected drop, got %v missing=%d", fl, d.Missing)
	}
	fl, err := d.Decode("a", v9Packet(now, true))
	if err != nil || len(fl) != 1 {
		t.Fatalf("got %v %v", fl, err)
	}
	if fl[0].Bytes != 5_000_000_000 || fl[0].Dst.String() != "10.0.20.5" || fl[0].SrcPort != 53 {
		t.Fatalf("bad flow %+v", fl[0])
	}
	// template is remembered per exporter
	if fl, _ := d.Decode("a", v9Packet(now, false)); len(fl) != 1 {
		t.Fatal("template not cached")
	}
	if fl, _ := d.Decode("b", v9Packet(now, false)); len(fl) != 0 {
		t.Fatal("template leaked across exporters")
	}
}

func TestIPFIXVarLenAndEnterprise(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	tpl := be16(nil, 300)
	tpl = be16(tpl, 6)
	tpl = be16(tpl, fSrcAddr6)
	tpl = be16(tpl, 16)
	tpl = be16(tpl, fDstAddr6)
	tpl = be16(tpl, 16)
	tpl = be16(tpl, fOctetTotal)
	tpl = be16(tpl, 4)
	tpl = be16(tpl, 0x8000|99) // enterprise field
	tpl = be16(tpl, 2)
	tpl = be32(tpl, 12345)
	tpl = be16(tpl, 82) // interfaceName, variable length
	tpl = be16(tpl, varLen)
	tpl = be16(tpl, fEndMilli)
	tpl = be16(tpl, 8)

	src := netip.MustParseAddr("2001:db8::10").As16()
	dst := netip.MustParseAddr("2606:4700::1").As16()
	rec := append(src[:], dst[:]...)
	rec = be32(rec, 777)
	rec = append(rec, 0xAA, 0xBB)
	rec = append(rec, 4, 'i', 'g', 'b', '0')
	rec = binary.BigEndian.AppendUint64(rec, uint64(now.UnixMilli()))

	var sets []byte
	sets = be16(sets, 2)
	sets = be16(sets, uint16(4+len(tpl)))
	sets = append(sets, tpl...)
	sets = be16(sets, 300)
	sets = be16(sets, uint16(4+len(rec)))
	sets = append(sets, rec...)

	p := be16(nil, 10)
	p = be16(p, uint16(16+len(sets)))
	p = be32(p, uint32(now.Unix()))
	p = be32(p, 1)
	p = be32(p, 0)
	p = append(p, sets...)

	fl, err := NewDecoder().Decode("x", p)
	if err != nil || len(fl) != 1 {
		t.Fatalf("got %v %v", fl, err)
	}
	if fl[0].Src.String() != "2001:db8::10" || fl[0].Bytes != 777 || !fl[0].End.Equal(now) {
		t.Fatalf("bad flow %+v", fl[0])
	}
}

func TestTruncatedDoesNotPanic(t *testing.T) {
	d := NewDecoder()
	full := v9Packet(time.Now(), true)
	for i := range full {
		d.Decode("z", full[:i])
	}
}

// FuzzDecode feeds arbitrary bytes to the decoder: it must never panic or
// hang, whatever arrives on the UDP port. Run longer with:
//
//	go test ./internal/netflow -fuzz=FuzzDecode -fuzztime=5m
func FuzzDecode(f *testing.F) {
	now := time.Now()
	f.Add(v9Packet(now, true))
	f.Add(v9Packet(now, false))
	f.Add([]byte{0, 5, 0, 1})
	f.Add([]byte{0, 10, 0, 16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	d := NewDecoder()
	f.Fuzz(func(t *testing.T, b []byte) {
		d.Decode("fuzz", b)
		// a second packet reuses templates learned from the first
		d.Decode("fuzz", b)
	})
}

func TestTemplateCap(t *testing.T) {
	d := NewDecoder()
	for i := 0; i < maxTemplates+500; i++ {
		d.parseTemplates("x", uint32(i), []byte{1, 0, 0, 1, 0, 1, 0, 4}, false)
	}
	if len(d.templates) > maxTemplates {
		t.Fatalf("template cache grew to %d", len(d.templates))
	}
}
