// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
)

// TestTheShapeCheckIsLinearInAnExtensionsContent: an extension placeholder's content is an xs:any
// of unbounded occurrence that the sender chooses freely, and it is decoded before the request is
// authenticated. A shape check that compared each repeated child name against every other one
// let a request of under 1 MiB -- tens of thousands of distinct elements, each sent twice -- hold
// a core for seconds. The cost of eight times the content must stay near eight times the cost.
//
// The sizes are large because the quadratic form only separates from the linear one there: at
// 1000 and 4000 pairs it cost under 10x for 4x the content, which a bound loose enough to absorb
// a loaded machine's noise could not tell from linear. At 5000 and 40000 pairs it costs about
// 40x for 8x, and the linear form about 7x.
//
// Not under the race detector. What it measures is the decoder's growth, which the detector's
// instrumentation slows several times over and makes noisier, and it was most of the package's
// time in a -race run. The CI test jobs run without -race, so it runs there on every change.
func TestTheShapeCheckIsLinearInAnExtensionsContent(t *testing.T) {
	if raceDetector {
		t.Skip("a cost ratio is not measured under the race detector; the CI test jobs run it without -race")
	}
	siblings := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "<a%d/><a%d/>", i, i)
		}

		return b.String()
	}

	for _, c := range []struct {
		name, open, close string
		into              func() any
	}{
		{
			"targetIdentifierExtension", `<targetIdentifierExtension><Owner>3GPP</Owner>`, `</targetIdentifierExtension>`,
			func() any { return new(TargetIdentifierExtension) },
		},
		{
			"taskDetailsExtensions", `<taskDetailsExtensions><Owner>3GPP</Owner>`, `</taskDetailsExtensions>`,
			func() any { return new(MessageExtension) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The best of three runs, so a pause on a loaded machine does not count. The larger
			// body is most of the test's time, but with the best of two its ratio still passed
			// the bound now and then on an idle machine, from one pause in both runs.
			cost := func(n, runs int) time.Duration {
				body := []byte(c.open + siblings(n) + c.close)
				best := time.Duration(math.MaxInt64)
				for range runs {
					start := time.Now()
					if err := xml.Unmarshal(body, c.into()); err != nil {
						t.Fatalf("decode: %v", err)
					}
					best = min(best, time.Since(start))
				}

				return best
			}

			small, large := cost(5000, 3), cost(40000, 3)
			t.Logf("5000 pairs %s, 40000 pairs %s", small, large)
			if large > 20*small {
				t.Errorf("40000 repeated pairs took %s against %s for 5000: %.1fx for 8x the content",
					large, small, float64(large)/float64(small))
			}
		})
	}
}

// TestARefusalQuotesAFewNamesNotAll: a refusal names the unmodelled elements a request carried,
// so the peer can see its mistake -- but a request's worth of them made the answer as large as
// the request.
func TestARefusalQuotesAFewNamesNotAll(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "<ns1:unmodelled%d>x</ns1:unmodelled%d>", i, i)
	}
	for _, c := range []struct{ name, body string }{
		{"target identifier extension", withTarget(`<ns1:targetIdentifierExtension><ns1:Owner>3GPP</ns1:Owner>` +
			b.String() + `</ns1:targetIdentifierExtension>`)},
		{"task extension", withTaskDetails("</ns1:taskDetails>", `<ns1:taskDetailsExtensions><ns1:Owner>3GPP</ns1:Owner>`+
			b.String()+`</ns1:taskDetailsExtensions></ns1:taskDetails>`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := serve(t, testServer(store.New()), c.body)
			if m.ErrorInformation == nil {
				t.Fatal("the request was acknowledged")
			}
			d := m.ErrorInformation.ErrorDescription
			if !strings.Contains(d, "unmodelled0") || !strings.Contains(d, "992 more") {
				t.Errorf("description %q does not name the first elements and count the rest", d)
			}
			if len(d) > 512 {
				t.Errorf("description is %d bytes for one refusal", len(d))
			}
		})
	}
}

// heldAtLastChild is an element whose children are decoded through decodeChildren, as every
// strictly decoded request structure is, and which reads how much of the heap is live when the
// decode reaches its last child.
type heldAtLastChild struct {
	children int
	live     uint64
}

func (h *heldAtLastChild) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	probe := lastChildProbe{at: h.children, live: &h.live}
	children := &struct {
		A *lastChildProbe `xml:"a"`
	}{A: &probe}
	_, _, err := decodeChildren(d, start, children)

	return err
}

// lastChildProbe counts the children decoded into it and, at the last, collects garbage and reads
// the live heap.
type lastChildProbe struct {
	seen, at int
	live     *uint64
}

func (p *lastChildProbe) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	p.seen++
	if p.seen == p.at {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		*p.live = ms.HeapAlloc
	}

	return d.Skip()
}

// TestTheStrictDecodeHoldsNoElementWhole: decodeChildren runs before a request is authenticated,
// once per element whose decode calls it, nested as deep as those elements are. It collected
// each element's tokens before decoding them, so every token of a request was held once per
// level, and a body of under 1 MiB of empty elements in a task extension held about 186 MiB at
// its peak. The tokens now pass through as they are read, so what is live at an element's last
// child does not grow with the children before it.
func TestTheStrictDecodeHoldsNoElementWhole(t *testing.T) {
	const children = 100000
	body := []byte("<x>" + strings.Repeat("<a/>", children) + "</x>")

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.HeapAlloc

	h := &heldAtLastChild{children: children}
	if err := xml.Unmarshal(body, h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.live == 0 {
		t.Fatal("the decode never reached the last child")
	}

	// Collected whole, 100000 children held about 13 MiB of tokens at the last one; passed
	// through, none of them is live there. The bound leaves room for whatever else the binary
	// holds.
	grew := int64(h.live) - int64(before)
	t.Logf("live heap grew %d KiB by the last of %d children", grew/1024, children)
	if grew > 4<<20 {
		t.Errorf("live heap grew %d KiB by the last of %d children: the element is held whole", grew/1024, children)
	}
}
