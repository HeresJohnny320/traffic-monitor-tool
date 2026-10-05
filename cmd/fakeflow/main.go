// fakeflow sends synthetic NetFlow v9 to a collector so you can try the
// dashboard without a firewall:
//
//	go run ./cmd/fakeflow -to 127.0.0.1:2055
package main

import (
	"encoding/binary"
	"flag"
	"log"
	"math/rand/v2"
	"net"
	"time"
)

type host struct {
	ip     [4]byte
	weight float64 // relative activity
}

func main() {
	to := flag.String("to", "127.0.0.1:2055", "collector address")
	every := flag.Duration("every", 2*time.Second, "send interval")
	flag.Parse()

	conn, err := net.Dial("udp", *to)
	if err != nil {
		log.Fatal(err)
	}
	hosts := []host{
		{[4]byte{192, 168, 1, 10}, 8}, {[4]byte{192, 168, 1, 11}, 3}, {[4]byte{192, 168, 1, 20}, 1},
		{[4]byte{192, 168, 1, 50}, 5}, {[4]byte{192, 168, 20, 5}, 0.5}, {[4]byte{192, 168, 20, 6}, 0.3},
		{[4]byte{192, 168, 30, 2}, 2}, {[4]byte{10, 6, 0, 2}, 1}, {[4]byte{10, 6, 0, 3}, 0.4},
	}
	remotes := [][4]byte{{142, 250, 72, 14}, {104, 16, 132, 229}, {151, 101, 1, 69}, {13, 107, 42, 14}, {52, 94, 236, 248}, {1, 1, 1, 1}}
	ports := []uint16{443, 443, 443, 80, 53, 3478}

	start := time.Now()
	var seq uint32
	for tick := 0; ; tick++ {
		now := time.Now()
		uptime := uint32(now.Sub(start).Milliseconds()) + 10_000
		var recs []byte
		n := 0
		for _, h := range hosts {
			for j := 0; j < 3; j++ {
				r := remotes[rand.IntN(len(remotes))]
				port := ports[rand.IntN(len(ports))]
				// download (remote -> host) is bigger than upload
				down := uint32(h.weight * (50_000 + rand.Float64()*400_000) * every.Seconds())
				up := down / uint32(5+rand.IntN(15))
				recs = rec(recs, r, h.ip, port, uint16(40000+rand.IntN(20000)), down, uptime)
				recs = rec(recs, h.ip, r, uint16(40000+rand.IntN(20000)), port, up, uptime)
				n += 2
			}
		}
		// some LAN <-> LAN traffic (e.g. a NAS)
		recs = rec(recs, [4]byte{192, 168, 1, 10}, [4]byte{192, 168, 1, 20}, 445, 50000, uint32(rand.IntN(3_000_000)), uptime)
		n++

		var pkt []byte
		pkt = binary.BigEndian.AppendUint16(pkt, 9)
		pkt = binary.BigEndian.AppendUint16(pkt, uint16(n+1))
		pkt = binary.BigEndian.AppendUint32(pkt, uptime)
		pkt = binary.BigEndian.AppendUint32(pkt, uint32(now.Unix()))
		seq++
		pkt = binary.BigEndian.AppendUint32(pkt, seq)
		pkt = binary.BigEndian.AppendUint32(pkt, 1)
		if tick%10 == 0 {
			pkt = append(pkt, template()...)
		}
		pkt = binary.BigEndian.AppendUint16(pkt, 256)
		pkt = binary.BigEndian.AppendUint16(pkt, uint16(4+len(recs)))
		pkt = append(pkt, recs...)
		if _, err := conn.Write(pkt); err != nil {
			log.Print(err)
		}
		time.Sleep(*every)
	}
}

var fields = [][2]uint16{{8, 4}, {12, 4}, {7, 2}, {11, 2}, {4, 1}, {1, 4}, {2, 4}, {22, 4}, {21, 4}}

func template() []byte {
	var t []byte
	t = binary.BigEndian.AppendUint16(t, 256)
	t = binary.BigEndian.AppendUint16(t, uint16(len(fields)))
	for _, f := range fields {
		t = binary.BigEndian.AppendUint16(t, f[0])
		t = binary.BigEndian.AppendUint16(t, f[1])
	}
	out := binary.BigEndian.AppendUint16(nil, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(4+len(t)))
	return append(out, t...)
}

func rec(b []byte, src, dst [4]byte, sp, dp uint16, bytes, uptime uint32) []byte {
	b = append(b, src[:]...)
	b = append(b, dst[:]...)
	b = binary.BigEndian.AppendUint16(b, sp)
	b = binary.BigEndian.AppendUint16(b, dp)
	b = append(b, 6)
	b = binary.BigEndian.AppendUint32(b, bytes)
	b = binary.BigEndian.AppendUint32(b, bytes/1200+1)
	b = binary.BigEndian.AppendUint32(b, uptime-2000)
	b = binary.BigEndian.AppendUint32(b, uptime)
	return b
}
