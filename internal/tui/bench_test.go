package tui

import (
	"testing"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func benchModel(b *testing.B, rows, cols int) *model {
	b.Helper()
	st := protocol.NewState()
	protocol.ApplyRecord(st, playingRecord())
	m := newModel(st, defaultCfg(), make(chan *protocol.Command, 64), nil)
	m.rows, m.cols = rows, cols
	m.sty = newTheme()
	m.sty.trueColor = true
	return m
}

func BenchmarkMotifBlock(b *testing.B) {
	t := newTheme()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		sink = t.motifBlock(30, 16, i)
	}
}

func BenchmarkSonar(b *testing.B) {
	t := newTheme()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		sink = t.sonar(30, 16, i)
	}
}

func BenchmarkViewFull(b *testing.B) {
	m := benchModel(b, 44, 150)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.frame = i // defeat the motif cache the way a live animated frame does
		sinkS = m.viewContent()
	}
}

func BenchmarkViewCompact(b *testing.B) {
	m := benchModel(b, 20, 90)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.frame = i
		sinkS = m.viewContent()
	}
}

func BenchmarkViewDiag(b *testing.B) {
	m := benchModel(b, 44, 150)
	m.diag = true
	b.ReportAllocs()
	for b.Loop() {
		sinkS = m.viewContent()
	}
}

func BenchmarkDispW(b *testing.B) {
	const s = "Everything In Its Right Place — Radiohead · Kid A"
	b.ReportAllocs()
	for b.Loop() {
		sinkI = DispW(s)
	}
}

func BenchmarkClip(b *testing.B) {
	const s = "Everything In Its Right Place — Radiohead · Kid A"
	b.ReportAllocs()
	for b.Loop() {
		sinkS = Clip(s, 30)
	}
}

func BenchmarkLineMeter(b *testing.B) {
	t := newTheme()
	b.ReportAllocs()
	for b.Loop() {
		sinkS = t.lineMeter(0.42, 60)
	}
}

func BenchmarkGaugeBar(b *testing.B) {
	t := newTheme()
	b.ReportAllocs()
	for b.Loop() {
		sinkS = t.gaugeBar(0.42, 12, t.sAcc)
	}
}

func BenchmarkVbar(b *testing.B) {
	t := newTheme()
	b.ReportAllocs()
	for b.Loop() {
		sink = t.vbar(0.42, 20)
	}
}

func BenchmarkHslHex(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkS = hslHex(210, 0.7, 0.5)
	}
}

func BenchmarkComputeTitle(b *testing.B) {
	m := benchModel(b, 44, 150)
	s := m.st.Snap()
	b.ReportAllocs()
	for b.Loop() {
		sinkS = m.computeTitle(s)
	}
}

func BenchmarkMarquee(b *testing.B) {
	m := benchModel(b, 44, 150)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		m.scroll = i
		sinkS = m.marquee("Everything In Its Right Place — Radiohead · Kid A", 24)
	}
}

func BenchmarkSnap(b *testing.B) {
	st := protocol.NewState()
	protocol.ApplyRecord(st, playingRecord())
	b.ReportAllocs()
	for b.Loop() {
		sinkSnap = st.Snap()
	}
}

func BenchmarkEqSliders(b *testing.B) {
	m := benchModel(b, 44, 150)
	s := m.st.Snap()
	b.ReportAllocs()
	for b.Loop() {
		sink = m.eqSliders(s, 140)
	}
}

func BenchmarkBoxArt(b *testing.B) {
	m := benchModel(b, 44, 150)
	art := m.sty.motifBlock(30, 16, 0)
	b.ReportAllocs()
	for b.Loop() {
		sink = m.boxArt(art, 30)
	}
}

func BenchmarkNoteBox(b *testing.B) {
	m := benchModel(b, 44, 150)
	b.ReportAllocs()
	for b.Loop() {
		sink = m.noteBox(30, 16)
	}
}

var (
	sink     []string
	sinkS    string
	sinkI    int
	sinkSnap protocol.Snapshot
)
