// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"encoding/asn1"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The emitters name each member's tag where they write it, which moves the tag away
// from the declaration it came from: a struct tag corrected and an emitter not, or the
// reverse, would disagree with neither looking wrong on its own. This file checks the
// emitted bytes against the declarations instead, and derives what to expect from them
// alone:
//
//   - a SEQUENCE member's tag, class, form and presence from its `asn1:"..."` struct tag;
//   - a CHOICE alternative's tag from the published module (TS33128Payloads.asn), since
//     the Go types carry no tag for an alternative.
//
// Nothing here reads emit.go. A check that asked the encoder what it should emit would
// agree with it by construction.

// choiceModules maps a struct tag's `choice:` name to the CHOICE it stands for in the
// module.
var choiceModules = map[string]string{
	"supi":                   "SUPI",
	"pei":                    "PEI",
	"gpsi":                   "GPSI",
	"ueEndpointAddress":      "UEEndpointAddress",
	"amfFailureCause":        "AMFFailureCause",
	"fiveGSSubscriberID":     "FiveGSSubscriberID",
	"serviceMessageIdentity": "ServiceMessageIdentity",
	"handoverCause":          "HandoverCause",
	"xiriEvent":              "XIRIEvent",
}

// alternativeNames names the module alternative a Go type stands for where the type's
// own name does not: the three FiveGSSubscriberID arms, whose module types are the
// SUPI/PEI/GPSI CHOICEs, and the two ServiceMessageIdentity arms, which the module
// types inline as OCTET STRING. Every other alternative is found by its type name.
var alternativeNames = map[reflect.Type]string{
	reflect.TypeOf(SubscriberSUPI{}):            "sUPI",
	reflect.TypeOf(SubscriberPEI{}):             "pEI",
	reflect.TypeOf(SubscriberGPSI{}):            "gPSI",
	reflect.TypeOf(ServiceRequestIdentity(nil)): "serviceRequest",
	reflect.TypeOf(ServiceAcceptIdentity(nil)):  "serviceAccept",
}

// modelledAlternatives is every CHOICE alternative this package models, as the Go type
// that carries it: 38 across the 9 CHOICEs. TestASN1EmittedTagsMatchDeclarations fails
// unless each one is exercised.
var modelledAlternatives = map[string][]reflect.Type{
	"supi":                   {reflect.TypeOf(IMSI("")), reflect.TypeOf(NAI(""))},
	"pei":                    {reflect.TypeOf(IMEI("")), reflect.TypeOf(IMEISV(""))},
	"gpsi":                   {reflect.TypeOf(MSISDN("")), reflect.TypeOf(NAI(""))},
	"ueEndpointAddress":      {reflect.TypeOf(IPv4Address(nil)), reflect.TypeOf(IPv6Address(nil)), reflect.TypeOf(MACAddress(nil))},
	"amfFailureCause":        {reflect.TypeOf(FiveGMMCause(0)), reflect.TypeOf(FiveGSMCause(0))},
	"fiveGSSubscriberID":     {reflect.TypeOf(SubscriberSUPI{}), reflect.TypeOf(SubscriberPEI{}), reflect.TypeOf(SubscriberGPSI{})},
	"serviceMessageIdentity": {reflect.TypeOf(ServiceRequestIdentity(nil)), reflect.TypeOf(ServiceAcceptIdentity(nil))},
	"handoverCause": {
		reflect.TypeOf(CauseRadioNetwork(0)), reflect.TypeOf(CauseTransport(0)), reflect.TypeOf(CauseNas(0)),
		reflect.TypeOf(CauseProtocol(0)), reflect.TypeOf(CauseMisc(0)),
	},
	"xiriEvent": func() []reflect.Type {
		var out []reflect.Type
		for _, sample := range goldenSamples() {
			out = append(out, reflect.TypeOf(sample))
		}
		return out
	}(),
}

// tagAudit walks a value and its encoding side by side.
type tagAudit struct {
	t       *testing.T
	choices map[string][]asn1TypedField
	seen    map[string]map[reflect.Type]bool
}

type declared struct {
	tag                        int
	hasTag, explicit, optional bool
	choice                     string
}

func declaration(f reflect.StructField) declared {
	var d declared
	for _, opt := range strings.Split(f.Tag.Get("asn1"), ",") {
		switch {
		case strings.HasPrefix(opt, "tag:"):
			n, err := strconv.Atoi(strings.TrimPrefix(opt, "tag:"))
			if err != nil {
				panic(fmt.Sprintf("%s: unparsable tag %q", f.Name, opt))
			}
			d.tag, d.hasTag = n, true
		case opt == "explicit":
			d.explicit = true
		case opt == "optional":
			d.optional = true
		case strings.HasPrefix(opt, "choice:"):
			d.choice = strings.TrimPrefix(opt, "choice:")
		}
	}
	return d
}

func (a *tagAudit) elements(path string, content []byte) []asn1.RawValue {
	var out []asn1.RawValue
	for len(content) > 0 {
		var rv asn1.RawValue
		rest, err := asn1.Unmarshal(content, &rv)
		if err != nil {
			a.t.Errorf("%s: contents do not parse as a sequence of elements: %v", path, err)
			return out
		}
		out = append(out, rv)
		content = rest
	}
	return out
}

func (a *tagAudit) expect(path string, e asn1.RawValue, class, tag int, compound bool) bool {
	if e.Class != class || e.Tag != tag || e.IsCompound != compound {
		a.t.Errorf("%s is emitted as class %d tag [%d] compound=%v; its declaration says class %d tag [%d] compound=%v",
			path, e.Class, e.Tag, e.IsCompound, class, tag, compound)
		return false
	}
	return true
}

// absent is the declared presence rule: an OPTIONAL member is omitted when it is at its
// type's zero, and a CHOICE member when it holds no alternative or a zero one.
func absent(d declared, v reflect.Value) bool {
	if !d.optional {
		return false
	}
	if v.IsZero() {
		return true
	}
	return v.Kind() == reflect.Interface && v.Elem().IsZero()
}

// structure checks the members of a SEQUENCE against the struct's fields in order.
func (a *tagAudit) structure(path string, v reflect.Value, content []byte) {
	elems := a.elements(path, content)
	i := 0
	for k := range v.NumField() {
		f := v.Type().Field(k)
		if _, tagged := f.Tag.Lookup("asn1"); !tagged {
			continue
		}
		d := declaration(f)
		fv := v.Field(k)
		p := path + "." + f.Name
		if absent(d, fv) {
			continue
		}
		if i >= len(elems) {
			a.t.Errorf("%s is declared present and was not emitted", p)
			return
		}
		e := elems[i]
		i++
		switch {
		case !d.hasTag:
			// An untagged CHOICE member: the alternative stands in the member's place.
			a.alternative(p, d.choice, fv.Elem(), e)
		case d.explicit:
			if a.expect(p, e, asn1.ClassContextSpecific, d.tag, true) {
				inner := a.elements(p, e.Bytes)
				if len(inner) != 1 {
					a.t.Errorf("%s: EXPLICIT [%d] holds %d elements, want 1", p, d.tag, len(inner))
					continue
				}
				a.alternative(p, d.choice, fv.Elem(), inner[0])
			}
		default:
			a.member(p, d, fv, e)
		}
	}
	if i != len(elems) {
		a.t.Errorf("%s: %d elements emitted beyond those its fields declare", path, len(elems)-i)
	}
}

func (a *tagAudit) member(path string, d declared, v reflect.Value, e asn1.RawValue) {
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	switch {
	case v.Kind() == reflect.Struct:
		if a.expect(path, e, asn1.ClassContextSpecific, d.tag, true) {
			a.structure(path, v, e.Bytes)
		}
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		if !a.expect(path, e, asn1.ClassContextSpecific, d.tag, true) {
			return
		}
		elems := a.elements(path, e.Bytes)
		if len(elems) != v.Len() {
			a.t.Errorf("%s: %d elements emitted for a list of %d", path, len(elems), v.Len())
			return
		}
		for i, el := range elems {
			p := fmt.Sprintf("%s[%d]", path, i)
			if d.choice != "" {
				a.alternative(p, d.choice, v.Index(i).Elem(), el)
			} else if a.expect(p, el, asn1.ClassUniversal, asn1.TagSequence, true) {
				a.structure(p, v.Index(i), el.Bytes)
			}
		}
	default:
		a.expect(path, e, asn1.ClassContextSpecific, d.tag, false)
	}
}

// alternative checks one CHOICE alternative, taking its tag from the module.
func (a *tagAudit) alternative(path, choice string, v reflect.Value, e asn1.RawValue) {
	module, ok := choiceModules[choice]
	if !ok {
		a.t.Errorf("%s: choice %q has no module CHOICE mapped in choiceModules", path, choice)
		return
	}
	name, named := alternativeNames[v.Type()]
	tag := -1
	for _, alt := range a.choices[module] {
		if (named && alt.name == name) || (!named && alt.typeName == v.Type().Name()) {
			tag = alt.tag
		}
	}
	if tag < 0 {
		a.t.Errorf("%s: %s is not an alternative of the module's %s CHOICE", path, v.Type().Name(), module)
		return
	}
	if a.seen[choice] == nil {
		a.seen[choice] = map[reflect.Type]bool{}
	}
	a.seen[choice][v.Type()] = true
	p := fmt.Sprintf("%s(%s)", path, v.Type().Name())
	if v.Kind() == reflect.Struct {
		if a.expect(p, e, asn1.ClassContextSpecific, tag, true) {
			a.structure(p, v, e.Bytes)
		}
		return
	}
	a.expect(p, e, asn1.ClassContextSpecific, tag, false)
}

// tagSamples is every golden form, plus the alternatives the golden forms do not
// happen to carry, so each of the 38 is emitted at least once.
func tagSamples() map[string]any {
	samples := goldenForms()
	guti := FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1}
	ids := UserIdentifiers{FiveGS: FiveGSSubscriberIDs{IDs: []any{
		SubscriberSUPI{Value: NAI("user@example.org")},
		SubscriberPEI{Value: IMEI("35342500000001")},
		SubscriberGPSI{Value: NAI("gpsi@example.org")},
	}}}
	samples["extra/GPSI NAI, PEI IMEISV"] = AMFIdentifierDeassociation{
		SUPI: IMSI("262019876543210"), PEI: IMEISV("3534250000000151"), GPSI: NAI("gpsi@example.org"), GUTI: guti,
	}
	samples["extra/every endpoint arm"] = SMFStartOfInterceptionWithEstablishedPDUSession{
		PDUSessionID: 5, GTPTunnelID: FTEID{TEID: 1}, PDUSessionType: PDUSessionTypeIPv4v6,
		UEEndpoint: []any{
			IPv4Address{10, 45, 0, 2},
			IPv6Address(net.ParseIP("2001:db8::7").To16()),
			MACAddress{0x02, 0x42, 0xac, 0x11, 0x00, 0x02},
		},
		DNN: "internet", RequestType: SMRequestExisting,
	}
	for i, cause := range []any{CauseTransport(1), CauseNas(2), CauseProtocol(3)} {
		samples[fmt.Sprintf("extra/handover cause %d", i)] = AMFRANHandoverRequest{
			UserIdentifiers: ids, AMFUENGAPID: 1, RANUENGAPID: 2, HandoverType: HandoverIntra5GS,
			HandoverCause:                 cause,
			PDUSessionResourceInformation: PDUSessionResourceInformation{PDUSessionID: 5},
			TargetToSourceContainer:       RANTargetToSourceContainer{0x01},
			SourceToTargetContainer:       RANSourceToTargetContainer{0x02},
		}
	}
	return samples
}

// TestASN1EmittedTagsMatchDeclarations holds every emitted element — records, nested
// structures, list elements and CHOICE alternatives — to the tag its declaration
// carries, and every declared-present member to having been emitted and every
// declared-absent one to not.
func TestASN1EmittedTagsMatchDeclarations(t *testing.T) {
	a := &tagAudit{t: t, choices: moduleChoices(t), seen: map[string]map[reflect.Type]bool{}}
	for name, sample := range tagSamples() {
		der, err := EncodeXIRI(sample)
		if err != nil {
			t.Fatalf("%s: EncodeXIRI: %v", name, err)
		}
		var outer asn1.RawValue
		rest, err := asn1.Unmarshal(der, &outer)
		if err != nil || len(rest) != 0 {
			t.Fatalf("%s: the payload is not one element: %v (%d trailing bytes)", name, err, len(rest))
		}
		if a.expect(name, outer, asn1.ClassUniversal, asn1.TagSequence, true) {
			a.structure(name, reflect.ValueOf(XIRIPayload{OID: xIRIPayloadOID, Event: sample}), outer.Bytes)
		}
	}

	total := 0
	for choice, types := range modelledAlternatives {
		for _, typ := range types {
			total++
			if !a.seen[choice][typ] {
				t.Errorf("the %s alternative %s is never emitted by any sample, so nothing checks its tag",
					choice, typ.Name())
			}
		}
	}
	if total != 38 {
		t.Errorf("modelledAlternatives lists %d alternatives; the package models 38", total)
	}
}
