// Package netflow decodes NetFlow v5, NetFlow v9 and IPFIX (v10) packets.
//
// pfSense (softflowd package) can export v5, v9 and IPFIX; OPNsense
// (Reporting > NetFlow) exports v5 or v9.
package netflow

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Flow is one unidirectional flow record.
type Flow struct {
	Src, Dst         netip.Addr
	SrcPort, DstPort uint16
	Proto            uint8
	Bytes, Packets   uint64
	Start, End       time.Time
	InIf, OutIf      uint32
}

// IANA information element IDs (shared by NetFlow v9 and IPFIX).
const (
	fInBytes       = 1
	fInPkts        = 2
	fProtocol      = 4
	fSrcPort       = 7
	fSrcAddr4      = 8
	fInputSNMP     = 10
	fDstPort       = 11
	fDstAddr4      = 12
	fOutputSNMP    = 14
	fLastSwitched  = 21
	fFirstSwitched = 22
	fOutBytes      = 23
	fOutPkts       = 24
	fSrcAddr6      = 27
	fDstAddr6      = 28
	fSamplingIntvl = 34
	fOctetTotal    = 85
	fPacketTotal   = 86
	fStartSec      = 150
	fEndSec        = 151
	fStartMilli    = 152
	fEndMilli      = 153
	fSysInitMilli  = 160
	varLen         = 0xFFFF
	maxTemplates   = 4096 // across all exporters; bounds memory under spoofing
	maxFlowAge     = 24 * time.Hour
)

type field struct {
	id     uint16
	length uint16
}

type tmplKey struct {
	exporter string
	domain   uint32
	id       uint16
}

// Decoder keeps template state per exporter. It is safe for concurrent use.
type Decoder struct {
	mu        sync.RWMutex
	templates map[tmplKey][]field
	// Missing counts data records dropped because their template has not
	// arrived yet (normal for the first minute after startup).
	Missing uint64
}

func NewDecoder() *Decoder {
	return &Decoder{templates: map[tmplKey][]field{}}
}

var errShort = errors.New("netflow: packet too short")

// Decode parses one UDP payload from exporter and returns the flows it contains.
func (d *Decoder) Decode(exporter string, b []byte) ([]Flow, error) {
	if len(b) < 2 {
		return nil, errShort
	}
	switch v := binary.BigEndian.Uint16(b); v {
	case 5:
		return decodeV5(b)
	case 9:
		return d.decodeV9(exporter, b)
	case 10:
		return d.decodeIPFIX(exporter, b)
	default:
		return nil, fmt.Errorf("netflow: unsupported version %d", v)
	}
}

func decodeV5(b []byte) ([]Flow, error) {
	if len(b) < 24 {
		return nil, errShort
	}
	count := int(binary.BigEndian.Uint16(b[2:]))
	uptime := binary.BigEndian.Uint32(b[4:])
	secs := binary.BigEndian.Uint32(b[8:])
	nsecs := binary.BigEndian.Uint32(b[12:])
	sampling := uint64(binary.BigEndian.Uint16(b[22:]) & 0x3FFF)
	if sampling == 0 {
		sampling = 1
	}
	if len(b) < 24+count*48 {
		return nil, errShort
	}
	export := time.Unix(int64(secs), int64(nsecs))
	boot := export.Add(-time.Duration(uptime) * time.Millisecond)
	flows := make([]Flow, 0, count)
	for i := 0; i < count; i++ {
		r := b[24+i*48:]
		f := Flow{
			Src:     netip.AddrFrom4([4]byte(r[0:4])),
			Dst:     netip.AddrFrom4([4]byte(r[4:8])),
			InIf:    uint32(binary.BigEndian.Uint16(r[12:])),
			OutIf:   uint32(binary.BigEndian.Uint16(r[14:])),
			Packets: uint64(binary.BigEndian.Uint32(r[16:])) * sampling,
			Bytes:   uint64(binary.BigEndian.Uint32(r[20:])) * sampling,
			Start:   boot.Add(time.Duration(binary.BigEndian.Uint32(r[24:])) * time.Millisecond),
			End:     boot.Add(time.Duration(binary.BigEndian.Uint32(r[28:])) * time.Millisecond),
			SrcPort: binary.BigEndian.Uint16(r[32:]),
			DstPort: binary.BigEndian.Uint16(r[34:]),
			Proto:   r[38],
		}
		flows = append(flows, sanitize(f, export))
	}
	return flows, nil
}

func (d *Decoder) decodeV9(exporter string, b []byte) ([]Flow, error) {
	if len(b) < 20 {
		return nil, errShort
	}
	uptime := binary.BigEndian.Uint32(b[4:])
	export := time.Unix(int64(binary.BigEndian.Uint32(b[8:])), 0)
	domain := binary.BigEndian.Uint32(b[16:])
	boot := export.Add(-time.Duration(uptime) * time.Millisecond)
	ctx := recordCtx{export: export, boot: boot, v9: true}

	var flows []Flow
	for p := b[20:]; len(p) >= 4; {
		id := binary.BigEndian.Uint16(p)
		l := int(binary.BigEndian.Uint16(p[2:]))
		if l < 4 || l > len(p) {
			return flows, errShort
		}
		body := p[4:l]
		p = p[l:]
		switch {
		case id == 0:
			d.parseTemplates(exporter, domain, body, false)
		case id == 1:
			// options template: not needed
		case id >= 256:
			flows = d.parseData(exporter, domain, id, body, ctx, flows)
		}
	}
	return flows, nil
}

func (d *Decoder) decodeIPFIX(exporter string, b []byte) ([]Flow, error) {
	if len(b) < 16 {
		return nil, errShort
	}
	total := int(binary.BigEndian.Uint16(b[2:]))
	if total < 16 || total > len(b) {
		return nil, errShort
	}
	b = b[:total]
	export := time.Unix(int64(binary.BigEndian.Uint32(b[4:])), 0)
	domain := binary.BigEndian.Uint32(b[12:])
	ctx := recordCtx{export: export}

	var flows []Flow
	for p := b[16:]; len(p) >= 4; {
		id := binary.BigEndian.Uint16(p)
		l := int(binary.BigEndian.Uint16(p[2:]))
		if l < 4 || l > len(p) {
			return flows, errShort
		}
		body := p[4:l]
		p = p[l:]
		switch {
		case id == 2:
			d.parseTemplates(exporter, domain, body, true)
		case id == 3:
			// options template: not needed
		case id >= 256:
			flows = d.parseData(exporter, domain, id, body, ctx, flows)
		}
	}
	return flows, nil
}

func (d *Decoder) parseTemplates(exporter string, domain uint32, b []byte, ipfix bool) {
	for len(b) >= 4 {
		id := binary.BigEndian.Uint16(b)
		n := int(binary.BigEndian.Uint16(b[2:]))
		b = b[4:]
		if id < 256 {
			return // padding
		}
		fields := make([]field, 0, n)
		for i := 0; i < n; i++ {
			if len(b) < 4 {
				return
			}
			f := field{id: binary.BigEndian.Uint16(b), length: binary.BigEndian.Uint16(b[2:])}
			b = b[4:]
			if ipfix && f.id&0x8000 != 0 {
				// enterprise-specific element: skip the PEN, never matches IANA ids
				if len(b) < 4 {
					return
				}
				b = b[4:]
				f.id = 0
			}
			fields = append(fields, f)
		}
		k := tmplKey{exporter, domain, id}
		d.mu.Lock()
		if n == 0 {
			delete(d.templates, k) // IPFIX template withdrawal
		} else if _, ok := d.templates[k]; ok || len(d.templates) < maxTemplates {
			d.templates[k] = fields
		}
		d.mu.Unlock()
	}
}

type recordCtx struct {
	export, boot time.Time
	v9           bool
}

func (d *Decoder) parseData(exporter string, domain uint32, id uint16, b []byte, ctx recordCtx, out []Flow) []Flow {
	d.mu.RLock()
	fields, ok := d.templates[tmplKey{exporter, domain, id}]
	d.mu.RUnlock()
	if !ok {
		d.mu.Lock()
		d.Missing++
		d.mu.Unlock()
		return out
	}
	minLen := 0
	for _, f := range fields {
		if f.length != varLen {
			minLen += int(f.length)
		} else {
			minLen++
		}
	}
	if minLen == 0 {
		return out
	}
	for len(b) >= minLen {
		var (
			f                          Flow
			outBytes, outPkts          uint64
			first, last                uint64
			haveRel, haveSec, haveMs   bool
			startS, endS, startM, endM uint64
			sysInit                    uint64
			sampling                   uint64 = 1
		)
		for _, fd := range fields {
			l := int(fd.length)
			if fd.length == varLen {
				if len(b) < 1 {
					return out
				}
				l = int(b[0])
				b = b[1:]
				if l == 255 {
					if len(b) < 2 {
						return out
					}
					l = int(binary.BigEndian.Uint16(b))
					b = b[2:]
				}
			}
			if len(b) < l {
				return out
			}
			v := b[:l]
			b = b[l:]
			switch fd.id {
			case fInBytes, fOctetTotal:
				f.Bytes = uintN(v)
			case fInPkts, fPacketTotal:
				f.Packets = uintN(v)
			case fOutBytes:
				outBytes = uintN(v)
			case fOutPkts:
				outPkts = uintN(v)
			case fProtocol:
				f.Proto = uint8(uintN(v))
			case fSrcPort:
				f.SrcPort = uint16(uintN(v))
			case fDstPort:
				f.DstPort = uint16(uintN(v))
			case fSrcAddr4, fSrcAddr6:
				f.Src = addr(v)
			case fDstAddr4, fDstAddr6:
				f.Dst = addr(v)
			case fInputSNMP:
				f.InIf = uint32(uintN(v))
			case fOutputSNMP:
				f.OutIf = uint32(uintN(v))
			case fFirstSwitched:
				first, haveRel = uintN(v), true
			case fLastSwitched:
				last, haveRel = uintN(v), true
			case fStartSec:
				startS, haveSec = uintN(v), true
			case fEndSec:
				endS, haveSec = uintN(v), true
			case fStartMilli:
				startM, haveMs = uintN(v), true
			case fEndMilli:
				endM, haveMs = uintN(v), true
			case fSysInitMilli:
				sysInit = uintN(v)
			case fSamplingIntvl:
				if s := uintN(v); s > 1 {
					sampling = s
				}
			}
		}
		switch {
		case haveMs:
			startM, endM = fillPair(startM, endM)
			f.Start, f.End = time.UnixMilli(int64(startM)), time.UnixMilli(int64(endM))
		case haveSec:
			startS, endS = fillPair(startS, endS)
			f.Start, f.End = time.Unix(int64(startS), 0), time.Unix(int64(endS), 0)
		case haveRel && (ctx.v9 || sysInit > 0):
			// uptime-relative: v9 uses the header's sysUptime, IPFIX needs systemInitTime
			boot := ctx.boot
			if !ctx.v9 {
				boot = time.UnixMilli(int64(sysInit))
			}
			first, last = fillPair(first, last)
			f.Start = boot.Add(time.Duration(first) * time.Millisecond)
			f.End = boot.Add(time.Duration(last) * time.Millisecond)
		}
		if !f.Src.IsValid() || !f.Dst.IsValid() {
			continue
		}
		f.Bytes *= sampling
		f.Packets *= sampling
		f = sanitize(f, ctx.export)
		if f.Bytes > 0 {
			out = append(out, f)
		}
		if outBytes > 0 {
			// bidirectional record: emit the reverse direction as its own flow
			r := f
			r.Src, r.Dst = f.Dst, f.Src
			r.SrcPort, r.DstPort = f.DstPort, f.SrcPort
			r.InIf, r.OutIf = f.OutIf, f.InIf
			r.Bytes, r.Packets = outBytes*sampling, outPkts*sampling
			out = append(out, r)
		}
	}
	return out
}

// sanitize makes sure timestamps are usable even when the exporter's clock or
// uptime math is off: anything implausible collapses to the export time.
func sanitize(f Flow, export time.Time) Flow {
	if f.End.IsZero() || f.End.Sub(export) > time.Minute || export.Sub(f.End) > maxFlowAge {
		f.End = export
	}
	if f.Start.IsZero() || f.Start.After(f.End) || f.End.Sub(f.Start) > maxFlowAge {
		f.Start = f.End
	}
	return f
}

// fillPair copies whichever of start/end is present into the missing one.
func fillPair(a, b uint64) (uint64, uint64) {
	if a == 0 {
		a = b
	}
	if b == 0 {
		b = a
	}
	return a, b
}

func uintN(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func addr(b []byte) netip.Addr {
	switch len(b) {
	case 4:
		return netip.AddrFrom4([4]byte(b))
	case 16:
		return netip.AddrFrom16([16]byte(b)).Unmap()
	}
	return netip.Addr{}
}
