// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This is the audit that should have found the last two defects, run by a test instead of
// by someone reading the schema.
//
// The class it catches: `encoding/xml` silently discards an element a struct does not
// declare. On this interface that is not a parsing detail: an unmodelled
// `listOfServiceTypes` would let a task narrowing interception to particular service types
// be acknowledged with the narrowing thrown away, and the provisioning function would have
// no channel through which the divergence could be reported. This test finds such a field
// before a reviewer has to.
//
// It is not the same check as TestRenderedResponsesValidate. That one validates what this
// element *emits* against the published schema, which says nothing about what it fails to
// *read*.
//
// The distinction that makes it work, and that a first pass at this audit got wrong: an
// unmodelled `xs:choice` arm is a refusal, because a value in an arm nothing maps falls
// through to "unsupported" and the message is rejected. An unmodelled `xs:sequence`
// element is a silent discard. Conflating them reported DeliveryAddress's three
// deliberately-unsupported arms as three defects.

// ── the schema side ─────────────────────────────────────────────────

// particleKind distinguishes the two content models, which is the whole basis of the
// audit's severity judgement.
type particleKind int

const (
	particleSequence particleKind = iota
	particleChoice
)

type schemaMember struct {
	name     string
	typeName string
}

type schemaType struct {
	base     string // the type extended, for xs:complexContent/xs:extension
	kind     particleKind
	members  []schemaMember
	wildcard bool // carries an xs:any, so unknown content is the schema's own business
}

// xsdFile is enough of an XSD to enumerate complex types and their members. The vendored
// schemas have no nested particles, no xs:group and no xs:simpleContent — asserted below,
// so a revision that introduces one fails rather than being quietly mis-parsed.
type xsdFile struct {
	ComplexTypes []struct {
		Name           string       `xml:"name,attr"`
		Sequence       *xsdParticle `xml:"sequence"`
		Choice         *xsdParticle `xml:"choice"`
		All            *xsdParticle `xml:"all"`
		ComplexContent *struct {
			Extension *struct {
				Base     string       `xml:"base,attr"`
				Sequence *xsdParticle `xml:"sequence"`
				Choice   *xsdParticle `xml:"choice"`
			} `xml:"extension"`
		} `xml:"complexContent"`
	} `xml:"complexType"`
}

type xsdParticle struct {
	Elements []struct {
		Name string `xml:"name,attr"`
		Type string `xml:"type,attr"`
	} `xml:"element"`
	Any []struct{} `xml:"any"`
	// Nested particles are not supported by this reader and must stay absent.
	Sequences []struct{} `xml:"sequence"`
	Choices   []struct{} `xml:"choice"`
}

// loadSchema reads the vendored schemas into one type table.
//
// One table across both files: TS 103 221-1 refers to TS 103 280 types by prefix, and the
// audit only ever needs the local name. A name defined in both would make that ambiguous,
// so it is an error rather than a silent last-one-wins.
func loadSchema(t *testing.T) map[string]schemaType {
	t.Helper()

	types := map[string]schemaType{}
	for _, name := range []string{"TS_103_221_01.xsd", "TS_103_280.xsd"} {
		b, err := os.ReadFile(filepath.Join(schemaDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var f xsdFile
		if err := xml.Unmarshal(b, &f); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, ct := range f.ComplexTypes {
			var st schemaType
			var p *xsdParticle
			switch {
			case ct.Sequence != nil:
				p, st.kind = ct.Sequence, particleSequence
			case ct.Choice != nil:
				p, st.kind = ct.Choice, particleChoice
			case ct.All != nil:
				p, st.kind = ct.All, particleSequence
			case ct.ComplexContent != nil && ct.ComplexContent.Extension != nil:
				st.base = localName(ct.ComplexContent.Extension.Base)
				if ct.ComplexContent.Extension.Choice != nil {
					p, st.kind = ct.ComplexContent.Extension.Choice, particleChoice
				} else {
					p, st.kind = ct.ComplexContent.Extension.Sequence, particleSequence
				}
			}
			if p != nil {
				if len(p.Sequences) > 0 || len(p.Choices) > 0 {
					t.Fatalf("%s: complexType %s nests a particle, which this reader does not model — "+
						"the audit would silently skip its members", name, ct.Name)
				}
				st.wildcard = len(p.Any) > 0
				for _, el := range p.Elements {
					st.members = append(st.members, schemaMember{name: el.Name, typeName: localName(el.Type)})
				}
			}
			if _, dup := types[ct.Name]; dup {
				t.Fatalf("complexType %s is defined in more than one vendored schema; "+
					"the audit resolves types by local name and can no longer tell them apart", ct.Name)
			}
			types[ct.Name] = st
		}
	}
	if len(types) == 0 {
		t.Fatal("no complex types were read from the vendored schemas")
	}

	return types
}

// localName strips a QName's prefix ("etsi103280:IPAddressPort" -> "IPAddressPort").
func localName(qname string) string {
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		return qname[i+1:]
	}

	return qname
}

// membersOf returns a type's members including those it inherits by extension.
func membersOf(types map[string]schemaType, name string) ([]schemaMember, particleKind, bool) {
	st, ok := types[name]
	if !ok {
		return nil, particleSequence, false
	}

	members := st.members
	if st.base != "" {
		inherited, _, ok := membersOf(types, st.base)
		if ok {
			members = append(append([]schemaMember{}, inherited...), members...)
		}
	}

	return members, st.kind, true
}

// ── the Go side ─────────────────────────────────────────────────────

// declNode is what a Go struct declares at one level of the message, as a tree so that a
// nested field path ("listOfDIDs>dId") declares an element inside a type that has no
// struct of its own.
type declNode struct {
	children map[string]*declNode
	// wildcard marks a `,any` field, which declares whatever arrives.
	wildcard bool
}

func newDeclNode() *declNode { return &declNode{children: map[string]*declNode{}} }

// declaredBy builds the tree a struct type declares.
func declaredBy(goType reflect.Type) *declNode {
	node := newDeclNode()
	goType = deref(goType)
	if goType.Kind() != reflect.Struct {
		return node
	}

	for i := range goType.NumField() {
		field := goType.Field(i)
		tag := field.Tag.Get("xml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(opts, "any") {
			node.wildcard = true

			continue
		}
		if strings.Contains(opts, "attr") || name == "" {
			continue
		}
		// A tag may carry a namespace ("ns local") and a path ("a>b").
		if i := strings.LastIndex(name, " "); i >= 0 {
			name = name[i+1:]
		}

		cur := node
		segments := strings.Split(name, ">")
		for depth, seg := range segments {
			child, ok := cur.children[seg]
			if !ok {
				child = newDeclNode()
				cur.children[seg] = child
			}
			// Only the last segment corresponds to the Go field's own type; the
			// intermediates are wrapper elements with no struct behind them.
			if depth == len(segments)-1 {
				for k, v := range declaredBy(field.Type).children {
					child.children[k] = v
				}
				child.wildcard = child.wildcard || declaredBy(field.Type).wildcard
			}
			cur = child
		}
	}

	return node
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}

	return t
}

// ── the audit ───────────────────────────────────────────────────────

// disregardedElements lists, per schema type, the sequence elements this element neither
// declares nor refuses — and why each is safe to leave alone.
//
// It is the counterpart of knownSchemaDefects and follows the same two rules, so that it
// cannot rot into a description of a past that has moved on:
//
//   - an undeclared sequence element that is not listed here fails the test;
//   - an entry here that no longer names an undeclared element *also* fails the test.
//
// Nothing is on this list because it was inconvenient. Each entry is a case where the
// specification addresses the element to a function this element is not, or where
// discarding it cannot change what is intercepted or where the product goes.
// disregardedResponseElements is the response-side counterpart. It is a separate map
// because the two audits walk different type sets: an entry naming a response type would
// read as stale to the request-side anti-rot check, which never visits it. The rules are
// otherwise identical.
//
// A requester that drops a field of an *answer* misreads what a peer holds, which is a
// different failure from an element that drops a field of an instruction. The two fields of
// TaskStatus a triggering function can act on — provisioningStatus and listOfFaults — are
// modelled and acted on; these are the rest.
var disregardedResponseElements = map[string]map[string]string{
	"TaskStatus": {
		"amountOfX2Data":         "a statistic; the triggering function reconciles against tasking, not volumes, and relaying a figure it has not verified would state it as fact",
		"amountOfX3Data":         "as amountOfX2Data",
		"numberOfModifications":  "a statistic, as above",
		"timeOfLastIntercept":    "a statistic, as above",
		"timeOfLastModification": "a statistic, as above",
		"taskStatusExtensions":   "the schema's extension point; nothing is defined here for this element to act on, and an extension it cannot interpret is not one it can honour",
	},
	"GetAllDetailsResponse": {
		// The requester asks GetAllDetails to learn what tasking a POI holds. The peer's
		// own health is a different question, with its own message (GetNEStatus) and its
		// own condition set.
		//
		// Worth knowing: over the internal triggering interface the
		// ADMF cannot ask the UPF anything — the UPF authenticates its peer as the SMF's
		// triggering function — so this SMF is the only party that could ever relay a
		// UPF's element health onward. If that is wanted, this field is where it comes
		// from.
		"neStatusDetails": "element health is asked with GetNEStatus, not inferred from a tasking answer",
		// The triggering function provisions a destination before the task naming it and
		// treats the create as idempotent, learning from error 2030 that the peer already
		// holds one. Reading the list would save a round trip; it would not change what is
		// provisioned or where product goes.
		"listOfDestinationResponseDetails": "destinations are re-provisioned idempotently; error 2030 already reports one that is held",
		// This element supports no Generic Objects and answers by omitting the list, so
		// there is never one to read.
		"listOfGenericObjectResponseDetails": "this element supports no Generic Objects, so the list is always absent",
	},
}

var disregardedElements = map[string]map[string]string{
	"GetAllGenericObjectDetailsRequest": {
		// The third find of this class, and the first found by a test rather than by
		// reading — which is the whole reason the test exists. It turns out to be
		// harmless, and the reasoning is worth keeping precisely because the answer was
		// not obvious before it was looked up.
		//
		// objectType narrows the query: "If present, only the specific XSD type is
		// required rather than the whole Generic Object Details." This element supports
		// no Generic Objects at all, and answers by omitting the list — which the
		// specification defines for exactly this position ("May be omitted if Generic
		// Objects are not supported by the NE"). That answer is the same whichever type
		// was asked about, so discarding the narrowing cannot change it, and it cannot
		// change what is intercepted or where product goes.
		//
		// It stops being safe the moment Generic Objects are implemented, at which
		// point discarding it would answer a narrow question broadly.
		"objectType": "the answer is 'Generic Objects are not supported', whatever type was asked about",
	},
	"MediationDetails": {
		// The whole structure is disregarded, so the audit stops at its door rather
		// than at each of its fields. TS 103 221-1: mediation details are "for use by
		// an NE that is performing mediation (i.e. a mediation and delivery function).
		// This shall be included between the ADMF and the MDF." The AMF, SMF and UPF
		// host POIs. If this project ever implements an MDF2, every one of these
		// becomes mandatory work — which is the reason they are enumerated here rather
		// than dismissed as one line.
		"StartTime":                     "a mediation concern; a POI is not an MDF",
		"EndTime":                       "a mediation concern; a POI is not an MDF",
		"listOfDIDs":                    "the MDF's own delivery endpoints, not this element's",
		"mediationDetailsExtensions":    "inside a structure this element disregards entirely",
		"serviceScopingOptions":         "a mediation concern; a POI is not an MDF",
		"listOfTrafficPolicyReferences": "the MDF's policy references, not the task's",
	},
}

// auditedRequestTypes pairs each message type this element acts on with the Go type that
// receives it. A message type absent from this list is one nothing here handles, and an
// unhandled type discards everything by definition — which is not this test's subject.
var auditedRequestTypes = []struct {
	xsdType string
	goType  reflect.Type
}{
	{"X1RequestMessage", reflect.TypeOf(X1RequestMessage{})},
	{"ActivateTaskRequest", reflect.TypeOf(X1RequestMessage{})},
	{"ModifyTaskRequest", reflect.TypeOf(X1RequestMessage{})},
	{"DeactivateTaskRequest", reflect.TypeOf(X1RequestMessage{})},
	{"DeactivateAllTasksRequest", reflect.TypeOf(X1RequestMessage{})},
	{"CreateDestinationRequest", reflect.TypeOf(X1RequestMessage{})},
	{"RemoveAllDestinationsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetTaskDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetDestinationDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetAllDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetAllTaskDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetAllDestinationDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetAllGenericObjectDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"ListAllDetailsRequest", reflect.TypeOf(X1RequestMessage{})},
	{"GetNEStatusRequest", reflect.TypeOf(X1RequestMessage{})},
	{"KeepaliveRequest", reflect.TypeOf(X1RequestMessage{})},
	{"PingRequest", reflect.TypeOf(X1RequestMessage{})},
}

// finding is one element the audit has something to say about.
type finding struct{ path string }

// auditor walks a schema type against what a Go struct declares.
type auditor struct {
	types map[string]schemaType
	// silent are undeclared xs:sequence elements: the defect class.
	silent []finding
	// refusable are undeclared xs:choice arms, which a value cannot reach without being
	// refused. Reported for information, never as a failure.
	refusable []finding
	// used records which disregardedElements entries were actually needed, so a stale
	// one can be reported.
	used map[string]map[string]bool
	seen map[string]bool
	// exempt is the disregard list this audit is judged against. The request and
	// response audits pass different maps; see disregardedResponseElements.
	exempt map[string]map[string]string
}

func (a *auditor) walk(path, xsdType string, node *declNode, depth int) {
	// The schemas are shallow and acyclic in the parts audited here; the guard is a
	// backstop against a revision that introduces recursion, not a real bound.
	if depth > 8 || a.seen[path+"|"+xsdType] {
		return
	}
	a.seen[path+"|"+xsdType] = true

	members, kind, ok := membersOf(a.types, xsdType)
	if !ok {
		return // a simple type or a built-in: nothing to descend into
	}
	st := a.types[xsdType]

	for _, m := range members {
		child, declared := node.children[m.name]
		where := path + "/" + m.name

		switch {
		case declared:
			a.walk(where, m.typeName, child, depth+1)
		case node.wildcard || st.wildcard:
			// The struct takes any content here, or the schema itself does.
		case kind == particleChoice:
			// An arm nothing maps: a value in it reaches the "unsupported" branch and
			// the message is refused, so nothing is discarded silently.
			a.refusable = append(a.refusable, finding{path: where})
		default:
			if _, exempt := a.exempt[xsdType][m.name]; exempt {
				if a.used[xsdType] == nil {
					a.used[xsdType] = map[string]bool{}
				}
				a.used[xsdType][m.name] = true

				continue
			}
			a.silent = append(a.silent, finding{path: where})
		}
	}
}

// TestNoHandledFieldIsSilentlyDiscarded is the drift check itself.
func TestNoHandledFieldIsSilentlyDiscarded(t *testing.T) {
	checkVendoredSchemas(t)
	types := loadSchema(t)

	a := &auditor{types: types, used: map[string]map[string]bool{}, seen: map[string]bool{}, exempt: disregardedElements}
	for _, c := range auditedRequestTypes {
		a.walk(c.xsdType, c.xsdType, declaredBy(c.goType), 0)
	}

	for _, f := range a.silent {
		t.Errorf("the schema defines %s, which this element neither declares nor refuses — "+
			"encoding/xml discards it, so a provisioning function is acknowledged and its "+
			"instruction is thrown away. Declare it (so it can be acted on or refused), or add "+
			"it to disregardedElements with the sentence that makes discarding it safe.", f.path)
	}

	// The anti-rot half: an exemption that is no longer needed is an exemption that has
	// stopped describing this element.
	for xsdType, elements := range a.exempt {
		for name := range elements {
			if !a.used[xsdType][name] {
				t.Errorf("the disregard list names %s/%s, but the audit no longer finds it "+
					"undeclared — remove the entry rather than leaving the list describing a "+
					"past that has moved on", xsdType, name)
			}
		}
	}

	t.Logf("%d unmodelled xs:choice arm(s), each a refusal rather than a discard", len(a.refusable))
}

// TestDriftAuditDistinguishesChoiceFromSequence pins the distinction the audit rests on.
// A first pass at this reported DeliveryAddress's three unsupported arms as three
// defects, which is the kind of noise that gets a check switched off.
func TestDriftAuditDistinguishesChoiceFromSequence(t *testing.T) {
	checkVendoredSchemas(t)
	types := loadSchema(t)

	// DeliveryAddress is an xs:choice with four arms; only ipAddressAndPort is modelled.
	// None of the other three may be reported as a silent discard.
	a := &auditor{types: types, used: map[string]map[string]bool{}, seen: map[string]bool{}, exempt: disregardedElements}
	a.walk("DeliveryAddress", "DeliveryAddress", declaredBy(reflect.TypeOf(DeliveryAddress{})), 0)
	if len(a.silent) != 0 {
		t.Errorf("unmodelled choice arms were reported as silent discards: %v", a.silent)
	}
	if len(a.refusable) != 3 {
		t.Errorf("found %d unmodelled DeliveryAddress arms, want 3 (e164Number, uri, emailAddress): %v",
			len(a.refusable), a.refusable)
	}

	// And the other half: a sequence element the struct does not declare must fail. This
	// is the mutation the real test would have to catch, run here against a deliberately
	// incomplete struct so that the check is proven rather than assumed.
	type incompleteTaskDetails struct {
		XID string `xml:"xId"`
	}
	b := &auditor{types: types, used: map[string]map[string]bool{}, seen: map[string]bool{}}
	b.walk("TaskDetails", "TaskDetails", declaredBy(reflect.TypeOf(incompleteTaskDetails{})), 0)
	if len(b.silent) == 0 {
		t.Error("a TaskDetails struct declaring only xId was reported as complete — " +
			"the audit cannot detect the defect it exists for")
	}
	if !containsPath(b.silent, "TaskDetails/listOfDIDs") {
		t.Errorf("the audit did not name the missing listOfDIDs: %v", paths(b.silent))
	}
}

// TestResponseParsingGaps runs the same audit over the types a *requester* parses a peer's
// answer with — the CC Triggering Function reading what a triggered POI reports.
//
// It is reported rather than enforced: a requester that drops a field of an answer misreads
// what a peer holds, which is a different failure from an element that drops a field of an
// instruction, and closing each gap means deciding what the CC-TF should do with that field.
// The list is kept here so that decision starts from it rather than from another reading of
// the schema.
// mergedDisregards combines disposition lists, later entries winning per type+element.
func mergedDisregards(maps ...map[string]map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, m := range maps {
		for xsdType, elements := range m {
			if out[xsdType] == nil {
				out[xsdType] = map[string]string{}
			}
			for name, reason := range elements {
				out[xsdType][name] = reason
			}
		}
	}

	return out
}

func TestResponseParsingGaps(t *testing.T) {
	checkVendoredSchemas(t)
	types := loadSchema(t)

	// Requests and responses share types — TaskDetails and MediationDetails appear in
	// both — so a disposition already recorded for the request side governs the response
	// side too. Only the response-specific entries are anti-rot checked below, because
	// the request entries are exercised by the request audit and would read as stale here.
	a := &auditor{
		types: types, used: map[string]map[string]bool{}, seen: map[string]bool{},
		exempt: mergedDisregards(disregardedElements, disregardedResponseElements),
	}
	for _, c := range []struct {
		xsdType string
		goType  reflect.Type
	}{
		{"X1ResponseMessage", reflect.TypeOf(X1ResponseMessage{})},
		{"GetTaskDetailsResponse", reflect.TypeOf(X1ResponseMessage{})},
		{"GetAllDetailsResponse", reflect.TypeOf(X1ResponseMessage{})},
		{"TaskResponseDetails", reflect.TypeOf(TaskResponseDetails{})},
	} {
		a.walk(c.xsdType, c.xsdType, declaredBy(c.goType), 0)
	}

	// The anti-rot half, over the response-specific entries only.
	for xsdType, elements := range disregardedResponseElements {
		for name := range elements {
			if !a.used[xsdType][name] {
				t.Errorf("disregardedResponseElements lists %s/%s, but the audit no longer finds "+
					"it undeclared — remove the entry rather than leaving the list describing a "+
					"past that has moved on", xsdType, name)
			}
		}
	}

	if len(a.silent) == 0 {
		t.Log("the response-parsing structs declare every sequence element the schema defines")

		return
	}
	gaps := paths(a.silent)
	sort.Strings(gaps)
	t.Errorf("%d response element(s) a requester parsing a peer's answer would discard, and "+
		"which are neither modelled nor listed in disregardedResponseElements. Decide what a "+
		"requester should do with each, then act on it or record why not:\n  %s",
		len(gaps), strings.Join(gaps, "\n  "))
}

func paths(fs []finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.path)
	}

	return out
}

func containsPath(fs []finding, want string) bool {
	for _, f := range fs {
		if f.path == want {
			return true
		}
	}

	return false
}
